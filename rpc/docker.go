// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

// The Docker backend's side of the service: the routes an administrator uses to manage
// standalone emulators and the backend's settings, the authenticated KasmVNC proxy, and the
// session start that puts a standalone emulator in a container when, and only when, the backend
// is on and an installed emulator plays the console. Everything else goes the way it always has.
//
// Who is asking comes from the server, which is the only thing that can reach these routes (the
// service token, see package service): it sets HeaderUser to the signed-in account and
// HeaderAdmin to "1" for an administrator, and strips both from what a browser sent. A route
// that needs either refuses a request without it, so a server that doesn't send them yet gets
// refusals, never access.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
	"github.com/wittywilla/wittyplay/stream"
)

// Headers the server sets on what it passes through.
const (
	HeaderUser  = "X-Witty-User"  // the signed-in account's id
	HeaderAdmin = "X-Witty-Admin" // "1" for an administrator
)

// DataEnv is where the server says this module's own folder is (the "data" permission).
const DataEnv = "WITTYMODULE_DATA"

// ConnectDocker reaches the Docker daemon. The module's command sets it to the engine; nothing
// else imports the Docker SDK. Left nil, Docker is never available.
var ConnectDocker func() (docker.Runtime, error)

// Docker is this process's Docker backend.
var Docker = NewDockerBackend(os.Getenv(DataEnv), func() (docker.Runtime, error) {
	if ConnectDocker == nil {
		return nil, docker.ErrUnavailable
	}
	return ConnectDocker()
}, Mgr, nil)

// DockerBackend holds the Docker backend's state for one module process.
type DockerBackend struct {
	dataDir string
	connect func() (docker.Runtime, error)
	mgr     *stream.Manager
	log     *slog.Logger
	proxy   docker.KasmProxy
	inst    *emulators.Installer

	mu       sync.Mutex
	settings docker.Settings
	rt       docker.Runtime
	lc       *docker.Lifecycle
	jobs     map[string]*InstallJob
	sessions map[string]*DockerSession
	runCtx   context.Context
	stopRun  context.CancelFunc
	started  sync.Once
}

// NewDockerBackend makes a backend keeping its files under dataDir/docker and dataDir/emulators.
// With dataDir empty, settings live only in memory, and nothing can be installed.
func NewDockerBackend(dataDir string, connect func() (docker.Runtime, error), mgr *stream.Manager, log *slog.Logger) *DockerBackend {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	b := &DockerBackend{dataDir: dataDir, connect: connect, mgr: mgr, log: log, settings: docker.DefaultSettings(),
		jobs: map[string]*InstallJob{}, sessions: map[string]*DockerSession{}}
	b.runCtx, b.stopRun = context.WithCancel(context.Background())
	if dataDir != "" {
		b.inst = &emulators.Installer{DataDir: dataDir, Client: safeClient, OnProgress: b.progress}
		if s, err := docker.LoadSettings(b.settingsPath()); err != nil {
			log.Warn("docker: the saved settings are unreadable; Docker stays off", "err", err)
		} else {
			b.settings = s
		}
	}
	return b
}

func (b *DockerBackend) settingsPath() string {
	return filepath.Join(b.dataDir, "docker", "settings.json")
}

// Settings are the current settings.
func (b *DockerBackend) Settings() docker.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.settings
}

