// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package host runs a standalone emulator on this machine's own desktop, boxed in by the
// system's own sandbox, and streams it the way a container's is: its window and its sound are
// captured, FFmpeg encodes them to H.264 and Opus and sends RTP to WittyPlay's relay on
// loopback, and the player's input becomes key presses sent to that one program.
//
// Each system does it its own way (platform_*.go):
//
//   - macOS: the emulator runs under sandbox-exec (no network, writing only to its own settings
//     and saves), ScreenCaptureKit captures its window and its sound, and CGEventPostToPid sends
//     it keys. The server needs Screen Recording and Accessibility permission, which only the
//     Mac's owner can give, in System Settings, Privacy & Security.
//   - Windows: a job object holds it to memory and process limits and ends it with the module,
//     and it runs at low integrity, so it can write only to its own folder. Desktop Duplication
//     (FFmpeg's ddagrab) captures the screen where its window is, WASAPI's loopback the sound,
//     and SendInput sends it keys while its window is in front. It needs a signed-in desktop: a
//     service has none.
//   - Linux has bubblewrap and a virtual X display of its own (sandbox/local), which is better
//     than this, so this package isn't used there. FreeBSD uses that package too, without
//     bubblewrap.
//
// It's a docker.Runtime, so the lifecycle, limits and session code are the ones containers use.
// A Spec for it is built with docker.Request.Host set: the paths a container would see (/game,
// /saves, /bios, /opt/emulator) are turned back into this machine's own here.
package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// Runtime runs emulators on this machine.
type Runtime struct {
	// Dir is a folder of the module's own, for logs.
	Dir string
	// FFmpeg says where the encoder is (the one the server gave the module); found on PATH if it
	// says nothing.
	FFmpeg func() string
	Log    *slog.Logger
	// Look finds a program; exec.LookPath if nil. For tests.
	Look func(string) (string, error)

	mu        sync.Mutex
	seq       int
	procs     map[string]*proc
	permsSeen string // what the availability check last found, so it's logged once a change
}

// proc is one emulator and everything started beside it.
type proc struct {
	spec    docker.Spec
	plan    plan
	info    docker.Info
	exited  chan struct{}
	logPath string
	cancel  context.CancelFunc
	game    *game // set once started
}

// Permissions macOS asks for, by the names the log and the server use.
const (
	PermScreenRecording = "screen-recording"
	PermAccessibility   = "accessibility"
)

// Permission is one of them, and whether the server has it.
type Permission struct {
	Name    string `json:"name"`
	Granted bool   `json:"granted"`
}

// Permissions says which of the system's permissions the server has, where the system asks for
// any (a Mac). Elsewhere it's empty.
func (r *Runtime) Permissions() []Permission { return platformPermissions() }

// permissionFix is what someone does about a missing one, in plain words.
func permissionFix(name string) string {
	pane := "Screen Recording"
	if name == PermAccessibility {
		pane = "Accessibility"
	}
	return "Open System Settings → Privacy & Security → " + pane + ", turn on WittyMediaServer, then restart the server."
}

// logPermissions warns about each permission missing. The availability check says so when what
// it finds changes, and so once each time the module starts; a refused game says so each time.
func logPermissions(log *slog.Logger, perms []Permission) {
	for _, p := range perms {
		if !p.Granted {
			log.Warn("macOS permission missing", "permission", p.Name, "fix", permissionFix(p.Name))
		}
	}
}

func (r *Runtime) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (r *Runtime) look(name string) (string, error) {
	if r.Look != nil {
		return r.Look(name)
	}
	return exec.LookPath(name)
}

func (r *Runtime) ffmpeg() (string, error) {
	if r.FFmpeg != nil {
		if p := r.FFmpeg(); p != "" {
			return p, nil
		}
	}
	return r.look("ffmpeg")
}

// Missing says, in words a person can act on, what this machine lacks to run emulators here.
func (r *Runtime) Missing() []string {
	var out []string
	if why := unsupported(); why != "" {
		return []string{why}
	}
	if _, err := r.ffmpeg(); err != nil {
		out = append(out, "FFmpeg isn't installed where the module can find it")
	}
	return append(out, platformMissing()...)
}

