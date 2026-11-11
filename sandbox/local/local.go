// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package local runs a standalone emulator without Docker: in a bubblewrap sandbox on this
// machine, held to memory, processor and process limits by a systemd scope. It's for a Linux
// server that has no Docker daemon, or whose owner doesn't want anything to hold the daemon's
// socket. It takes the same hardened Spec the Docker engine takes (docker.Build with Local set)
// and is a docker.Runtime, so the lifecycle, the limits and the session code are shared.
//
// What the sandbox is: new user, mount, PID, IPC, UTS, cgroup and network namespaces
// (--unshare-all), so the emulator sees no other process, has no capabilities, and has no
// network at all, only a loopback of its own; a root made of the system's /usr and a few
// folders of /etc read-only, with the game, BIOS folder and emulator read-only and one person's
// saves writable; and it dies with the module (--die-with-parent). The picture, sound and input
// cross through one Unix socket (inputagent.Bridge).
//
// What it isn't: there's no seccomp filter (bubblewrap applies one only if handed a compiled
// program, and none is written yet), and the emulator runs on this machine's own kernel and
// libraries rather than an image's. A container is the stronger box. docs/DOCKER-BACKEND.md says
// so to the person choosing.
//
// Only Linux has it. bubblewrap is Linux's; the emulators in the list are Linux programs; and
// macOS's sandbox-exec and Windows's job objects, which box WittyPlay's own game runner in, have
// no X display or PulseAudio to capture from.
package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	base "github.com/wittywilla/wittyplay/docker/images/base"
	"github.com/wittywilla/wittyplay/sandbox/docker"
)

// Where the entrypoint and the agent are inside the sandbox.
const toolDir = "/run/wittyplay"

// Programs the sandbox needs on this machine, besides bubblewrap and systemd-run.
var needs = []string{"Xvfb", "xdpyinfo", "xdotool", "pulseaudio", "pactl", "ffmpeg"}

// Runtime is the local sandbox.
type Runtime struct {
	// Dir is a folder of the module's own, for the entrypoint and logs.
	Dir string
	// Agent is the wittyplay-input-agent program. Empty: beside this program.
	Agent string
	// Look finds a program; exec.LookPath if nil. GOOS is runtime.GOOS if empty. For tests.
	Look func(string) (string, error)
	GOOS string

	mu    sync.Mutex
	seq   int
	procs map[string]*proc
}

type proc struct {
	spec    docker.Spec
	info    docker.Info
	cmd     *exec.Cmd
	exited  chan struct{}
	logPath string
}

func (r *Runtime) look(name string) (string, error) {
	if r.Look != nil {
		return r.Look(name)
	}
	return exec.LookPath(name)
}

func (r *Runtime) agent() string {
	if r.Agent != "" {
		return r.Agent
	}
	if self, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(self), "wittyplay-input-agent")
	}
	return "wittyplay-input-agent"
}

// Missing lists what this machine lacks for the local sandbox, in words for a settings page.
func (r *Runtime) Missing() []string {
	goos := r.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return []string{"the local sandbox is bubblewrap, which only Linux has; on " + goos + " standalone emulators need Docker"}
	}
	var out []string
	if _, err := r.look("bwrap"); err != nil {
		out = append(out, "bubblewrap (the bwrap program) isn't installed")
	}
	if _, err := r.look("systemd-run"); err != nil {
		out = append(out, "systemd-run isn't here, and without it nothing holds an emulator to its memory and processor limits")
	}
	for _, n := range needs {
		if _, err := r.look(n); err != nil {
			out = append(out, n+" isn't installed")
		}
	}
	if st, err := os.Stat(r.agent()); err != nil || st.IsDir() {
		out = append(out, "wittyplay-input-agent isn't beside the module")
	}
	return out
}

// Available says whether the local sandbox can run here.
func (r *Runtime) Available(context.Context) docker.Status {
	st := docker.Status{Host: "this machine's own sandbox (bubblewrap)", OS: runtime.GOOS, Arch: runtime.GOARCH, Rootless: true,
		CPUs: runtime.NumCPU(), MemoryMB: memoryMB()}
	if m := r.Missing(); len(m) > 0 {
		st.Error = strings.Join(m, "; ")
		return st
	}
	st.Available = true
	return st
}