// runtime returns the daemon when Docker is on and answers, or why not.
func (b *DockerBackend) runtime(ctx context.Context) (docker.Runtime, docker.Status, error) {
	b.mu.Lock()
	enabled, rt := b.settings.Enabled, b.rt
	b.mu.Unlock()
	if !enabled {
		return nil, docker.Status{Error: "the Docker backend is off"}, errDockerOff
	}
	if rt == nil {
		if b.connect == nil {
			return nil, docker.Status{Error: docker.ErrUnavailable.Error()}, docker.ErrUnavailable
		}
		r, err := b.connect()
		if err != nil || r == nil {
			st := docker.Status{Error: docker.ErrUnavailable.Error()}
			if err != nil {
				st.Error = err.Error()
			}
			return nil, st, docker.ErrUnavailable
		}
		b.mu.Lock()
		if b.rt == nil {
			b.rt = r
		}
		rt = b.rt
		b.mu.Unlock()
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st := rt.Available(cctx)
	if !st.Available {
		return nil, st, docker.ErrUnavailable
	}
	return rt, st, nil
}

// lifecycle is the lifecycle for rt, made the first time it's needed.
func (b *DockerBackend) lifecycle(rt docker.Runtime) *docker.Lifecycle {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.lc == nil || b.lc.RT != rt {
		b.lc = b.settings.Lifecycle(rt)
		b.lc.Log = b.log
		b.lc.Run(b.runCtx)
	}
	return b.lc
}

var errDockerOff = errors.New("the Docker backend is off; an administrator can turn it on in Settings, Consoles & cores")

// Startup removes containers a crash left behind, when Docker is on and reachable. It never
// fails: with Docker off or missing it does nothing, and says why in the log.
func (b *DockerBackend) Startup(ctx context.Context) {
	b.started.Do(func() {
		rt, st, err := b.runtime(ctx)
		if err != nil {
			if !errors.Is(err, errDockerOff) {
				b.log.Info("docker: not reachable at startup; standalone emulators stay off", "err", st.Error)
			}
			return
		}
		n, err := b.lifecycle(rt).SweepOrphans(ctx)
		if err != nil {
			b.log.Warn("docker: removing containers left from before", "removed", n, "err", err)
			return
		}
		if n > 0 {
			b.log.Info("docker: removed containers left from before", "removed", n)
		}
	})
}

// Count is how many containers this module is running.
func (b *DockerBackend) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

// StopAll ends every container, as the module goes away.
func (b *DockerBackend) StopAll() {
	b.mu.Lock()
	lc := b.lc
	b.mu.Unlock()
	if lc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		lc.StopAll(ctx)
		cancel()
	}
	b.stopRun()
}

// ---------------------------------------------------------------- sessions

// DockerSession is a game in a container.
type DockerSession struct {
	ID       string
	UserID   int64
	Emulator string
	Backend  docker.Backend
	KasmURL  string // for docker+kasmvnc: the proxy's address, relative to the module's routes

	relay *stream.Relay
	fwd   *inputagent.Forwarder
	c     *docker.Container
}

// MarshalJSON is what a server gets back from a start: the id, as for a game, and how to reach
// it.
func (s *DockerSession) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID       string         `json:"id"`
		Backend  docker.Backend `json:"backend"`
		Emulator string         `json:"emulator"`
		Connect  string         `json:"connect,omitempty"`
		KasmURL  string         `json:"kasmUrl,omitempty"`
	}{s.ID, s.Backend, s.Emulator, s.connectPath(), s.KasmURL})
}

func (s *DockerSession) connectPath() string {
	if s.Backend == docker.BackendDockerWebRTC {
		return "/docker/sessions/" + s.ID + "/connect"
	}
	return ""
}

// Done is closed when its container has gone.
func (s *DockerSession) Done() <-chan struct{} { return s.c.Done() }

// Session finds a game in a container.
func (b *DockerBackend) Session(id string) *DockerSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}

// pick is the installed standalone emulator that plays a console, or nil.
func (b *DockerBackend) pick(console string) (*emulators.Emulator, *emulators.Installed) {
	if b.inst == nil || console == "" {
		return nil, nil
	}
	for _, e := range emulators.ForSystem(console) {
		if in := b.inst.Get(e.ID); in != nil {
			return &e, in
		}
	}
	return nil, nil
}

// Start runs a game in a container when the backend is on and an installed emulator plays its
// console. handled is false for everything else, which takes the process path unchanged. When the
// backend should have taken it but Docker can't be reached, or the emulator has no pinned image,
// handled is false too and o carries a notice: the game falls back to the process sandbox.
func (b *DockerBackend) Start(ctx context.Context, o *stream.Options) (s *DockerSession, handled bool, err error) {
	set := b.Settings()
	if !set.Enabled {
		return nil, false, nil
	}
	emu, inst := b.pick(o.Console)
	if emu == nil {
		return nil, false, nil
	}
	if set.Overrides[emu.ID] == docker.BackendProcess {
		return nil, false, nil
	}
	image := set.Images[emu.ID]
	if image == "" {
		image = emu.Image.Pinned()
	}
	if image == "" {
		o.Notices = append(o.Notices, fmt.Sprintf("%s has no container image pinned to a digest yet, so this game runs on a core instead.", emu.Name))
		return nil, false, nil
	}
	rt, st, err := b.runtime(ctx)
	if err != nil {
		b.log.Info("docker: not reachable; falling back to the process sandbox", "emulator", emu.ID, "err", st.Error)
		o.Notices = append(o.Notices, "Docker isn't reachable on this server, so this game runs on a core instead.")
		return nil, false, nil
	}
	backend, err := docker.Choose(set, true, emu.ID, emu.Stream)
	if err != nil {
		return nil, true, err
	}
	s, err = b.launch(ctx, rt, set, backend, emu, inst, image, *o)
	return s, true, err
}