// Available implements docker.Runtime.
func (r *Runtime) Available(context.Context) docker.Status {
	st := docker.Status{Host: "this machine (" + sandboxName() + ")", OS: runtime.GOOS, Arch: runtime.GOARCH, Rootless: true,
		CPUs: runtime.NumCPU(), MemoryMB: memoryMB()}
	m := r.Missing()
	if perms := r.Permissions(); len(perms) > 0 {
		key := fmt.Sprint(perms)
		r.mu.Lock()
		changed := key != r.permsSeen
		r.permsSeen = key
		r.mu.Unlock()
		if changed {
			logPermissions(r.log(), perms)
		}
	}
	if len(m) > 0 {
		st.Error = strings.Join(m, "; ")
		return st
	}
	st.Available = true
	return st
}

// Pull has nothing to do: there are no images here.
func (r *Runtime) Pull(context.Context, string) error { return nil }

// EnsureNetwork has nothing to make: the system's sandbox is the emulator's network.
func (r *Runtime) EnsureNetwork(context.Context, docker.NetworkSpec) (docker.NetworkInfo, error) {
	return docker.NetworkInfo{ID: docker.HostNetwork, Name: docker.HostNetwork}, nil
}

// plan is a Spec turned into this machine's paths and numbers.
type plan struct {
	Program string
	Args    []string
	Env     []string // only what the emulator itself was given by its settings
	SaveDir string
	ROM     string
	BIOSDir string
	AppDir  string // the installed emulator's folder

	Width, Height, FPS, Kbps int
	RTPVideo, RTPAudio       int
	Encoder                  string
	Keys                     string
	MemoryMB                 int64
	Pids                     int64
	CPUs                     float64
}

// settingsEnv are the variables Build sets for a container's entrypoint, which say how to
// stream rather than anything for the emulator. The rest came from the emulator's settings.
func settingsEnv(k string) bool {
	return strings.HasPrefix(k, "WITTYPLAY_") || slices.Contains([]string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "DISPLAY",
		"PULSE_SERVER", "APPIMAGE_EXTRACT_AND_RUN", "TMPDIR"}, k)
}

// planFor checks a Spec and turns it into this machine's terms.
func planFor(s docker.Spec) (plan, error) {
	if err := docker.Check(s); err != nil {
		return plan{}, err
	}
	if !s.Host {
		return plan{}, errors.New("this spec isn't for an emulator on this machine")
	}
	var p plan
	// Container paths to this machine's, longest first so /game/x beats /game.
	type pair struct{ from, to string }
	var paths []pair
	for _, m := range s.Mounts {
		switch {
		case m.Target == docker.ContainerSaves:
			p.SaveDir = m.Source
		case m.Target == docker.ContainerBIOS:
			p.BIOSDir = m.Source
		case m.Target == docker.ContainerProgram:
			p.AppDir = m.Source
		case strings.HasPrefix(m.Target, docker.ContainerROM+"/"):
			p.ROM = m.Source
		}
		paths = append(paths, pair{m.Target, m.Source})
	}
	if p.SaveDir == "" || p.ROM == "" {
		return plan{}, errors.New("a game needs its file and a save folder")
	}
	slices.SortFunc(paths, func(a, b pair) int { return len(b.from) - len(a.from) })
	local := func(arg string) string {
		for _, pr := range paths {
			if arg == pr.from {
				return pr.to
			}
			if rest, ok := strings.CutPrefix(arg, pr.from+"/"); ok {
				return filepath.Join(pr.to, filepath.FromSlash(rest))
			}
			// An argument such as --config=/saves/config.
			if i := strings.Index(arg, "="+pr.from); i >= 0 && (len(arg) == i+1+len(pr.from) || arg[i+1+len(pr.from)] == '/') {
				return arg[:i+1] + filepath.Join(pr.to, filepath.FromSlash(strings.TrimPrefix(arg[i+1+len(pr.from):], "/")))
			}
		}
		return arg
	}
	if len(s.Cmd) == 0 {
		return plan{}, errors.New("an emulator on this machine needs its program: there's no image to bring one")
	}
	p.Program = local(s.Cmd[0])
	if p.AppDir == "" || !within(p.AppDir, p.Program) {
		return plan{}, errors.New("the program isn't inside the installed emulator's folder")
	}
	for _, a := range s.Cmd[1:] {
		p.Args = append(p.Args, local(a))
	}
	num := func(v string) int { n, _ := strconv.Atoi(v); return n }
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "WITTYPLAY_WIDTH":
			p.Width = num(v)
		case "WITTYPLAY_HEIGHT":
			p.Height = num(v)
		case "WITTYPLAY_FPS":
			p.FPS = num(v)
		case "WITTYPLAY_KBPS":
			p.Kbps = num(v)
		case "WITTYPLAY_RTP_VIDEO":
			p.RTPVideo = num(v)
		case "WITTYPLAY_RTP_AUDIO":
			p.RTPAudio = num(v)
		case "WITTYPLAY_RTP_HOST":
			if v != "127.0.0.1" {
				return plan{}, errors.New("an emulator on this machine streams to loopback only")
			}
		case "WITTYPLAY_ENCODER":
			p.Encoder = v
		case "WITTYPLAY_KEYS":
			p.Keys = v
		}
		if !settingsEnv(k) {
			p.Env = append(p.Env, k+"="+local(v))
		}
	}
	if p.RTPVideo <= 0 || p.RTPAudio <= 0 || p.Width <= 0 || p.Height <= 0 || p.FPS <= 0 || p.Kbps <= 0 {
		return plan{}, errors.New("the stream's size, rate or ports are missing")
	}
	p.MemoryMB, p.Pids, p.CPUs = s.MemoryBytes>>20, s.PidsLimit, float64(s.NanoCPUs)/1e9
	return p, nil
}