// memoryMB reads the machine's memory from /proc/meminfo; 0 when it can't.
func memoryMB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				kb, _ := strconv.ParseInt(f[0], 10, 64)
				return kb >> 10
			}
		}
	}
	return 0
}

// Pull has nothing to do: there are no images.
func (r *Runtime) Pull(context.Context, string) error { return nil }

// Args is the whole command for one sandbox: systemd-run holding the limits, then bubblewrap
// making the box, then the entrypoint and the emulator. It's a function of the spec and three
// paths alone, so a test can hold every flag in place.
func Args(s docker.Spec, entrypoint, agent, unit string) ([]string, error) {
	if err := docker.Check(s); err != nil {
		return nil, err
	}
	if !s.Local {
		return nil, errors.New("this spec is for Docker, not the local sandbox")
	}
	a := []string{"systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit", unit,
		"-p", "MemoryMax=" + strconv.FormatInt(s.MemoryBytes, 10),
		"-p", "MemorySwapMax=0",
		"-p", "TasksMax=" + strconv.FormatInt(s.PidsLimit, 10),
		"-p", "CPUQuota=" + strconv.FormatInt(s.NanoCPUs/1e7, 10) + "%",
		"--",
		"bwrap", "--unshare-all", "--die-with-parent", "--cap-drop", "ALL", "--hostname", "wittyplay", "--clearenv",
		"--ro-bind", "/usr", "/usr",
	}
	for _, d := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/etc/ld.so.cache", "/etc/ld.so.conf", "/etc/ld.so.conf.d", "/etc/alternatives",
		"/etc/fonts", "/etc/X11", "/etc/pulse", "/etc/glvnd", "/etc/vulkan", "/etc/OpenCL"} {
		a = append(a, "--ro-bind-try", d, d)
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/run", "--dir", toolDir)
	tmp := make([]string, 0, len(s.Tmpfs))
	for t := range s.Tmpfs {
		tmp = append(tmp, t)
	}
	sort.Strings(tmp)
	for _, t := range tmp {
		a = append(a, "--tmpfs", t)
	}
	for _, m := range s.Mounts {
		flag := "--bind"
		if m.ReadOnly {
			flag = "--ro-bind"
		}
		a = append(a, flag, m.Source, m.Target)
	}
	for _, d := range s.Devices {
		a = append(a, "--dev-bind", d, d)
	}
	a = append(a, "--ro-bind", entrypoint, toolDir+"/wittyplay-entrypoint", "--ro-bind", agent, toolDir+"/wittyplay-input-agent")
	a = append(a, "--setenv", "PATH", toolDir+":/usr/local/bin:/usr/bin:/bin")
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		a = append(a, "--setenv", k, v)
	}
	a = append(a, "--chdir", docker.ContainerEmuHome, "--", toolDir+"/wittyplay-entrypoint")
	return append(a, s.Cmd...), nil
}

// Create notes a sandbox to start. Nothing runs yet.
func (r *Runtime) Create(_ context.Context, s docker.Spec) (string, error) {
	if err := docker.Check(s); err != nil {
		return "", fmt.Errorf("refusing to make a sandbox: %w", err)
	}
	if !s.Local {
		return "", errors.New("this spec is for Docker, not the local sandbox")
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
	id := fmt.Sprintf("local-%d-%d", os.Getpid(), r.seq)
	r.procs[id] = &proc{spec: s, info: docker.Info{ID: id, Name: s.Name, State: "created", Labels: s.Labels, Created: time.Now()}}
	return id, nil
}

func (r *Runtime) get(id string) (*proc, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.procs[id]
	if p == nil {
		return nil, errors.New("no such sandbox")
	}
	return p, nil
}

// Start runs the sandbox.
func (r *Runtime) Start(_ context.Context, id string) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return err
	}
	entry := filepath.Join(r.Dir, "entrypoint.sh")
	if err := os.WriteFile(entry, base.Entrypoint, 0o500); err != nil && !errors.Is(err, os.ErrPermission) {
		return err
	}
	args, err := Args(p.spec, entry, r.agent(), "wittyplay-"+strings.ReplaceAll(id, "local-", ""))
	if err != nil {
		return err
	}
	path, err := r.look(args[0])
	if err != nil {
		return err
	}
	p.logPath = filepath.Join(r.Dir, p.spec.Name+".log")
	logf, err := os.OpenFile(p.logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args[1:]...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "XDG_RUNTIME_DIR=" + os.Getenv("XDG_RUNTIME_DIR"), "DBUS_SESSION_BUS_ADDRESS=" + os.Getenv("DBUS_SESSION_BUS_ADDRESS")}
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		logf.Close()
		return err
	}
	logf.Close()
	exited := make(chan struct{})
	r.mu.Lock()
	p.cmd, p.exited = cmd, exited
	p.info.Running, p.info.State = true, "running"
	r.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		r.mu.Lock()
		p.info.Running, p.info.Paused, p.info.State = false, false, "exited"
		r.mu.Unlock()
		close(exited)
	}()
	return nil
}