func (b *DockerBackend) launch(ctx context.Context, rt docker.Runtime, set docker.Settings, backend docker.Backend, emu *emulators.Emulator,
	inst *emulators.Installed, image string, o stream.Options) (*DockerSession, error) {
	netInfo, err := rt.EnsureNetwork(ctx, docker.NetworkSpec{Name: set.Network, Internal: set.Internal})
	if err != nil {
		return nil, err
	}
	sid := docker.NewSecret()[:32]
	saves := filepath.Join(b.dataDir, "docker", "saves", strconv.FormatInt(o.UserID, 10), emu.ID)
	if err := os.MkdirAll(saves, 0o700); err != nil {
		return nil, err
	}
	rom := filepath.Clean(o.ROM)
	cmd := append([]string{docker.ContainerProgram + "/" + inst.Entry}, emu.Args...)
	cmd = append(cmd, docker.ContainerROM+"/"+filepath.Base(rom))
	req := docker.Request{
		SessionID: sid, UserID: o.UserID, Emulator: emu.ID, Image: image,
		ROM: rom, SaveDir: saves, ProgramDir: inst.Dir, AppImage: strings.HasSuffix(strings.ToLower(inst.Entry), ".appimage"),
		Cmd: cmd, Network: netInfo.Name, PublishLoopback: set.PublishLoopback, InputToken: docker.NewSecret(),
		Limits: set.Limits(), GPU: set.GPU,
	}
	if emu.NeedsBios && o.SystemDir != "" {
		req.BIOSDir = filepath.Clean(o.SystemDir)
	}
	ds := &DockerSession{ID: sid, UserID: o.UserID, Emulator: emu.ID, Backend: backend}
	fwd := atomic.Pointer[inputagent.Forwarder]{}
	lc := b.lifecycle(rt)

	if backend == docker.BackendDockerWebRTC {
		req.Stream = emulators.StreamPipe
		bind := netInfo.Gateway
		req.RTPHost = bind.String()
		if set.PublishLoopback || !bind.IsValid() {
			// Docker Desktop: the host isn't on the container's network.
			bind = netip.MustParseAddr("127.0.0.1")
			req.RTPHost = "host.docker.internal"
		}
		relay, err := b.mgr.StartRelay(stream.RelayOptions{UserID: o.UserID, ItemID: o.ItemID, Title: o.Title, Console: o.Console, Emulator: emu.ID,
			Width: 1280, Height: 720, FPS: 60, Bind: bind, Input: func(p []byte) {
				lc.Touch(sid)
				if f := fwd.Load(); f != nil {
					f.Send(p)
				}
			}})
		if err != nil {
			return nil, err
		}
		ds.relay = relay
		req.RTPVideo, req.RTPAudio = relay.VideoPort(), relay.AudioPort()
	} else {
		req.Stream = emulators.StreamKasmVNC
		req.KasmToken = docker.NewSecret()
	}
	spec, err := docker.Build(req)
	if err == nil && req.RTPHost == "host.docker.internal" {
		spec.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	var c *docker.Container
	if err == nil {
		c, err = lc.Start(ctx, spec)
	}
	if err != nil {
		if ds.relay != nil {
			ds.relay.Stop()
		}
		return nil, err
	}
	ds.c = c

	inputAddr := net.JoinHostPort(c.Info.IP.String(), strconv.Itoa(docker.InputPort))
	if set.PublishLoopback {
		inputAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Info.HostPorts[strconv.Itoa(docker.InputPort)+"/tcp"]))
	}
	f := &inputagent.Forwarder{Addr: inputAddr, Secret: req.InputToken}
	fwd.Store(f)
	ds.fwd = f
	if ds.relay != nil && !set.PublishLoopback && c.Info.IP.IsValid() {
		ds.relay.AllowFrom(c.Info.IP)
	}
	if backend == docker.BackendDockerKasmVNC {
		up := &url.URL{Scheme: "http", Host: net.JoinHostPort(c.Info.IP.String(), strconv.Itoa(docker.KasmPort))}
		if set.PublishLoopback {
			up = docker.LoopbackURL(c.Info.HostPorts[strconv.Itoa(docker.KasmPort)+"/tcp"])
		}
		ttl := docker.DefaultLifetime
		if set.MaxHours > 0 {
			ttl = time.Duration(set.MaxHours) * time.Hour
		}
		ticket, err := b.proxy.Register(sid, up, req.KasmToken, ttl)
		if err != nil {
			_ = lc.Stop(context.WithoutCancel(ctx), sid)
			return nil, err
		}
		ds.KasmURL = "/kasm/" + sid + "/?ticket=" + ticket
	}
	b.mu.Lock()
	b.sessions[sid] = ds
	b.mu.Unlock()
	go b.watch(ds, lc)
	return ds, nil
}