// within says path is inside dir.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && filepath.IsLocal(rel)
}

// Create implements docker.Runtime.
func (r *Runtime) Create(_ context.Context, s docker.Spec) (string, error) {
	p, err := planFor(s)
	if err != nil {
		return "", fmt.Errorf("refusing to start an emulator here: %w", err)
	}
	if m := r.Missing(); len(m) > 0 {
		return "", fmt.Errorf("%w: %s", docker.ErrUnavailable, strings.Join(m, "; "))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.procs == nil {
		r.procs = map[string]*proc{}
	}
	r.seq++
	id := fmt.Sprintf("host-%d-%d", os.Getpid(), r.seq)
	r.procs[id] = &proc{spec: s, plan: p, info: docker.Info{ID: id, Name: s.Name, State: "created", Labels: s.Labels, Created: time.Now()}}
	return id, nil
}

func (r *Runtime) get(id string) (*proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.procs[id]
	if p == nil {
		return nil, errors.New("no such emulator")
	}
	return p, nil
}

// Start implements docker.Runtime: the emulator, then once its window is up, the capture and
// the encoder. It returns once the emulator has started; the rest follows in the background,
// and if it fails the emulator is stopped and its log says why.
func (r *Runtime) Start(_ context.Context, id string) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	ff, err := r.ffmpeg()
	if err != nil {
		return err
	}
	p.logPath = filepath.Join(r.Dir, p.spec.Name+".log")
	logf, err := os.OpenFile(p.logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	log := r.log().With("session", p.spec.Labels[docker.LabelSession], "emulator", p.spec.Labels[docker.LabelEmu])
	g, err := startGame(ctx, p.plan, ff, logf, log)
	if err != nil {
		cancel()
		logf.Close()
		log.Warn("the emulator wouldn't start on this machine", "path", p.plan.Program, "err", err)
		return err
	}
	log.Info("emulator started on this machine", "path", p.plan.Program, "pid", g.emu.Process.Pid, "log", p.logPath)
	started := time.Now()
	exited := make(chan struct{})
	r.mu.Lock()
	p.game, p.exited, p.cancel = g, exited, cancel
	p.info.Running, p.info.State = true, "running"
	r.mu.Unlock()
	go func() {
		g.wait()
		cancel()
		logf.Close()
		g.mu.Lock()
		asked := g.asked
		g.mu.Unlock()
		ran := time.Since(started).Round(time.Second).String()
		if asked {
			log.Info("the emulator exited", "ran", ran, "code", g.emu.ProcessState.ExitCode())
		} else {
			// Nobody asked it to go. What it said last is logged with the session's end, from Logs;
			// the per-game file keeps all of it.
			log.Warn("the emulator ended by itself", "ran", ran, "code", g.emu.ProcessState.ExitCode(), "log", p.logPath)
		}
		r.mu.Lock()
		p.info.Running, p.info.Paused, p.info.State = false, false, "exited"
		r.mu.Unlock()
		close(exited)
	}()
	return nil
}

// Stop implements docker.Runtime.
func (r *Runtime) Stop(ctx context.Context, id string, timeout time.Duration) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.game == nil {
		return nil
	}
	p.game.logger.Info("stopping the emulator")
	p.game.stop(false)
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.exited:
		return nil
	case <-t.C:
	case <-ctx.Done():
	}
	p.game.logger.Warn("the emulator didn't stop in time; ending it", "waited", timeout.String())
	p.game.stop(true)
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		p.game.logger.Error("the emulator didn't end, even when killed")
		return errors.New("the emulator didn't end")
	}
	return nil
}