// Stop asks the sandbox to end, and ends it if it hasn't within timeout.
func (r *Runtime) Stop(ctx context.Context, id string, timeout time.Duration) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.cmd == nil {
		return nil
	}
	signalGroup(p.cmd, sigCont)
	signalGroup(p.cmd, sigTerm)
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.exited:
		return nil
	case <-t.C:
	case <-ctx.Done():
	}
	signalGroup(p.cmd, sigKill)
	select {
	case <-p.exited:
	case <-time.After(5 * time.Second):
		return errors.New("the sandbox didn't end")
	}
	return nil
}

// Remove forgets a sandbox, ending it first if it's still there.
func (r *Runtime) Remove(ctx context.Context, id string) error {
	p, err := r.get(id)
	if err != nil {
		return err
	}
	if p.cmd != nil {
		select {
		case <-p.exited:
		default:
			_ = r.Stop(ctx, id, 2*time.Second)
		}
	}
	if p.logPath != "" {
		_ = os.Remove(p.logPath)
	}
	r.mu.Lock()
	delete(r.procs, id)
	r.mu.Unlock()
	return nil
}

// Pause freezes every process in the sandbox.
func (r *Runtime) Pause(_ context.Context, id string) error {
	p, err := r.get(id)
	if err != nil || p.cmd == nil {
		return errors.Join(err, errors.New("not running"))
	}
	signalGroup(p.cmd, sigStop)
	r.mu.Lock()
	p.info.Paused, p.info.State = true, "paused"
	r.mu.Unlock()
	return nil
}

// Unpause lets them go on.
func (r *Runtime) Unpause(_ context.Context, id string) error {
	p, err := r.get(id)
	if err != nil || p.cmd == nil {
		return errors.Join(err, errors.New("not running"))
	}
	signalGroup(p.cmd, sigCont)
	r.mu.Lock()
	p.info.Paused, p.info.State = false, "running"
	r.mu.Unlock()
	return nil
}

// Logs returns the end of what the sandbox said, at most 64 KiB.
func (r *Runtime) Logs(_ context.Context, id string, _ int) (string, error) {
	p, err := r.get(id)
	if err != nil || p.logPath == "" {
		return "", err
	}
	b, err := os.ReadFile(p.logPath)
	if err != nil {
		return "", err
	}
	if len(b) > 64<<10 {
		b = b[len(b)-64<<10:]
	}
	return string(b), nil
}

// Inspect says how a sandbox is.
func (r *Runtime) Inspect(_ context.Context, id string) (docker.Info, error) {
	p, err := r.get(id)
	if err != nil {
		return docker.Info{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return p.info, nil
}

// List finds this process's sandboxes. One left by a process that has gone doesn't exist: it
// died with it.
func (r *Runtime) List(_ context.Context, labels map[string]string) ([]docker.Info, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []docker.Info
	for _, p := range r.procs {
		ok := true
		for k, v := range labels {
			if p.info.Labels[k] != v {
				ok = false
			}
		}
		if ok {
			out = append(out, p.info)
		}
	}
	return out, nil
}

// EnsureNetwork has nothing to make: a sandbox has no network.
func (r *Runtime) EnsureNetwork(context.Context, docker.NetworkSpec) (docker.NetworkInfo, error) {
	return docker.NetworkInfo{Name: docker.LocalNetwork, Internal: true}, nil
}

var _ docker.Runtime = (*Runtime)(nil)