// watch tidies up after a session, however it ends: its container stopping, or its relay.
func (b *DockerBackend) watch(ds *DockerSession, lc *docker.Lifecycle) {
	var relayDone <-chan struct{}
	if ds.relay != nil {
		relayDone = ds.relay.Done()
	}
	select {
	case <-ds.c.Done():
	case <-relayDone:
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_ = lc.Stop(ctx, ds.ID)
		cancel()
	}
	if ds.relay != nil {
		ds.relay.Stop()
	}
	if ds.fwd != nil {
		ds.fwd.Close()
	}
	b.proxy.Forget(ds.ID)
	b.mu.Lock()
	delete(b.sessions, ds.ID)
	b.mu.Unlock()
}

// Stop ends a game in a container.
func (b *DockerBackend) Stop(ctx context.Context, id string) error {
	b.mu.Lock()
	lc := b.lc
	b.mu.Unlock()
	if lc == nil || b.Session(id) == nil {
		return docker.ErrNoContainer
	}
	return lc.Stop(ctx, id)
}

// ---------------------------------------------------------------- installs

// InstallJob is an install under way or finished.
type InstallJob struct {
	ID        string               `json:"id"`
	State     string               `json:"state"` // running | done | failed
	Got       int64                `json:"got"`
	Total     int64                `json:"total"` // -1: not known
	Error     string               `json:"error,omitempty"`
	Installed *emulators.Installed `json:"installed,omitempty"`
	Started   time.Time            `json:"started"`
}

func (b *DockerBackend) progress(id string, got, total int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[id]; j != nil {
		j.Got, j.Total = got, total
	}
}

// Install starts installing an emulator, and returns at once.
func (b *DockerBackend) Install(id string) (InstallJob, error) {
	if !b.Settings().Enabled {
		return InstallJob{}, errDockerOff
	}
	if b.inst == nil {
		return InstallJob{}, errors.New("this module has no folder of its own to install into")
	}
	e := emulators.Find(id)
	if e == nil {
		return InstallJob{}, errNoEmulator
	}
	b.mu.Lock()
	if j := b.jobs[id]; j != nil && j.State == "running" {
		b.mu.Unlock()
		return InstallJob{}, emulators.ErrBusy
	}
	j := &InstallJob{ID: id, State: "running", Total: -1, Started: time.Now()}
	b.jobs[id] = j
	out := *j
	b.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(b.runCtx, 45*time.Minute)
		defer cancel()
		got, err := b.inst.Install(ctx, e)
		b.mu.Lock()
		defer b.mu.Unlock()
		if err != nil {
			j.State, j.Error = "failed", err.Error()
			b.log.Warn("docker: installing an emulator", "emulator", id, "err", err)
			return
		}
		j.State, j.Installed = "done", got
	}()
	return out, nil
}

// Job says how an install is going.
func (b *DockerBackend) Job(id string) (InstallJob, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[id]
	if j == nil {
		return InstallJob{}, false
	}
	return *j, true
}

var errNoEmulator = errors.New("there's no such emulator in the list")

// ---------------------------------------------------------------- routes

// CatalogEntry is one emulator for the admin page: the list's entry and what's installed.
type CatalogEntry struct {
	emulators.Emulator
	Installed   *emulators.Installed `json:"installed,omitempty"`
	Installable bool                 `json:"installable"`
	Why         string               `json:"why,omitempty"` // why it can't be installed
}

