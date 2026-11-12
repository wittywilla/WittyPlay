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
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
	"github.com/wittywilla/wittyplay/sandbox/host"
	"github.com/wittywilla/wittyplay/sandbox/local"
	"github.com/wittywilla/wittyplay/stream"
)

// Headers the server sets on what it passes through.
const (
	HeaderUser  = "X-Witty-User"  // the signed-in account's id
	HeaderAdmin = "X-Witty-Admin" // "1" for an administrator
	// HeaderSystemDir is the server's BIOS folder, so a page can be told which firmware is
	// there. The server sets it on everything it passes through, over whatever a browser sent.
	HeaderSystemDir = "X-Wittyplay-System-Dir"
)

// DataEnv is where the server says this module's own folder is (the "data" permission).
const DataEnv = "WITTYMODULE_DATA"

// ConnectDocker reaches the Docker daemon. The module's command sets it to the engine; nothing
// else imports the Docker SDK. Left nil, Docker is never available.
var ConnectDocker func() (docker.Runtime, error)

// Docker is this process's backend for standalone emulators.
var Docker = NewDockerBackend(os.Getenv(DataEnv), func() (docker.Runtime, error) {
	if ConnectDocker == nil {
		return nil, docker.ErrUnavailable
	}
	return ConnectDocker()
}, Mgr, nil)

// unwatchedLimit is how long a streamed game may run with nobody connected to it: long enough
// to reload a page or walk to another room, not long enough to hold a place for nobody.
const unwatchedLimit = 2 * time.Minute

// DockerBackend holds the standalone-emulator backend's state for one module process. The name
// is from when Docker was its only sandbox; it runs the local one too.
type DockerBackend struct {
	dataDir string
	connect func() (docker.Runtime, error)
	mgr     *stream.Manager
	log     *slog.Logger
	proxy   docker.KasmProxy
	inst    *emulators.Installer
	store   *emulators.Store
	// Local is the sandbox without Docker. LocalMissing says what this machine lacks for it.
	// Both are set by NewDockerBackend; a test puts a fake in.
	Local        docker.Runtime
	LocalMissing func() []string
	// Host runs an emulator on this machine's own desktop (a Mac, a Windows PC). HostMissing
	// says what it lacks.
	Host        docker.Runtime
	HostMissing func() []string

	mu        sync.Mutex
	settings  docker.Settings
	rt        docker.Runtime
	rtKind    string
	lc        *docker.Lifecycle
	machine   docker.Status // the last answer from a runtime that was there
	systemDir string        // the server's BIOS folder, once it has said
	jobs      map[string]*InstallJob
	sessions  map[string]*DockerSession
	runCtx    context.Context
	stopRun   context.CancelFunc
	started   sync.Once
}