// Remove implements docker.Runtime.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.game != nil {
		select {
		case <-p.exited:
		default:
			if err := r.Stop(ctx, id, 2*time.Second); err != nil {
				return err
			}
		}
	}
	r.mu.Lock()
	delete(r.procs, id)
	r.mu.Unlock()
	return nil
}

// Pause implements docker.Runtime: the emulator stops where it is, and the encoder keeps
// sending its last picture.
func (r *Runtime) Pause(_ context.Context, id string) error { return r.setPaused(id, true) }

// Unpause implements docker.Runtime.
func (r *Runtime) Unpause(_ context.Context, id string) error { return r.setPaused(id, false) }

func (r *Runtime) setPaused(id string, paused bool) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.game == nil {
		return errors.New("it isn't running")
	}
	if err := p.game.suspend(paused); err != nil {
		return err
	}
	r.mu.Lock()
	p.info.Paused = paused
	if paused {
		p.info.State = "paused"
	} else {
		p.info.State = "running"
	}
	r.mu.Unlock()
	return nil
}

// Logs implements docker.Runtime: the end of the emulator's and the encoder's output.
func (r *Runtime) Logs(_ context.Context, id string, tail int) (string, error) {
	p, err := r.get(id)
	if err != nil {
		return "", err
	}
	if p.logPath == "" {
		return "", nil
	}
	return tailFile(p.logPath, tail)
}