// Routes adds the Docker backend's routes.
func (b *DockerBackend) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /docker/status", b.admin(b.serveStatus))
	mux.HandleFunc("GET /docker/settings", b.admin(b.serveSettings))
	mux.HandleFunc("PUT /docker/settings", b.admin(b.serveSetSettings))
	mux.HandleFunc("GET /docker/emulators", b.admin(b.serveCatalog))
	mux.HandleFunc("GET /docker/installed", b.admin(b.serveInstalled))
	mux.HandleFunc("POST /docker/emulators/{id}/install", b.admin(b.serveInstall))
	mux.HandleFunc("GET /docker/emulators/{id}/install", b.admin(b.serveJob))
	mux.HandleFunc("DELETE /docker/emulators/{id}", b.admin(b.serveUninstall))
	mux.HandleFunc("POST /docker/sessions/{sid}/connect", b.user(b.serveConnect))
	mux.HandleFunc("DELETE /docker/sessions/{sid}", b.user(b.serveStop))
	mux.Handle("/kasm/", b.user(b.serveKasm))
}

type handler func(w http.ResponseWriter, r *http.Request, user int64, admin bool)

// identity reads who the server says is asking.
func identity(r *http.Request) (user int64, admin, ok bool) {
	u, err := strconv.ParseInt(r.Header.Get(HeaderUser), 10, 64)
	if err != nil || u <= 0 {
		return 0, false, false
	}
	return u, r.Header.Get(HeaderAdmin) == "1", true
}

// user lets in anyone the server says is signed in.
func (b *DockerBackend) user(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, admin, ok := identity(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "signed-out", "sign in first")
			return
		}
		h(w, r, u, admin)
	}
}

// admin lets in only an administrator.
func (b *DockerBackend) admin(h handler) http.HandlerFunc {
	return b.user(func(w http.ResponseWriter, r *http.Request, u int64, admin bool) {
		if !admin {
			writeJSONError(w, http.StatusForbidden, "admin-only", "only an administrator can do that")
			return
		}
		h(w, r, u, admin)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, text string) {
	writeJSON(w, status, map[string]string{"error": text, "code": code})
}

func (b *DockerBackend) serveStatus(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	set := b.Settings()
	_, st, err := b.runtime(r.Context())
	out := struct {
		Enabled bool `json:"enabled"`
		docker.Status
		Containers int `json:"containers"`
	}{set.Enabled, st, b.Count()}
	if errors.Is(err, errDockerOff) {
		out.Status = docker.Status{Error: err.Error()}
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *DockerBackend) serveSettings(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	writeJSON(w, http.StatusOK, struct {
		Settings docker.Settings  `json:"settings"`
		Schema   []docker.Field   `json:"schema"`
		Backends []docker.Backend `json:"backends"`
	}{b.Settings(), docker.Schema(), docker.Backends})
}

func (b *DockerBackend) serveSetSettings(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	s := docker.DefaultSettings()
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-settings", "the settings are unreadable: "+err.Error())
		return
	}
	s, err := s.Clean()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-settings", err.Error())
		return
	}
	if b.dataDir != "" {
		if err := docker.SaveSettings(b.settingsPath(), s); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
	}
	b.mu.Lock()
	b.settings = s
	if b.lc != nil && b.lc.Count() == 0 {
		b.lc = nil // the next start makes one with the new limits
	}
	b.mu.Unlock()
	if s.Enabled {
		// Turning it on is when what an earlier run left behind can first be found.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if rt, _, err := b.runtime(ctx); err == nil {
				if _, err := b.lifecycle(rt).SweepOrphans(ctx); err != nil {
					b.log.Warn("docker: removing containers left from before", "err", err)
				}
			}
		}()
	}
	writeJSON(w, http.StatusOK, s)
}