// NewDockerBackend makes a backend keeping its files under dataDir/docker and dataDir/emulators.
// With dataDir empty, settings live only in memory, and nothing can be installed.
func NewDockerBackend(dataDir string, connect func() (docker.Runtime, error), mgr *stream.Manager, log *slog.Logger) *DockerBackend {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	b := &DockerBackend{dataDir: dataDir, connect: connect, mgr: mgr, log: log, settings: docker.DefaultSettings(),
		jobs: map[string]*InstallJob{}, sessions: map[string]*DockerSession{}, store: &emulators.Store{}}
	b.runCtx, b.stopRun = context.WithCancel(context.Background())
	lr := &local.Runtime{Dir: filepath.Join(os.TempDir(), "wittyplay-local")}
	if dataDir != "" {
		lr.Dir = filepath.Join(dataDir, "docker", "local")
		b.store.Path = filepath.Join(dataDir, "docker", "emulator-settings.json")
		b.inst = &emulators.Installer{DataDir: dataDir, Client: safeClient, OnProgress: b.progress, Platform: b.platform}
		if s, err := docker.LoadSettings(b.settingsPath()); err != nil {
			log.Warn("docker: the saved settings are unreadable; standalone emulators stay off", "err", err)
		} else {
			b.settings = s
		}
	}
	b.Local, b.LocalMissing = lr, lr.Missing
	hr := &host.Runtime{Dir: filepath.Join(os.TempDir(), "wittyplay-host"), Log: log}
	if dataDir != "" {
		hr.Dir = filepath.Join(dataDir, "docker", "host")
	}
	if mgr != nil {
		hr.FFmpeg = mgr.FFmpegPath
	}
	b.Host, b.HostMissing = hr, hr.Missing
	if dataDir != "" {
		if own, err := emulators.LoadOwn(b.ownPath()); err != nil {
			log.Warn("docker: your own emulator programs couldn't be read", "err", err)
		} else {
			emulators.SetOwn(own)
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

// platform is where emulators run: this machine for an emulator on its desktop, or in the local
// sandbox on FreeBSD; otherwise Linux (a container is Linux, and so is Linux's local sandbox), on
// the processor the runtime last said it has. Before any runtime has answered it's this machine's
// processor, which is right everywhere but a server whose Docker runs in a virtual machine of
// another kind.
func (b *DockerBackend) platform() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settings.Runtime == docker.RuntimeHost || (b.settings.Runtime == docker.RuntimeLocal && runtime.GOOS == "freebsd") {
		return runtime.GOOS, runtime.GOARCH
	}
	if b.machine.Arch != "" {
		return "linux", b.machine.Arch
	}
	return "linux", runtime.GOARCH
}

// runtime returns what runs emulators, when the backend is on and it answers, or why not.
func (b *DockerBackend) runtime(ctx context.Context) (docker.Runtime, docker.Status, error) {
	b.mu.Lock()
	enabled, kind, rt := b.settings.Enabled, b.settings.Runtime, b.rt
	if b.rtKind != kind {
		rt = nil
	}
	b.mu.Unlock()
	if !enabled {
		return nil, docker.Status{Error: "standalone emulators are off"}, errDockerOff
	}
	if rt == nil {
		if kind == docker.RuntimeLocal {
			rt = b.Local
		} else if kind == docker.RuntimeHost {
			rt = b.Host
		} else {
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
			rt = r
		}
		b.mu.Lock()
		if b.rt == nil || b.rtKind != kind {
			b.rt, b.rtKind = rt, kind
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
	b.mu.Lock()
	b.machine = st
	b.mu.Unlock()
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

var errDockerOff = errors.New("standalone emulators are off; an administrator can turn them on in Settings, Standalone emulators")

// Startup removes containers a crash left behind, when the backend is on and its runtime is
// reachable. It never fails: with it off or missing it does nothing, and says why in the log.
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

// Count is how many emulators this module is running.
func (b *DockerBackend) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sessions)
}

// StopAll ends every emulator, as the module goes away.
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

// sender is how input reaches an emulator: over TCP to a container's agent, or through the
// local sandbox's socket.
type sender interface {
	Send([]byte)
	Close()
}

// DockerSession is a game in a standalone emulator.
type DockerSession struct {
	ID       string
	UserID   int64
	ItemID   int64
	Title    string
	Emulator string
	Name     string // the emulator's name
	Backend  docker.Backend
	KasmURL  string // for docker+kasmvnc: the proxy's address, relative to the module's routes
	Started  time.Time
	Width    int
	Height   int
	FPS      int
	Players  int
	Notices  []string

	relay *stream.Relay
	fwd   sender
	ioDir string
	c     *docker.Container
}

// sessionJSON is a session as a client sees it. Paths are relative to the module's routes; the
// server puts its own prefix before them.
type sessionJSON struct {
	ID       string         `json:"id"`
	Backend  docker.Backend `json:"backend"`
	Emulator string         `json:"emulator"`
	Name     string         `json:"name"`
	Connect  string         `json:"connect,omitempty"`
	KasmURL  string         `json:"kasmUrl,omitempty"`
	Stop     string         `json:"stop"`
	Pause    string         `json:"pause"`
	Resume   string         `json:"resume"`
	Width    int            `json:"width"`
	Height   int            `json:"height"`
	FPS      int            `json:"fps"`
	Players  int            `json:"players"`
	Notices  []string       `json:"notices"`
}

// MarshalJSON is what a server gets back from a start: the id, as for a game, and how to reach
// it.
func (s *DockerSession) MarshalJSON() ([]byte, error) {
	base := "/docker/sessions/" + s.ID
	notices := s.Notices
	if notices == nil {
		notices = []string{}
	}
	return json.Marshal(sessionJSON{ID: s.ID, Backend: s.Backend, Emulator: s.Emulator, Name: s.Name, Connect: s.connectPath(), KasmURL: s.KasmURL,
		Stop: base, Pause: base + "/pause", Resume: base + "/resume", Width: s.Width, Height: s.Height, FPS: s.FPS, Players: s.Players, Notices: notices})
}

func (s *DockerSession) connectPath() string {
	if s.relay != nil {
		return "/docker/sessions/" + s.ID + "/connect"
	}
	return ""
}

// Done is closed when its emulator has gone.
func (s *DockerSession) Done() <-chan struct{} { return s.c.Done() }

// Session finds a game in a standalone emulator.
func (b *DockerBackend) Session(id string) *DockerSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}

// Refusal is a start that can't happen, said in a sentence for whoever asked. It isn't a fault.
type Refusal struct {
	Code string `json:"code"`
	Text string `json:"error"`
	// Busy: there's no room right now, and there may be later.
	Busy bool `json:"busy,omitempty"`
}

func (r *Refusal) Error() string { return r.Text }

func refuse(code, format string, a ...any) *Refusal {
	return &Refusal{Code: code, Text: fmt.Sprintf(format, a...)}
}

// LaunchOptions start a game in a standalone emulator. Only the server sends them: the paths are
// ones it has checked this person may play.
type LaunchOptions struct {
	UserID, ItemID int64
	Title          string
	Console        string
	ROM            string
	SystemDir      string
	Emulator       string
}

// LaunchResult is the answer to docker.Launch: a session, or why not.
type LaunchResult struct {
	Session *DockerSession `json:"session,omitempty"`
	Refusal *Refusal       `json:"refusal,omitempty"`
}

// fits says why an emulator can't run on a machine, or "".
func fits(e *emulators.Emulator, set docker.Settings, m docker.Status) string {
	gb := func(mb int64) string {
		if mb%1024 == 0 {
			return strconv.FormatInt(mb/1024, 10) + " GB"
		}
		return strconv.FormatFloat(float64(mb)/1024, 'f', 1, 64) + " GB"
	}
	need := e.Requires.MemoryMB
	if need == 0 {
		need = 1024
	}
	limit := set.MemoryMB
	if limit <= 0 {
		limit = docker.DefaultMemoryMB
	}
	switch {
	case m.MemoryMB > 0 && need > m.MemoryMB:
		return fmt.Sprintf("%s needs about %s of memory, and this server has %s. It can't run here.", e.Name, gb(need), gb(m.MemoryMB))
	case need > limit:
		return fmt.Sprintf("%s needs about %s of memory, and each emulator is given %s. An administrator can raise that in Settings, Standalone emulators.", e.Name, gb(need), gb(limit))
	case m.CPUs > 0 && e.Requires.CPUs > float64(m.CPUs):
		return fmt.Sprintf("%s needs %v processor cores, and this server has %d. It can't run here.", e.Name, e.Requires.CPUs, m.CPUs)
	}
	return ""
}

// ready says why a game can't start in an emulator right now, or nil. It reaches the runtime.
func (b *DockerBackend) ready(ctx context.Context, e *emulators.Emulator, console string) (docker.Runtime, docker.Status, *emulators.Installed, string, *Refusal) {
	set := b.Settings()
	if !set.Enabled {
		return nil, docker.Status{}, nil, "", refuse("off", "%s", errDockerOff.Error())
	}
	if console != "" && !e.Plays(console) {
		return nil, docker.Status{}, nil, "", refuse("wrong-console", "%s doesn't play that console's games.", e.Name)
	}
	if set.Overrides[e.ID] == docker.BackendProcess {
		return nil, docker.Status{}, nil, "", refuse("turned-off", "An administrator has turned %s off.", e.Name)
	}
	if e.Own && set.Runtime == docker.RuntimeDocker {
		return nil, docker.Status{}, nil, "", refuse("own-in-container", "%s is a program on this server, which runs only on this computer, not in a container. An administrator can choose where emulators run in Settings, Standalone emulators.", e.Name)
	}
	var in *emulators.Installed
	if b.inst != nil {
		in = b.installed(e)
	}
	image := set.Images[e.ID]
	if image == "" {
		image = e.Image.Pinned()
	}
	local := set.Runtime == docker.RuntimeLocal || set.Runtime == docker.RuntimeHost
	// Not installed is fine only for an image that has the emulator in it.
	if in == nil && (local || image == "") {
		return nil, docker.Status{}, nil, "", refuse("not-installed", "%s isn't installed. An administrator can install it in Settings, Standalone emulators.", e.Name)
	}
	if in != nil && in.Unverified {
		return nil, docker.Status{}, nil, "", refuse("unverified", "%s was installed from a download nobody had checked, so it isn't run. Install it again.", e.Name)
	}
	if !local && image == "" {
		return nil, docker.Status{}, nil, "", refuse("no-image", "%s has no container image pinned yet. An administrator sets one in Settings, Standalone emulators (see docker/images in WittyPlay for how to build it).", e.Name)
	}
	rt, st, err := b.runtime(ctx)
	if err != nil {
		what := "Docker isn't reachable on this server"
		if local {
			what = "This server can't run emulators itself"
		}
		return nil, st, nil, "", refuse("unavailable", "%s: %s", what, st.Error)
	}
	if why := fits(e, set, st); why != "" {
		return nil, st, nil, "", refuse("too-small", "%s", why)
	}
	return rt, st, in, image, nil
}

// Launch starts a game in a standalone emulator, or says in a Refusal why it can't.
func (b *DockerBackend) Launch(ctx context.Context, o LaunchOptions) (*DockerSession, error) {
	e := emulators.Find(o.Emulator)
	if e == nil {
		return nil, refuse("no-emulator", "There's no emulator by that name.")
	}
	if o.UserID <= 0 || o.ROM == "" || !filepath.IsAbs(o.ROM) {
		return nil, errors.New("a start needs an account and a game")
	}
	b.noteSystemDir(o.SystemDir)
	// The same person opening the same game again gets the one that's running: a reloaded page
	// carries on rather than starting a second emulator and ending the first.
	b.mu.Lock()
	for _, s := range b.sessions {
		if s.UserID == o.UserID && s.ItemID == o.ItemID && s.Emulator == e.ID {
			b.mu.Unlock()
			if s.Backend == docker.BackendDockerKasmVNC {
				if p, ok := b.proxy.Path(s.ID); ok {
					s.KasmURL = p
				}
			}
			return s, nil
		}
	}
	b.mu.Unlock()
	rt, st, inst, image, no := b.ready(ctx, e, o.Console)
	if no != nil {
		return nil, no
	}
	set := b.Settings()
	if e.NeedsBios && len(b.biosStatus(e)) > 0 && !b.hasAnyBios(e) {
		return nil, refuse("needs-bios", "%s needs firmware of your own before it can start a game, and none was found in this server's BIOS folder. WittyPlay never downloads it.", e.Name)
	}
	backend, err := docker.Choose(set, true, e.ID, e.Stream)
	if err != nil {
		return nil, refuse("turned-off", "%s", err.Error())
	}
	s, err := b.launch(ctx, rt, st, set, backend, e, inst, image, o)
	if errors.Is(err, docker.ErrBusy) || errors.Is(err, stream.ErrBusy) {
		return nil, &Refusal{Code: "busy", Text: docker.ErrBusy.Error(), Busy: true}
	}
	return s, err
}

func (b *DockerBackend) launch(ctx context.Context, rt docker.Runtime, st docker.Status, set docker.Settings, backend docker.Backend, emu *emulators.Emulator,
	inst *emulators.Installed, image string, o LaunchOptions) (*DockerSession, error) {
	localRT := backend == docker.BackendLocalWebRTC
	hostRT := backend == docker.BackendHostWebRTC
	var art *emulators.Artifact
	if b.inst != nil {
		art = b.inst.Artifact(emu)
		if emu.Own {
			art = emu.Here()
		}
	}
	netInfo, err := rt.EnsureNetwork(ctx, docker.NetworkSpec{Name: set.Network, Internal: set.Internal})
	if err != nil {
		return nil, err
	}
	sid := docker.NewSecret()[:32]
	saves := filepath.Join(b.dataDir, "docker", "saves", strconv.FormatInt(o.UserID, 10), emu.ID)
	if b.dataDir == "" {
		saves = filepath.Join(os.TempDir(), "wittyplay-saves", strconv.FormatInt(o.UserID, 10), emu.ID)
	}
	if err := os.MkdirAll(saves, 0o700); err != nil {
		return nil, err
	}
	// This person's settings for this emulator and this game, written into the emulator's own
	// files before it starts.
	schema := b.personSchema(emu, o.UserID)
	admin, own, game := b.store.Layers(emu.ID, o.UserID, o.ItemID)
	eff := emulators.Effective(schema, admin, own, game)
	if hostRT {
		eff = hostPaths(eff, saves, o.SystemDir)
	}
	plan := emulators.Plan(schema, eff)
	settingsDir := filepath.Join(saves, filepath.FromSlash(art.SettingsDir()))
	if err := plan.WriteFiles(settingsDir); err != nil {
		return nil, fmt.Errorf("writing %s's settings: %w", emu.Name, err)
	}
	w, h := sizeOf(eff)
	fps, _ := strconv.Atoi(textOf(eff, "stream.fps"))
	kbps := int(numberOf(eff, "stream.kbps"))
	limits := set.Limits()
	if st.MemoryMB > 0 && (limits.MemoryMB == 0 && docker.DefaultMemoryMB > st.MemoryMB || limits.MemoryMB > st.MemoryMB) {
		limits.MemoryMB = st.MemoryMB * 3 / 4 // never promise more than the machine has
	}

	if err := seedFirmware(emu, o.SystemDir, saves); err != nil {
		return nil, fmt.Errorf("copying %s's firmware for this person: %w", emu.Name, err)
	}
	if err := writeInputMap(emu, settingsDir, eff, runtime.GOOS, hostRT); err != nil {
		b.log.Warn("docker: the controllers couldn't be set up in the emulator's own settings", "emulator", emu.ID, "err", err)
	}
	// A game kept as a folder (a PS3 disc, a Wii U game) is given whole: the emulator is
	// pointed at the folder, or at the file in it that starts it.
	rom, inGame := dumpRoot(filepath.Clean(o.ROM))
	var cmd []string
	if inst != nil {
		cmd = CommandLine(emu, art, inst.Entry, plan.Args, path.Join(filepath.Base(rom), inGame))
	}
	// With nothing installed the image has the emulator in it, and runs it on WITTYPLAY_ROM.
	req := docker.Request{
		SessionID: sid, UserID: o.UserID, Emulator: emu.ID, Image: image,
		ROM: rom, SaveDir: saves, Cmd: cmd, Env: plan.Env, Network: netInfo.Name, PublishLoopback: set.PublishLoopback, InputToken: docker.NewSecret(),
		Limits: limits, GPU: set.GPU, Width: w, Height: h, FPS: fps, Kbps: kbps, Encoder: textOf(eff, "stream.encoder"), Keys: keysJSON(eff),
	}
	if inst != nil {
		req.ProgramDir, req.AppImage = inst.Dir, strings.HasSuffix(strings.ToLower(inst.Entry), ".appimage")
		if hostRT && art != nil && art.Portable {
			// This build keeps everything beside itself: each person runs a copy of their own.
			dir, err := portableCopy(inst, saves)
			if err != nil {
				return nil, fmt.Errorf("making %s's own copy for this person: %w", emu.Name, err)
			}
			req.ProgramDir = dir
		}
	}
	if req.Encoder == "vaapi" && !set.GPU.DRI {
		req.Encoder = "x264"
	}
	if emu.NeedsBios && o.SystemDir != "" {
		req.BIOSDir = filepath.Clean(o.SystemDir)
	}
	players := emu.Players
	if players <= 0 {
		players = 1
	}
	ds := &DockerSession{ID: sid, UserID: o.UserID, ItemID: o.ItemID, Title: o.Title, Emulator: emu.ID, Name: emu.Name, Backend: backend, Started: time.Now(),
		Width: w, Height: h, FPS: fps, Players: players}
	if emu.Requires.GPU && !set.GPU.DRI && !set.GPU.Nvidia {
		ds.Notices = append(ds.Notices, emu.Name+" is drawing without a graphics card here, so it may be slow. An administrator can pass one through in Settings, Standalone emulators.")
	}
	fwd := atomic.Pointer[sender]{}
	lc := b.lifecycle(rt)
	fail := func(err error) (*DockerSession, error) {
		if ds.relay != nil {
			ds.relay.Stop()
		}
		if f := fwd.Load(); f != nil {
			(*f).Close()
		}
		if ds.ioDir != "" {
			_ = os.RemoveAll(ds.ioDir)
		}
		return nil, err
	}

	if backend != docker.BackendDockerKasmVNC {
		req.Stream = emulators.StreamPipe
		bind := netInfo.Gateway
		req.RTPHost = bind.String()
		if hostRT {
			// On this machine: the encoder beside the emulator sends to loopback.
			bind = netip.MustParseAddr("127.0.0.1")
			req.RTPHost = "127.0.0.1"
		} else if localRT || set.PublishLoopback || !bind.IsValid() {
			// Docker Desktop: the host isn't on the container's network. The local sandbox:
			// nothing arrives over the network at all.
			bind = netip.MustParseAddr("127.0.0.1")
			req.RTPHost = "host.docker.internal"
		}
		relay, err := b.mgr.StartRelay(stream.RelayOptions{UserID: o.UserID, ItemID: o.ItemID, Title: o.Title, Console: o.Console, Emulator: emu.ID,
			Width: w, Height: h, FPS: float64(fps), Bind: bind, Kbps: kbps,
			Input: func(p []byte) {
				lc.Touch(sid)
				if f := fwd.Load(); f != nil {
					(*f).Send(p)
				}
			},
			SetBitrate: func(k int) {
				if f := fwd.Load(); f != nil {
					(*f).Send(inputagent.BitrateMessage(k))
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
	if localRT {
		// One socket carries the picture, the sound and the input: the sandbox has no network.
		// Its folder is short and in the system's temporary folder, as a socket's path may only
		// be about a hundred characters long.
		dir, err := os.MkdirTemp("", "wp-io-")
		if err != nil {
			return fail(err)
		}
		ds.ioDir = dir
		host := &inputagent.Host{Secret: req.InputToken, OnPacket: func(kind byte, p []byte) { ds.relay.Push(kind == inputagent.KindVideo, p) }}
		if err := host.Listen(filepath.Join(dir, "host.sock")); err != nil {
			return fail(err)
		}
		var sd sender = host
		fwd.Store(&sd)
		req.Local, req.IODir, req.Image, req.PublishLoopback = true, dir, "", false
	}
	if hostRT {
		req.Host, req.Image, req.PublishLoopback, req.GPU = true, "", false, docker.GPU{}
		if req.Encoder == "vaapi" {
			req.Encoder = "x264"
		}
	}
	spec, err := docker.Build(req)
	if err != nil {
		return fail(err)
	}
	if !localRT && !hostRT && req.RTPHost == "host.docker.internal" {
		spec.ExtraHosts = []string{"host.docker.internal:host-gateway"}
	}
	c, err := lc.Start(ctx, spec)
	if err != nil {
		return fail(err)
	}
	ds.c = c

	if hostRT {
		hr, ok := rt.(*host.Runtime)
		if !ok {
			_ = lc.Stop(context.WithoutCancel(ctx), sid)
			return fail(errors.New("this machine's runtime isn't the one that takes input"))
		}
		var sd sender = &hostSender{rt: hr, id: c.Info.ID}
		fwd.Store(&sd)
	} else if !localRT {
		inputAddr := net.JoinHostPort(c.Info.IP.String(), strconv.Itoa(docker.InputPort))
		if set.PublishLoopback {
			inputAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Info.HostPorts[strconv.Itoa(docker.InputPort)+"/tcp"]))
		}
		var sd sender = &inputagent.Forwarder{Addr: inputAddr, Secret: req.InputToken}
		fwd.Store(&sd)
		if ds.relay != nil && !set.PublishLoopback && c.Info.IP.IsValid() {
			ds.relay.AllowFrom(c.Info.IP)
		}
	}
	if f := fwd.Load(); f != nil {
		ds.fwd = *f
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
			return fail(err)
		}
		ds.KasmURL = "/kasm/" + sid + "/" + ticket + "/"
	}
	b.mu.Lock()
	b.sessions[sid] = ds
	b.mu.Unlock()
	go b.watch(ds, lc)
	return ds, nil
}

// endings says, for each reason a container ends, what the player is told.
var endings = map[string][2]string{
	"stopped":   {stream.EndStopped, ""},
	"idle":      {stream.EndIdle, "This game was stopped after sitting unplayed."},
	"lifetime":  {stream.EndIdle, "This game reached the longest time one may run here."},
	"exited":    {stream.EndQuit, "The emulator closed."},
	"replaced":  {stream.EndEvicted, "You started another game, which ended this one."},
	"unwatched": {stream.EndIdle, ""},
}

// watch tidies up after a session, however it ends: its emulator stopping, its relay, or nobody
// having been connected to it for unwatchedLimit.
func (b *DockerBackend) watch(ds *DockerSession, lc *docker.Lifecycle) {
	var relayDone <-chan struct{}
	if ds.relay != nil {
		relayDone = ds.relay.Done()
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	stop := func(why string) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		_ = lc.StopBecause(ctx, ds.ID, why)
		cancel()
	}
wait:
	for {
		select {
		case <-ds.c.Done():
			break wait
		case <-relayDone:
			stop("stopped")
			break wait
		case <-t.C:
			if ds.relay != nil && !ds.c.Paused() && ds.relay.Unwatched() > unwatchedLimit {
				b.log.Info("docker: stopping a game nobody is connected to", "session", ds.ID)
				stop("unwatched")
				break wait
			}
		}
	}
	<-ds.c.Done()
	why := ds.c.Reason()
	if why == "exited" {
		b.log.Warn("docker: an emulator ended by itself", "session", ds.ID, "emulator", ds.Emulator, "said", ds.c.LastLog())
	}
	if ds.relay != nil {
		end := endings[why]
		if end[0] == "" {
			end[0] = stream.EndStopped
		}
		ds.relay.End(end[0], end[1])
	}
	if ds.fwd != nil {
		ds.fwd.Close()
	}
	if ds.ioDir != "" {
		_ = os.RemoveAll(ds.ioDir)
	}
	b.proxy.Forget(ds.ID)
	b.mu.Lock()
	delete(b.sessions, ds.ID)
	b.mu.Unlock()
}

// Stop ends a game in a standalone emulator.
func (b *DockerBackend) Stop(ctx context.Context, id string) error {
	b.mu.Lock()
	lc := b.lc
	b.mu.Unlock()
	if lc == nil || b.Session(id) == nil {
		return docker.ErrNoContainer
	}
	return lc.Stop(ctx, id)
}

// ---------------------------------------------------------------- an emulator's settings

// The settings every emulator has, which WittyPlay itself uses: the stream and the keys.
func commonSettings() []emulators.Setting {
	buttons := make([]emulators.Choice, len(inputagent.Buttons))
	for i, n := range inputagent.Buttons {
		buttons[i] = emulators.Choice{Value: n, Label: buttonLabels[n]}
	}
	out := []emulators.Setting{
		{Key: "stream.size", Kind: emulators.KindChoice, Label: "Picture size", Default: "1280x720", Scope: emulators.ScopeAdmin,
			Choices: []emulators.Choice{{Value: "854x480", Label: "480p (lightest)"}, {Value: "1280x720", Label: "720p"}, {Value: "1920x1080", Label: "1080p (needs a fast processor or a graphics card)"}},
			Help:    "The size of the emulator's screen and of the stream. Larger costs this server more for every player."},
		{Key: "stream.fps", Kind: emulators.KindChoice, Label: "Frames a second", Default: "60", Scope: emulators.ScopeAdmin,
			Choices: []emulators.Choice{{Value: "30", Label: "30"}, {Value: "60", Label: "60"}}},
		{Key: "stream.encoder", Kind: emulators.KindChoice, Label: "Encode the picture with", Default: "x264", Scope: emulators.ScopeAdmin,
			Choices: encoderChoices()},
		{Key: "stream.kbps", Kind: emulators.KindNumber, Label: "Most bitrate (kbit/s)", Default: float64(6000), Min: 800, Max: 20000, Step: 100, Scope: emulators.ScopeUser,
			Help: "The stream starts here and drops by itself when the connection loses too much, then climbs back."},
	}
	for p := 1; p <= inputagent.MaxPlayers; p++ {
		out = append(out, emulators.Setting{Key: "keys.p" + strconv.Itoa(p), Kind: emulators.KindMap, Label: "Keys for player " + strconv.Itoa(p), Default: map[string]any{},
			MapOf: emulators.KindText, MapKeys: buttons, Scope: emulators.ScopeAdmin,
			Help: "The key each controller button presses in the emulator (an X key name such as z, Return, KP_1). Leave one empty for WittyPlay's own. WittyPlay writes the same keys into the emulator's own controller settings before each game, for as many players as it takes."})
	}
	return out
}

var buttonLabels = map[string]string{"b": "B (bottom)", "y": "Y (left)", "select": "Select", "start": "Start", "up": "D-pad up", "down": "D-pad down", "left": "D-pad left",
	"right": "D-pad right", "a": "A (right)", "x": "X (top)", "l": "L", "r": "R", "l2": "L2", "r2": "R2", "l3": "L3", "r3": "R3",
	"lup": "Left stick up", "ldown": "Left stick down", "lleft": "Left stick left", "lright": "Left stick right",
	"rup": "Right stick up", "rdown": "Right stick down", "rleft": "Right stick left", "rright": "Right stick right"}

// schemaFor is every setting of one emulator: the common ones, then the list's own for it, then
// every other option its own configuration declares (emulators/schemas). An own program that's a
// build of a listed emulator has that one's.
func schemaFor(e *emulators.Emulator) []emulators.Setting {
	return append(commonSettings(), emulators.FullSchema(e.Settings, e.Base())...)
}

func textOf(v emulators.Values, key string) string {
	s, _ := v[key].(string)
	return s
}

func numberOf(v emulators.Values, key string) float64 {
	f, _ := v[key].(float64)
	return f
}

func sizeOf(v emulators.Values) (int, int) {
	ws, hs, _ := strings.Cut(textOf(v, "stream.size"), "x")
	w, _ := strconv.Atoi(ws)
	h, _ := strconv.Atoi(hs)
	if w <= 0 || h <= 0 {
		return 1280, 720
	}
	return w, h
}

// keysJSON is the key map the agent is started with, or "" for its own.
func keysJSON(v emulators.Values) string {
	out := make([]map[string]string, inputagent.MaxPlayers)
	some := false
	for p := range out {
		out[p] = map[string]string{}
		m, _ := v["keys.p"+strconv.Itoa(p+1)].(map[string]any)
		for k, val := range m {
			if s, ok := val.(string); ok && s != "" {
				out[p][k] = s
				some = true
			}
		}
	}
	if !some {
		return ""
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ---------------------------------------------------------------- firmware

func (b *DockerBackend) noteSystemDir(dir string) {
	if dir == "" || !filepath.IsAbs(dir) {
		return
	}
	b.mu.Lock()
	b.systemDir = filepath.Clean(dir)
	b.mu.Unlock()
}

// BiosStatus is one firmware file an emulator looks for, and whether it's in the BIOS folder.
type BiosStatus struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Note    string `json:"note,omitempty"`
}

// biosStatus says which of an emulator's firmware files are in the server's BIOS folder. It's
// empty until the server has said where that folder is. Names are matched whatever their case,
// and only in the folder itself: nothing is opened.
func (b *DockerBackend) biosStatus(e *emulators.Emulator) []BiosStatus {
	b.mu.Lock()
	dir := b.systemDir
	b.mu.Unlock()
	if dir == "" || len(e.Bios) == 0 {
		return nil
	}
	var names []string
	if list, err := os.ReadDir(dir); err == nil {
		for i, d := range list {
			if i >= 10000 {
				break
			}
			if !d.IsDir() {
				names = append(names, strings.ToLower(d.Name()))
			}
		}
	}
	out := make([]BiosStatus, 0, len(e.Bios))
	for _, f := range e.Bios {
		st := BiosStatus{Name: f.Name, Note: f.Note}
		for _, n := range names {
			if ok, _ := path.Match(strings.ToLower(f.Name), n); ok {
				st.Present = true
				break
			}
		}
		out = append(out, st)
	}
	return out
}

func (b *DockerBackend) hasAnyBios(e *emulators.Emulator) bool {
	for _, s := range b.biosStatus(e) {
		if s.Present {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- installs

// InstallJob is an install under way or finished.
type InstallJob struct {
	ID        string               `json:"id"`
	State     string               `json:"state"` // running | done | failed | cancelled
	Phase     string               `json:"phase"`
	Got       int64                `json:"got"`
	Total     int64                `json:"total"` // -1: not known
	Resumed   int64                `json:"resumed,omitempty"`
	Error     string               `json:"error,omitempty"`
	Installed *emulators.Installed `json:"installed,omitempty"`
	Started   time.Time            `json:"started"`

	cancel context.CancelFunc
}

func (b *DockerBackend) progress(id string, p emulators.Progress) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if j := b.jobs[id]; j != nil && j.State == "running" {
		j.Phase, j.Got, j.Total, j.Resumed = p.Phase, p.Got, p.Total, p.Resumed
	}
}

// Install starts installing an emulator, and returns at once. The work carries on in the
// background, whoever asked having gone or not, and Job says how it's going.
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
	ctx, cancel := context.WithTimeout(b.runCtx, 45*time.Minute)
	j := &InstallJob{ID: id, State: "running", Phase: emulators.PhaseDownloading, Total: -1, Started: time.Now(), cancel: cancel}
	b.jobs[id] = j
	out := *j
	b.mu.Unlock()
	go func() {
		defer cancel()
		got, err := b.inst.Install(ctx, e)
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case err != nil && errors.Is(ctx.Err(), context.Canceled):
			j.State, j.Error = "cancelled", ""
		case err != nil:
			j.State, j.Error = "failed", err.Error()
			b.log.Warn("docker: installing an emulator", "emulator", id, "err", err)
		default:
			j.State, j.Phase, j.Installed = "done", emulators.PhaseDone, got
		}
	}()
	return out, nil
}

// CancelInstall stops an install that's running. What it had downloaded stays, and the next
// install carries on from there.
func (b *DockerBackend) CancelInstall(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.jobs[id]
	if j == nil || j.State != "running" {
		return false
	}
	j.cancel()
	return true
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

// CatalogEntry is one emulator for the admin page: the list's entry and how it stands here.
type CatalogEntry struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Systems       []string           `json:"systems"`
	License       string             `json:"license"`
	LicenseURL    string             `json:"licenseUrl,omitempty"`
	Upstream      string             `json:"upstream"`
	Version       string             `json:"version"`
	NonCommercial bool               `json:"nonCommercial,omitempty"`
	Experimental  bool               `json:"experimental,omitempty"`
	NeedsBios     bool               `json:"needsBios,omitempty"`
	Stream        string             `json:"stream"`
	Requires      emulators.Requires `json:"requires,omitzero"`
	DownloadBytes int64              `json:"downloadBytes,omitempty"`

	Installed       *InstalledJSON `json:"installed,omitempty"`
	UpdateAvailable bool           `json:"updateAvailable"`
	Installable     bool           `json:"installable"`
	Why             string         `json:"why,omitempty"` // why it can't be installed
	Runnable        bool           `json:"runnable"`
	WhyNot          string         `json:"whyNot,omitempty"` // why it can't run here yet
	Job             *InstallJob    `json:"job,omitempty"`
	Bios            []BiosStatus   `json:"bios,omitempty"`
}

// InstalledJSON is an installed emulator without where it is on disk, which no page needs.
type InstalledJSON struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Source      string `json:"source"`
	InstalledAt int64  `json:"installedAt"`
}

// Routes adds the backend's routes.
func (b *DockerBackend) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /docker/status", b.admin(b.serveStatus))
	mux.HandleFunc("GET /docker/diagnostics", b.admin(b.serveDiagnostics))
	mux.HandleFunc("GET /docker/settings", b.admin(b.serveSettings))
	mux.HandleFunc("PUT /docker/settings", b.admin(b.serveSetSettings))
	mux.HandleFunc("GET /docker/emulators", b.admin(b.serveCatalog))
	mux.HandleFunc("GET /docker/installed", b.admin(b.serveInstalled))
	mux.HandleFunc("POST /docker/emulators/{id}/install", b.admin(b.serveInstall))
	mux.HandleFunc("GET /docker/emulators/{id}/install", b.admin(b.serveJob))
	mux.HandleFunc("DELETE /docker/emulators/{id}/install", b.admin(b.serveCancelInstall))
	mux.HandleFunc("DELETE /docker/emulators/{id}", b.admin(b.serveUninstall))
	mux.HandleFunc("GET /docker/emulators/{id}/settings", b.user(b.serveEmuSettings))
	mux.HandleFunc("PUT /docker/emulators/{id}/settings", b.user(b.serveSetEmuSettings))
	mux.HandleFunc("GET /docker/sessions", b.user(b.serveSessions))
	mux.HandleFunc("POST /docker/sessions/{sid}/connect", b.user(b.serveConnect))
	mux.HandleFunc("POST /docker/sessions/{sid}/pause", b.user(b.servePause(true)))
	mux.HandleFunc("POST /docker/sessions/{sid}/resume", b.user(b.servePause(false)))
	mux.HandleFunc("DELETE /docker/sessions/{sid}", b.user(b.serveStop))
	mux.HandleFunc("GET /docker/own", b.admin(b.serveOwn))
	mux.HandleFunc("PUT /docker/own", b.admin(b.serveSetOwn))
	mux.HandleFunc("/kasm/", b.serveKasm)
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
		b.noteSystemDir(r.Header.Get(HeaderSystemDir))
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

// localStatus is how the sandbox without Docker stands on this machine.
type localStatus struct {
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
}

func (b *DockerBackend) localStatus() localStatus {
	if m := b.LocalMissing(); len(m) > 0 {
		return localStatus{Why: strings.Join(m, "; ")}
	}
	return localStatus{Available: true}
}

// hostStatus says whether this machine can run an emulator on its own desktop (a Mac, Windows).
func (b *DockerBackend) hostStatus() localStatus {
	if b.HostMissing == nil {
		return localStatus{Why: "not on this system"}
	}
	if m := b.HostMissing(); len(m) > 0 {
		return localStatus{Why: strings.Join(m, "; ")}
	}
	return localStatus{Available: true}
}

func (b *DockerBackend) serveStatus(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	set := b.Settings()
	_, st, err := b.runtime(r.Context())
	out := struct {
		Enabled bool   `json:"enabled"`
		Runtime string `json:"runtime"`
		docker.Status
		Local      localStatus `json:"local"`
		Desktop    localStatus `json:"desktop"`
		Containers int         `json:"containers"`
	}{set.Enabled, set.Runtime, st, b.localStatus(), b.hostStatus(), b.Count()}
	if errors.Is(err, errDockerOff) {
		out.Status = docker.Status{Error: err.Error()}
	}
	writeJSON(w, http.StatusOK, out)
}

// Check is one line of the diagnostics: something looked at, and how it stands.
type Check struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"` // ok | warn | fail | off
	Detail string `json:"detail"`
}

// Diagnostics looks over everything a game in a standalone emulator depends on, and says in
// plain words what it found. It changes nothing.
func (b *DockerBackend) Diagnostics(ctx context.Context) (docker.Status, []Check) {
	set := b.Settings()
	var out []Check
	add := func(id, label, state, detail string) { out = append(out, Check{id, label, state, detail}) }
	if !set.Enabled {
		add("enabled", "Standalone emulators", "off", "Turned off. Nothing below is used until an administrator turns them on.")
	} else {
		add("enabled", "Standalone emulators", "ok", "Turned on.")
	}
	var st docker.Status
	if set.Runtime == docker.RuntimeLocal {
		st = b.Local.Available(ctx)
		if st.Available {
			add("runtime", "This machine's own sandbox", "ok", "bubblewrap, systemd-run and everything it starts are here.")
		} else {
			add("runtime", "This machine's own sandbox", "fail", st.Error)
		}
		add("isolation", "How strong the box is", "warn", "No network, no capabilities and its own view of processes and files, but no system-call filter, and the emulator runs on this machine's own libraries. A container is the stronger box.")
	} else {
		var rt docker.Runtime
		var err error
		if b.connect != nil {
			rt, err = b.connect()
		}
		if rt == nil || err != nil {
			msg := docker.ErrUnavailable.Error()
			if err != nil {
				msg = err.Error()
			}
			add("runtime", "Docker", "fail", msg)
		} else {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			st = rt.Available(cctx)
			cancel()
			if st.Available {
				add("runtime", "Docker", "ok", "Answering at "+st.Host+", API "+st.APIVersion+", "+st.OS+"/"+st.Arch+".")
				if st.Rootless {
					add("rootless", "Who the daemon runs as", "ok", "A rootless daemon: reaching it isn't root on this machine.")
				} else {
					add("rootless", "Who the daemon runs as", "warn", "The daemon runs as root, so whatever can reach its socket, this server included, can do anything on this machine. A rootless Docker or Podman is the safer way.")
				}
			} else {
				add("runtime", "Docker", "fail", "Not answering at "+st.Host+": "+st.Error)
			}
		}
		if l := b.localStatus(); l.Available {
			add("local", "This machine's own sandbox", "ok", "Available, if you'd rather not use Docker.")
		} else {
			add("local", "This machine's own sandbox", "off", l.Why)
		}
	}
	if st.Available {
		limit := set.MemoryMB
		if limit <= 0 {
			limit = docker.DefaultMemoryMB
		}
		n := set.MaxContainers
		if n <= 0 {
			n = docker.DefaultMaxContainers
		}
		detail := fmt.Sprintf("%d processor cores and %d MB of memory; up to %d emulators at once, %d MB each.", st.CPUs, st.MemoryMB, n, limit)
		switch {
		case st.MemoryMB > 0 && st.MemoryMB < 2048:
			add("machine", "This machine", "fail", detail+" That's too little memory for any emulator in the list. Cores are the way to play here.")
		case st.MemoryMB > 0 && int64(n)*limit > st.MemoryMB:
			add("machine", "This machine", "warn", detail+" Together that's more than the machine has, so two demanding games at once may run out.")
		default:
			add("machine", "This machine", "ok", detail)
		}
	}
	gpu := "Off. Emulators draw with the processor, which is enough for older consoles and slow for newer ones."
	state := "off"
	if set.GPU.DRI || set.GPU.Nvidia {
		state, gpu = "ok", "Passed through."
		if set.GPU.DRI && set.Runtime == docker.RuntimeLocal {
			if _, err := os.Stat("/dev/dri"); err != nil {
				state, gpu = "fail", "/dev/dri isn't there on this machine."
			}
		}
	}
	add("gpu", "Graphics card", state, gpu)
	if b.inst == nil {
		add("storage", "Where emulators are kept", "fail", "The server gave this module no folder of its own, so nothing can be installed.")
	} else if err := os.MkdirAll(b.inst.Root(), 0o755); err != nil {
		add("storage", "Where emulators are kept", "fail", err.Error())
	} else {
		list, _ := b.inst.List()
		add("storage", "Where emulators are kept", "ok", fmt.Sprintf("%s, with %d installed.", b.inst.Root(), len(list)))
		if set.Runtime != docker.RuntimeLocal {
			var missing []string
			for _, in := range list {
				if e := emulators.Find(in.ID); e != nil && set.Images[in.ID] == "" && e.Image.Pinned() == "" {
					missing = append(missing, e.Name)
				}
			}
			if len(missing) > 0 {
				add("images", "Container images", "warn", "No image is pinned for "+strings.Join(missing, ", ")+". Build one from docker/images in WittyPlay and set it under “Your own image for each”.")
			} else if len(list) > 0 {
				add("images", "Container images", "ok", "Every installed emulator has an image pinned.")
			}
		}
	}
	b.mu.Lock()
	dir := b.systemDir
	b.mu.Unlock()
	if dir != "" {
		add("bios", "BIOS folder", "ok", dir+", given to an emulator read-only. WittyPlay never downloads firmware.")
	}
	return st, out
}

func (b *DockerBackend) serveDiagnostics(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	st, checks := b.Diagnostics(r.Context())
	host := struct {
		OS       string `json:"os"`
		Arch     string `json:"arch"`
		CPUs     int    `json:"cpus"`
		MemoryMB int64  `json:"memoryMB"`
	}{runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), st.MemoryMB}
	if st.Available {
		host.OS, host.Arch, host.CPUs = st.OS, st.Arch, st.CPUs
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": host, "checks": checks})
}

func emulatorChoices() []docker.Choice {
	m, err := emulators.Builtin()
	if err != nil {
		return nil
	}
	out := make([]docker.Choice, len(m.Emulators))
	for i, e := range m.Emulators {
		out[i] = docker.Choice{Value: e.ID, Label: e.Name}
	}
	return out
}

func (b *DockerBackend) serveSettings(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	writeJSON(w, http.StatusOK, struct {
		Settings docker.Settings  `json:"settings"`
		Schema   []docker.Field   `json:"schema"`
		Backends []docker.Backend `json:"backends"`
	}{b.Settings(), docker.Schema(emulatorChoices()), docker.Backends})
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
	if old := b.Settings(); (old.Runtime != s.Runtime || !s.Enabled) && b.Count() > 0 {
		writeJSONError(w, http.StatusConflict, "in-use", "someone is playing; stop their game before turning emulators off or changing where they run")
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

// Catalog is the list as it stands on this machine.
func (b *DockerBackend) Catalog() ([]CatalogEntry, error) {
	m, err := emulators.Builtin()
	if err != nil {
		return nil, err
	}
	set := b.Settings()
	b.mu.Lock()
	machine := b.machine
	b.mu.Unlock()
	goos, goarch := b.platform()
	out := make([]CatalogEntry, 0, len(m.Emulators))
	for _, e := range m.Emulators {
		c := CatalogEntry{ID: e.ID, Name: e.Name, Systems: e.Systems, License: e.License, LicenseURL: e.LicenseURL, Upstream: e.Upstream, Version: e.Version,
			NonCommercial: e.NonCommercial, Experimental: e.Experimental, NeedsBios: e.NeedsBios, Stream: e.Stream, Requires: e.Requires,
			Installable: true, Bios: b.biosStatus(&e)}
		var in *emulators.Installed
		if b.inst != nil {
			in = b.installed(&e)
		}
		if in != nil {
			c.Installed = &InstalledJSON{in.ID, in.Version, in.SHA256, in.Source, in.InstalledAt}
			c.UpdateAvailable = in.Version != e.Version
		}
		art := e.For(goos, goarch)
		switch {
		case b.inst == nil:
			c.Installable, c.Why = false, "the server gave this module no folder of its own to install into"
		case e.NonCommercial:
			c.Installable, c.Why = false, emulators.ErrNonCommercial.Error()
		case art == nil:
			c.Installable, c.Why = false, fmt.Sprintf("%s (%s/%s)", emulators.ErrNoArtifact.Error(), goos, goarch)
		case art.Placeholder():
			c.Installable, c.Why = false, emulators.ErrPlaceholder.Error()
		}
		if art != nil {
			c.DownloadBytes = art.Size
		}
		if j, ok := b.Job(e.ID); ok {
			c.Job = &j
		}
		image := set.Images[e.ID]
		if image == "" {
			image = e.Image.Pinned()
		}
		switch {
		case in == nil && (image == "" || set.Runtime == docker.RuntimeLocal):
			c.WhyNot = "" // not installed: nothing to say yet
		case in != nil && in.Unverified:
			c.WhyNot = "It was installed from a download nobody had checked, so it isn't run. Install it again."
		case set.Runtime != docker.RuntimeLocal && image == "":
			c.WhyNot = "No container image is pinned for it yet. Build one from docker/images in WittyPlay and set it under “Your own image for each”."
		case set.Overrides[e.ID] == docker.BackendProcess:
			c.WhyNot = "It's set to “Not offered”."
		default:
			if why := fits(&e, set, machine); why != "" {
				c.WhyNot = why
			} else {
				c.Runnable = true
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func (b *DockerBackend) serveCatalog(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	// Ask the runtime what machine it is, so the list says what fits it. Not being there is no
	// reason to hide the list.
	_, _, _ = b.runtime(r.Context())
	out, err := b.Catalog()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *DockerBackend) serveInstalled(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	out := []InstalledJSON{}
	if b.inst != nil {
		list, err := b.inst.List()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		for _, in := range list {
			out = append(out, InstalledJSON{in.ID, in.Version, in.SHA256, in.Source, in.InstalledAt})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (b *DockerBackend) serveInstall(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	id := r.PathValue("id")
	e := emulators.Find(id)
	if e == nil {
		writeJSONError(w, http.StatusNotFound, "no-emulator", errNoEmulator.Error())
		return
	}
	// An emulator is someone else's program under its own licence. The page shows that licence
	// and sends its name back; an install that doesn't name it wasn't shown it.
	var body struct {
		License string `json:"license"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&body)
	if body.License != e.License {
		writeJSONError(w, http.StatusBadRequest, "license", fmt.Sprintf("%s is under its own licence, %s. Confirm you've seen it to install.", e.Name, e.License))
		return
	}
	// Know which machine the download is for before choosing it.
	_, _, _ = b.runtime(r.Context())
	j, err := b.Install(id)
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

func (b *DockerBackend) serveCancelInstall(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	if !b.CancelInstall(r.PathValue("id")) {
		writeJSONError(w, http.StatusNotFound, "no-job", "nothing is being installed by that name")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// emuSettings is one emulator's settings as one person sees them.
type emuSettings struct {
	Emulator  string              `json:"emulator"`
	Name      string              `json:"name"`
	Schema    []emulators.Setting `json:"schema"`
	Admin     emulators.Values    `json:"admin"`
	User      emulators.Values    `json:"user"`
	Game      emulators.Values    `json:"game"`
	Effective emulators.Values    `json:"effective"`
	CanAdmin  bool                `json:"canAdmin"`
}

func (b *DockerBackend) emuSettings(e *emulators.Emulator, user, game int64, admin bool) emuSettings {
	schema := emulators.Offered(b.personSchema(e, user))
	a, u, g := b.store.Layers(e.ID, user, game)
	return emuSettings{Emulator: e.ID, Name: e.Name, Schema: schema, Admin: a, User: u, Game: g, Effective: emulators.Effective(schema, a, u, g), CanAdmin: admin}
}

func (b *DockerBackend) serveEmuSettings(w http.ResponseWriter, r *http.Request, user int64, admin bool) {
	e := emulators.Find(r.PathValue("id"))
	if e == nil {
		writeJSONError(w, http.StatusNotFound, "no-emulator", errNoEmulator.Error())
		return
	}
	game, _ := strconv.ParseInt(r.URL.Query().Get("game"), 10, 64)
	if game < 0 {
		game = 0
	}
	writeJSON(w, http.StatusOK, b.emuSettings(e, user, game, admin))
}

func (b *DockerBackend) serveSetEmuSettings(w http.ResponseWriter, r *http.Request, user int64, admin bool) {
	e := emulators.Find(r.PathValue("id"))
	if e == nil {
		writeJSONError(w, http.StatusNotFound, "no-emulator", errNoEmulator.Error())
		return
	}
	var body struct {
		Scope  string           `json:"scope"`
		Game   int64            `json:"game"`
		Values emulators.Values `json:"values"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-setting", "the settings are unreadable")
		return
	}
	switch body.Scope {
	case emulators.ScopeAdmin:
		if !admin {
			writeJSONError(w, http.StatusForbidden, "admin-only", "only an administrator can change settings for everyone")
			return
		}
	case emulators.ScopeUser:
		body.Game = 0
	case emulators.ScopeGame:
		if body.Game <= 0 {
			writeJSONError(w, http.StatusBadRequest, "bad-setting", "which game?")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "bad-setting", "scope is admin, user or game")
		return
	}
	schema := b.personSchema(e, user)
	vals, err := emulators.CleanValues(schema, body.Scope, body.Values)
	var se *emulators.ScopeError
	if errors.As(err, &se) {
		writeJSONError(w, http.StatusForbidden, "admin-only", err.Error())
		return
	}
	if err == nil {
		// The keys reach a command line inside the sandbox: they must be names of keys.
		probe := emulators.Values{}
		for k, v := range vals {
			probe[k] = v
		}
		if _, kerr := inputagent.ParseKeys(keysJSON(probe)); kerr != nil {
			err = kerr
		}
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-setting", err.Error())
		return
	}
	if err := b.store.Set(e.ID, body.Scope, user, body.Game, vals); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-setting", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b.emuSettings(e, user, body.Game, admin))
}

// Offer is an emulator that could play a game, for the game's page.
type Offer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Stream       string `json:"stream"`
	Experimental bool   `json:"experimental,omitempty"`
	NeedsBios    bool   `json:"needsBios,omitempty"`
	Ready        bool   `json:"ready"`
	Why          string `json:"why,omitempty"`
}

// Offers lists the emulators that are set up to play a console's games here: installed, or
// with an image of their own pinned. With the backend off it's empty, and a page shows nothing.
func (b *DockerBackend) Offers(ctx context.Context, console, systemDir string) []Offer {
	out := []Offer{}
	set := b.Settings()
	if !set.Enabled || console == "" {
		return out
	}
	b.noteSystemDir(systemDir)
	for _, e := range emulators.ForSystem(console) {
		if set.Overrides[e.ID] == docker.BackendProcess {
			continue
		}
		installed := b.inst != nil && b.installed(&e) != nil
		if !installed && (set.Runtime == docker.RuntimeLocal || set.Runtime == docker.RuntimeHost || (set.Images[e.ID] == "" && e.Image.Pinned() == "")) {
			continue // nobody has set it up: it isn't offered at all
		}
		o := Offer{ID: e.ID, Name: e.Name, Version: e.Version, Stream: e.Stream, Experimental: e.Experimental, NeedsBios: e.NeedsBios}
		if _, _, _, _, no := b.ready(ctx, &e, console); no != nil {
			o.Why = no.Text
		} else if e.NeedsBios && len(b.biosStatus(&e)) > 0 && !b.hasAnyBios(&e) {
			o.Why = e.Name + " needs firmware of your own, and none was found in this server's BIOS folder."
		} else {
			o.Ready = true
		}
		out = append(out, o)
	}
	return out
}

// ConsoleOffers is Offers for every console an emulator in the list plays, those with none left
// out. With the backend off it's empty.
func (b *DockerBackend) ConsoleOffers(ctx context.Context, systemDir string) map[string][]Offer {
	out := map[string][]Offer{}
	m, err := emulators.Builtin()
	if err != nil || !b.Settings().Enabled {
		return out
	}
	seen := map[string]bool{}
	for _, e := range m.Emulators {
		for _, c := range e.Systems {
			if seen[c] {
				continue
			}
			seen[c] = true
			if list := b.Offers(ctx, c, systemDir); len(list) > 0 {
				out[c] = list
			}
		}
	}
	return out
}

func (b *DockerBackend) serveSessions(w http.ResponseWriter, _ *http.Request, user int64, admin bool) {
	type row struct {
		ID       string         `json:"id"`
		Emulator string         `json:"emulator"`
		Name     string         `json:"name"`
		Backend  docker.Backend `json:"backend"`
		UserID   int64          `json:"userId"`
		Mine     bool           `json:"mine"`
		ItemID   int64          `json:"itemId"`
		Title    string         `json:"title"`
		Started  time.Time      `json:"started"`
		State    string         `json:"state"`
		Viewers  int            `json:"viewers"`
	}
	out := []row{}
	b.mu.Lock()
	for _, s := range b.sessions {
		if s.UserID != user && !admin {
			continue
		}
		r := row{ID: s.ID, Emulator: s.Emulator, Name: s.Name, Backend: s.Backend, UserID: s.UserID, Mine: s.UserID == user, ItemID: s.ItemID, Title: s.Title,
			Started: s.Started, State: "running"}
		if s.c.Paused() {
			r.State = "paused"
		}
		if s.relay != nil {
			r.Viewers = s.relay.Viewers()
		}
		out = append(out, r)
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	writeJSON(w, http.StatusOK, out)
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
		writeJSONError(w, http.StatusConflict, "kasmvnc", "this game is shown through its desktop")
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

func (b *DockerBackend) servePause(pause bool) handler {
	return func(w http.ResponseWriter, r *http.Request, user int64, _ bool) {
		s := b.own(w, r.PathValue("sid"), user, false)
		if s == nil {
			return
		}
		b.mu.Lock()
		lc := b.lc
		b.mu.Unlock()
		if lc == nil {
			writeJSONError(w, http.StatusNotFound, "no-session", "this game has ended")
			return
		}
		var err error
		if pause {
			err = lc.Pause(r.Context(), s.ID)
		} else {
			err = lc.Resume(r.Context(), s.ID)
		}
		if errors.Is(err, docker.ErrNoContainer) {
			writeJSONError(w, http.StatusNotFound, "no-session", "this game has ended")
			return
		}
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
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

// serveKasm is the KasmVNC proxy. Who is asking isn't looked at: the page it serves runs in a
// sandboxed frame that carries nobody's sign-in, so the session's ticket in the address is the
// way in, and KasmProxy checks it. Every request counts as someone playing, for the idle limit.
func (b *DockerBackend) serveKasm(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/kasm/")
	sid, _, _ := strings.Cut(rest, "/")
	if s := b.Session(sid); s != nil && s.Backend == docker.BackendDockerKasmVNC {
		b.mu.Lock()
		lc := b.lc
		b.mu.Unlock()
		if lc != nil {
			lc.Touch(sid)
		}
	}
	r2 := r.Clone(r.Context())
	r2.Header.Del(HeaderUser)
	r2.Header.Del(HeaderAdmin)
	r2.Header.Del(HeaderSystemDir)
	http.StripPrefix("/kasm", &b.proxy).ServeHTTP(w, r2)
}