func tailFile(path string, lines int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	const most = 64 << 10
	if st, err := f.Stat(); err == nil && st.Size() > most {
		_, _ = f.Seek(st.Size()-most, io.SeekStart)
	}
	b, err := io.ReadAll(io.LimitReader(f, most))
	if err != nil {
		return "", err
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if lines > 0 && len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n"), nil
}

// Inspect implements docker.Runtime.
func (r *Runtime) Inspect(_ context.Context, id string) (docker.Info, error) {
	p, err := r.get(id)
	if err != nil {
		return docker.Info{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return p.info, nil
}

// List implements docker.Runtime. Only this module's own emulators are known: one left by a
// module that stopped went with it (a job object, a process group).
func (r *Runtime) List(_ context.Context, labels map[string]string) ([]docker.Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docker.Info
next:
	for _, p := range r.procs {
		for k, v := range labels {
			if p.info.Labels[k] != v {
				continue next
			}
		}
		out = append(out, p.info)
	}
	return out, nil
}

// Input passes one message from the player to the emulator: keys, the pointer, or a request
// for another bitrate. It's how the session's input reaches an emulator here.
func (r *Runtime) Input(id string, msg []byte) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.game == nil {
		return errors.New("it isn't running")
	}
	return p.game.input(msg)
}

// ReleaseInput lets go of every key held, when the player goes.
func (r *Runtime) ReleaseInput(id string) {
	if p, err := r.get(id); err == nil && p.game != nil {
		p.game.release()
	}
}

// ---------------------------------------------------------------- one game

// game is a running emulator with its capture and encoder.
type game struct {
	plan   plan
	ff     string
	log    io.Writer
	logger *slog.Logger
	emu    *exec.Cmd
	box    *box // the system's sandbox around it
	done   chan struct{}

	mu      sync.Mutex
	agent   *inputagent.Agent
	enc     *encoder
	kbps    int
	stopped bool
	asked   bool // something here asked it to stop: not a crash
}

// startGame starts the emulator and, in the background, what streams it.
func startGame(ctx context.Context, pl plan, ff string, logw io.Writer, logger *slog.Logger) (*game, error) {
	for _, d := range []string{pl.SaveDir, filepath.Join(pl.SaveDir, "home"), filepath.Join(pl.SaveDir, "tmp"), filepath.Join(pl.SaveDir, "config")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	keys, err := inputagent.ParseKeys(pl.Keys)
	if err != nil {
		fmt.Fprintln(logw, "wittyplay: the key map is unreadable, so the default one is used:", err)
	}
	cmd := exec.Command(pl.Program, pl.Args...)
	cmd.Dir = filepath.Join(pl.SaveDir, "home")
	cmd.Env = append(baseEnv(pl), pl.Env...)
	cmd.Stdout, cmd.Stderr = logw, logw
	b, err := sandbox(cmd, pl)
	if err != nil {
		return nil, fmt.Errorf("boxing the emulator in: %w", err)
	}
	if err := cmd.Start(); err != nil {
		b.close()
		return nil, err
	}
	if err := b.started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		b.close()
		return nil, fmt.Errorf("boxing the emulator in: %w", err)
	}
	g := &game{plan: pl, ff: ff, log: logw, logger: logger, emu: cmd, box: b, done: make(chan struct{}), kbps: pl.Kbps}
	g.agent = &inputagent.Agent{Keys: &keys, Bitrate: g.setBitrate}
	go func() {
		_ = cmd.Wait()
		g.mu.Lock()
		g.stopped = true
		enc := g.enc
		g.mu.Unlock()
		if enc != nil {
			enc.close()
		}
		b.close()
		close(g.done)
	}()
	go g.stream(ctx)
	return g, nil
}

// baseEnv is all the emulator's environment, besides its settings: a home and a temporary
// folder inside its own save folder, so what it keeps lands where it may write.
func baseEnv(pl plan) []string {
	home := filepath.Join(pl.SaveDir, "home")
	env := []string{"HOME=" + home, "TMPDIR=" + filepath.Join(pl.SaveDir, "tmp"),
		"XDG_CONFIG_HOME=" + filepath.Join(pl.SaveDir, "config"), "XDG_DATA_HOME=" + filepath.Join(pl.SaveDir, "data")}
	return append(env, systemEnv(pl)...)
}

// windowWait is how long an emulator has to show its window.
const windowWait = 45 * time.Second

// stream waits for the emulator's window, then captures and encodes it until the game ends.
// The encoder starts again at a new bitrate when asked, and after anything else stops it.
func (g *game) stream(ctx context.Context) {
	wctx, cancel := context.WithTimeout(ctx, windowWait)
	win, err := findWindow(wctx, g.emu.Process.Pid)
	cancel()
	if err != nil {
		fmt.Fprintln(g.log, "wittyplay: the emulator's window never appeared:", err)
		g.logger.Warn("the emulator's window never appeared; ending it", "waited", windowWait.String(), "err", err)
		g.stop(true)
		return
	}
	g.logger.Info("found the emulator's window")
	g.mu.Lock()
	g.agent.Sink = newSink(g.emu.Process.Pid, win, g.plan.Width, g.plan.Height)
	g.mu.Unlock()
	for ctx.Err() == nil {
		g.mu.Lock()
		if g.stopped {
			g.mu.Unlock()
			return
		}
		kbps := g.kbps
		g.mu.Unlock()
		enc, err := startEncoder(ctx, g.ff, g.plan, kbps, win, g.log)
		if err != nil {
			fmt.Fprintln(g.log, "wittyplay: the encoder wouldn't start:", err)
			if errors.Is(err, errNoPermission) {
				// Each game refused says so, as nothing else will tell its owner.
				perms := platformPermissions()
				if len(perms) == 0 {
					perms = []Permission{{Name: PermScreenRecording}}
				}
				logPermissions(g.logger, perms)
				g.logger.Warn("the system refused to let the server capture the emulator; ending it", "err", err)
				g.stop(true)
				return
			}
			g.logger.Warn("the encoder wouldn't start", "kbps", kbps, "encoder", g.plan.Encoder, "err", err)
		} else {
			g.logger.Info("encoder started", "kbps", kbps, "encoder", g.plan.Encoder)
			g.mu.Lock()
			g.enc = enc
			g.mu.Unlock()
			enc.wait()
		}
		select {
		case <-ctx.Done():
			return
		case <-g.done:
			return
		case <-time.After(time.Second):
		}
	}
}

func (g *game) setBitrate(kbps int) {
	kbps = max(800, min(kbps, 50000))
	g.mu.Lock()
	if kbps == g.kbps {
		g.mu.Unlock()
		return
	}
	old := g.kbps
	g.kbps = kbps
	enc := g.enc
	g.mu.Unlock()
	g.logger.Info("the stream's bitrate changed", "kbps", kbps, "was", old)
	if enc != nil {
		enc.close() // the loop in stream starts it again at the new rate, with a keyframe
	}
}

func (g *game) input(msg []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.agent.Sink == nil {
		// The window isn't up yet: only a bitrate request means anything.
		if len(msg) == inputagent.ControlSize && msg[0] == inputagent.ControlMark {
			kbps := int(msg[1]) | int(msg[2])<<8
			g.kbps = max(800, min(kbps, 50000))
		}
		return nil
	}
	a := g.agent
	g.mu.Unlock()
	err := a.Handle(msg)
	g.mu.Lock()
	return err
}

func (g *game) release() {
	g.mu.Lock()
	a := g.agent
	g.mu.Unlock()
	if a.Sink != nil {
		a.Release()
	}
}

func (g *game) suspend(paused bool) error { return suspend(g.emu, g.box, paused) }

// stop ends the emulator: politely, or now.
func (g *game) stop(now bool) {
	g.mu.Lock()
	g.stopped, g.asked = true, true
	enc := g.enc
	g.mu.Unlock()
	if enc != nil {
		enc.close()
	}
	_ = suspend(g.emu, g.box, false)
	if now {
		kill(g.emu, g.box)
	} else {
		terminate(g.emu, g.box)
	}
}

func (g *game) wait() { <-g.done }

// ---------------------------------------------------------------- the encoder

// errNoPermission is a capture the system refused: nothing will change that until its owner
// gives permission, so the game ends with the reason rather than trying again every second.
var errNoPermission = errors.New("the system refused to let this program capture the screen")

// encoder is FFmpeg, with what feeds it.
type encoder struct {
	cmd  *exec.Cmd
	feed capture
	once sync.Once
	done chan struct{}
}

func (e *encoder) close() {
	e.once.Do(func() {
		if e.feed != nil {
			e.feed.close()
		}
		if e.cmd.Process != nil {
			_ = e.cmd.Process.Kill()
		}
	})
}

func (e *encoder) wait() { <-e.done }

// capture is what a system gives FFmpeg: arguments for its inputs (video first, then audio),
// and, for inputs that are pipes, what writes into them.
type capture interface {
	// inputs are FFmpeg's input arguments. Pipes are pipe:0 (video) and pipe:3 (audio).
	inputs() []string
	// run writes into the pipes until the context ends or capture fails. video or audio is nil
	// when that input isn't a pipe.
	run(ctx context.Context, video, audio io.WriteCloser) error
	close()
}

func startEncoder(ctx context.Context, ff string, pl plan, kbps int, win window, logw io.Writer) (*encoder, error) {
	feed, err := newCapture(win, pl.Width, pl.Height, pl.FPS)
	if err != nil {
		return nil, err
	}
	args := EncoderArgs(feed.inputs(), pl.Encoder, pl.FPS, kbps, pl.RTPVideo, pl.RTPAudio)
	cmd := exec.CommandContext(ctx, ff, args...)
	cmd.Stdout, cmd.Stderr = logw, logw
	hideConsole(cmd)
	var video, audio io.WriteCloser
	var vr, ar *os.File
	in := strings.Join(feed.inputs(), " ")
	if strings.Contains(in, "pipe:0") {
		r, w, err := os.Pipe()
		if err != nil {
			feed.close()
			return nil, err
		}
		cmd.Stdin, video, vr = r, w, r
	}
	if strings.Contains(in, "pipe:3") {
		r, w, err := os.Pipe()
		if err != nil {
			feed.close()
			return nil, err
		}
		cmd.ExtraFiles, audio, ar = []*os.File{r}, w, r
	}
	if err := cmd.Start(); err != nil {
		feed.close()
		for _, f := range []io.Closer{video, audio, vr, ar} {
			if f != nil {
				f.Close()
			}
		}
		return nil, err
	}
	for _, f := range []*os.File{vr, ar} {
		if f != nil {
			f.Close() // FFmpeg has its own copy
		}
	}
	e := &encoder{cmd: cmd, feed: feed, done: make(chan struct{})}
	cctx, cancel := context.WithCancel(ctx)
	go func() {
		if err := feed.run(cctx, video, audio); err != nil && cctx.Err() == nil {
			fmt.Fprintln(logw, "wittyplay: capture stopped:", err)
		}
		if video != nil {
			video.Close()
		}
		if audio != nil {
			audio.Close()
		}
	}()
	go func() {
		_ = cmd.Wait()
		cancel()
		e.close()
		close(e.done)
	}()
	return e, nil
}

// EncoderArgs is FFmpeg's command line for a game on this machine: the same encoding a
// container's entrypoint uses (docker/images/base/entrypoint.sh), from whatever inputs this
// system's capture gives. What keeps the delay down: no input buffering, no B-frames, no
// lookahead, a keyframe a second, a rate buffer a quarter of a second deep, 10 ms Opus frames.
func EncoderArgs(inputs []string, encoder string, fps, kbps, rtpVideo, rtpAudio int) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin", "-fflags", "nobuffer", "-flags", "low_delay"}
	a = append(a, inputs...)
	a = append(a, "-map", "0:v")
	switch encoder {
	case "videotoolbox":
		a = append(a, "-c:v", "h264_videotoolbox", "-realtime", "1", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0")
	case "nvenc":
		a = append(a, "-c:v", "h264_nvenc", "-preset", "p1", "-tune", "ull", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0")
	case "qsv":
		a = append(a, "-vf", "format=nv12", "-c:v", "h264_qsv", "-preset", "veryfast", "-profile:v", "baseline", "-bf", "0", "-low_power", "1")
	case "amf":
		a = append(a, "-c:v", "h264_amf", "-usage", "ultralowlatency", "-profile:v", "constrained_baseline", "-pix_fmt", "yuv420p", "-bf", "0")
	default:
		a = append(a, "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-bf", "0")
	}
	k := strconv.Itoa(kbps) + "k"
	a = append(a, "-g", strconv.Itoa(fps), "-keyint_min", strconv.Itoa(fps), "-b:v", k, "-maxrate", k, "-bufsize", strconv.Itoa(max(kbps/4, 100))+"k",
		"-f", "rtp", "-payload_type", "102", "rtp://127.0.0.1:"+strconv.Itoa(rtpVideo)+"?pkt_size=1200",
		"-map", "1:a", "-c:a", "libopus", "-b:a", "128k", "-application", "lowdelay", "-frame_duration", "10",
		"-f", "rtp", "-payload_type", "111", "rtp://127.0.0.1:"+strconv.Itoa(rtpAudio)+"?pkt_size=1200")
	return a
}

// Encoders are the encoder choices on this system, for the emulator's stream.encoder setting.
func Encoders() []string { return platformEncoders() }

// paceAudio writes sound to FFmpeg in 10 ms pieces at the rate it plays, as signed 16-bit
// samples, filling with silence when the emulator makes none. A capture only has sound while
// something plays; FFmpeg waiting on a silent pipe would hold the picture back with it.
func paceAudio(ctx context.Context, in <-chan []byte, out io.Writer, rate, channels int) {
	piece := rate / 100 * channels * 2
	var pending []byte
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	fill:
		for len(pending) < piece {
			select {
			case b := <-in:
				pending = append(pending, b...)
			default:
				break fill
			}
		}
		// Never more than a quarter of a second behind: older sound is dropped.
		if most := piece * 25; len(pending) > most {
			pending = pending[len(pending)-most:]
		}
		chunk := make([]byte, piece)
		n := copy(chunk, pending)
		pending = pending[n:]
		if _, err := out.Write(chunk); err != nil {
			return
		}
	}
}