func (b *DockerBackend) serveCatalog(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	m, err := emulators.Builtin()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	out := make([]CatalogEntry, 0, len(m.Emulators))
	for _, e := range m.Emulators {
		c := CatalogEntry{Emulator: e, Installable: true}
		if b.inst != nil {
			c.Installed = b.inst.Get(e.ID)
		}
		art := e.Here()
		switch {
		case e.NonCommercial:
			c.Installable, c.Why = false, emulators.ErrNonCommercial.Error()
		case art == nil:
			c.Installable, c.Why = false, emulators.ErrNoArtifact.Error()
		case art.Placeholder():
			c.Installable, c.Why = false, emulators.ErrPlaceholder.Error()
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *DockerBackend) serveInstalled(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	if b.inst == nil {
		writeJSON(w, http.StatusOK, []emulators.Installed{})
		return
	}
	list, err := b.inst.List()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (b *DockerBackend) serveInstall(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	j, err := b.Install(r.PathValue("id"))
	switch {
	case errors.Is(err, errDockerOff):
		writeJSONError(w, http.StatusConflict, "docker-off", err.Error())
	case errors.Is(err, errNoEmulator):
		writeJSONError(w, http.StatusNotFound, "no-emulator", err.Error())
	case errors.Is(err, emulators.ErrBusy):
		writeJSONError(w, http.StatusConflict, "busy", err.Error())
	case err != nil:
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
	default:
		writeJSON(w, http.StatusAccepted, j)
	}
}

func (b *DockerBackend) serveJob(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	j, ok := b.Job(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "no-job", "nothing is being installed by that name")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (b *DockerBackend) serveUninstall(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	id := r.PathValue("id")
	if b.inst == nil {
		writeJSONError(w, http.StatusNotFound, "not-installed", emulators.ErrNotInstalled.Error())
		return
	}
	b.mu.Lock()
	for _, s := range b.sessions {
		if s.Emulator == id {
			b.mu.Unlock()
			writeJSONError(w, http.StatusConflict, "in-use", "someone is playing with that emulator; stop the game first")
			return
		}
	}
	b.mu.Unlock()
	switch err := b.inst.Uninstall(id); {
	case errors.Is(err, emulators.ErrNotInstalled):
		writeJSONError(w, http.StatusNotFound, "not-installed", err.Error())
	case errors.Is(err, emulators.ErrBusy):
		writeJSONError(w, http.StatusConflict, "busy", err.Error())
	case err != nil:
		writeJSONError(w, http.StatusBadRequest, "failed", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// own finds a session that belongs to whoever is asking (or that an administrator may see).
func (b *DockerBackend) own(w http.ResponseWriter, sid string, user int64, admin bool) *DockerSession {
	s := b.Session(sid)
	if s == nil || (s.UserID != user && !admin) {
		writeJSONError(w, http.StatusNotFound, "no-session", "this game has ended")
		return nil
	}
	return s
}

func (b *DockerBackend) serveConnect(w http.ResponseWriter, r *http.Request, user int64, _ bool) {
	// Only the player connects, not an administrator watching someone else's game.
	s := b.own(w, r.PathValue("sid"), user, false)
	if s == nil {
		return
	}
	if s.relay == nil {
		writeJSONError(w, http.StatusConflict, "kasmvnc", "this game is shown through its desktop: "+s.KasmURL)
		return
	}
	var body struct {
		Hello stream.Hello `json:"hello"`
		Offer string       `json:"offer"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, stream.MaxOfferSize+64<<10)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-hello", "the connection request is unreadable")
		return
	}
	c, err := s.relay.Connect(r.Context(), body.Hello, body.Offer)
	if err != nil {
		var se *stream.StreamError
		if errors.As(err, &se) {
			writeJSONError(w, se.Status, se.Code, se.Text)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (b *DockerBackend) serveStop(w http.ResponseWriter, r *http.Request, user int64, admin bool) {
	s := b.own(w, r.PathValue("sid"), user, admin)
	if s == nil {
		return
	}
	if err := b.Stop(r.Context(), s.ID); err != nil && !errors.Is(err, docker.ErrNoContainer) {
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveKasm is the KasmVNC proxy: only for the person playing, and with that session's ticket
// (KasmProxy checks it). Every request counts as someone playing, for the idle limit.
func (b *DockerBackend) serveKasm(w http.ResponseWriter, r *http.Request, user int64, _ bool) {
	rest := strings.TrimPrefix(r.URL.Path, "/kasm/")
	sid, _, _ := strings.Cut(rest, "/")
	s := b.own(w, sid, user, false)
	if s == nil {
		return
	}
	if s.Backend != docker.BackendDockerKasmVNC {
		http.NotFound(w, r)
		return
	}
	b.mu.Lock()
	lc := b.lc
	b.mu.Unlock()
	if lc != nil {
		lc.Touch(sid)
	}
	r2 := r.Clone(r.Context())
	r2.Header.Del(HeaderUser)
	r2.Header.Del(HeaderAdmin)
	http.StripPrefix("/kasm", &b.proxy).ServeHTTP(w, r2)
}
