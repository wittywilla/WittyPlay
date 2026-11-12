// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package local

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/sandbox/docker"
)

func localSpec(t *testing.T, mut func(*docker.Request)) docker.Spec {
	t.Helper()
	r := docker.Request{
		SessionID: "0123456789abcdef0123456789abcdef", UserID: 7, Emulator: "flycast", Local: true, IODir: "/tmp/wp-io-1",
		ROM: "/library/dc/game.chd", SaveDir: "/data/saves/7/flycast", BIOSDir: "/data/bios", ProgramDir: "/data/emulators/flycast/2.4", AppImage: true,
		Cmd: []string{"/opt/emulator/flycast.AppImage", "/game/game.chd"}, Stream: "pipe", InputToken: docker.NewSecret(),
		Limits: docker.Limits{MemoryMB: 2048, CPUs: 1.5, Pids: 256},
	}
	if mut != nil {
		mut(&r)
	}
	s, err := docker.Build(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// has says the flags appear in a, together and in order.
func has(a []string, flags ...string) bool {
	for i := 0; i+len(flags) <= len(a); i++ {
		if slices.Equal(a[i:i+len(flags)], flags) {
			return true
		}
	}
	return false
}

// TestArgsBoxTheEmulatorIn holds every part of the sandbox in place: a change that drops one
// fails here.
func TestArgsBoxTheEmulatorIn(t *testing.T) {
	a, err := Args(localSpec(t, nil), "/data/local/entrypoint.sh", "/opt/wittyplay/wittyplay-input-agent", "wittyplay-1")
	if err != nil {
		t.Fatal(err)
	}
	if a[0] != "systemd-run" {
		t.Fatalf("the limits don't come first: %v", a[:3])
	}
	for what, flags := range map[string][]string{
		"every namespace, the network included": {"--unshare-all"},
		"dies with the module":                  {"--die-with-parent"},
		"no capabilities":                       {"--cap-drop", "ALL"},
		"nothing of the module's environment":   {"--clearenv"},
		"the system, read-only":                 {"--ro-bind", "/usr", "/usr"},
		"its own /proc":                         {"--proc", "/proc"},
		"its own /dev":                          {"--dev", "/dev"},
		"the game, read-only":                   {"--ro-bind", "/library/dc/game.chd", "/game/game.chd"},
		"the BIOS folder, read-only":            {"--ro-bind", "/data/bios", "/bios"},
		"the emulator, read-only":               {"--ro-bind", "/data/emulators/flycast/2.4", "/opt/emulator"},
		"saves, writable":                       {"--bind", "/data/saves/7/flycast", "/saves"},
		"the socket's folder":                   {"--bind", "/tmp/wp-io-1", docker.ContainerIO},
		"the entrypoint, read-only":             {"--ro-bind", "/data/local/entrypoint.sh", toolDir + "/wittyplay-entrypoint"},
		"the agent, read-only":                  {"--ro-bind", "/opt/wittyplay/wittyplay-input-agent", toolDir + "/wittyplay-input-agent"},
		"a memory limit":                        {"-p", "MemoryMax=2147483648"},
		"no swap":                               {"-p", "MemorySwapMax=0"},
		"a process limit":                       {"-p", "TasksMax=256"},
		"a processor limit":                     {"-p", "CPUQuota=150%"},
		"a private /tmp":                        {"--tmpfs", "/tmp"},
		"a private home":                        {"--tmpfs", docker.ContainerEmuHome},
	} {
		if !has(a, flags...) {
			t.Errorf("%s: %v is missing from\n%s", what, flags, strings.Join(a, " "))
		}
	}
	for _, never := range []string{"--share-net", "--dev-bind", "/home", "/etc/passwd", "/var", "--bind-try", "/etc"} {
		if slices.Contains(a, never) {
			t.Errorf("%s is given to the sandbox", never)
		}
	}
	// Writable binds: saves and the socket's folder, and nothing else.
	n := 0
	for _, f := range a {
		if f == "--bind" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d writable folders, want 2", n)
	}
	// The command comes last, after "--", untouched.
	if !slices.Equal(a[len(a)-3:], []string{toolDir + "/wittyplay-entrypoint", "/opt/emulator/flycast.AppImage", "/game/game.chd"}) {
		t.Errorf("the command: %v", a[len(a)-3:])
	}
	// The graphics card only when it was asked for.
	g, err := Args(localSpec(t, func(r *docker.Request) { r.GPU.DRI = true }), "/e", "/a", "u")
	if err != nil || !has(g, "--dev-bind", "/dev/dri", "/dev/dri") {
		t.Errorf("gpu: %v", err)
	}
}

func TestArgsRefuseWhatIsntLocalOrIsWeakened(t *testing.T) {
	s := localSpec(t, nil)
	s.Privileged = true
	if _, err := Args(s, "/e", "/a", "u"); err == nil {
		t.Error("a weakened spec was accepted")
	}
	d, err := docker.Build(docker.Request{SessionID: "0123456789abcdef", UserID: 1, Emulator: "x",
		Image: "ghcr.io/a/b:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ROM:   "/g/a.iso", SaveDir: "/s/1", Stream: "pipe", InputToken: docker.NewSecret(), Network: "wittyplay", RTPHost: "172.30.0.1", RTPVideo: 1, RTPAudio: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Args(d, "/e", "/a", "u"); err == nil {
		t.Error("a Docker spec was run locally")
	}
}

func TestMissingSaysWhy(t *testing.T) {
	none := func(string) (string, error) { return "", errors.New("not found") }
	r := &Runtime{GOOS: "darwin", Look: none}
	if m := r.Missing(); len(m) != 1 || !strings.Contains(m[0], "only Linux") {
		t.Errorf("macOS: %v", m)
	}
	r = &Runtime{GOOS: "linux", Look: none, Agent: "/nonexistent/agent"}
	m := strings.Join(r.Missing(), "\n")
	for _, want := range []string{"bubblewrap", "systemd-run", "Xvfb", "ffmpeg", "pulseaudio", "xdotool", "wittyplay-input-agent"} {
		if !strings.Contains(m, want) {
			t.Errorf("Linux with nothing installed doesn't mention %s:\n%s", want, m)
		}
	}
	if st := r.Available(context.Background()); st.Available || st.Error == "" {
		t.Errorf("available with nothing installed: %+v", st)
	}
	if _, err := r.Create(context.Background(), localSpec(t, nil)); !errors.Is(err, docker.ErrUnavailable) {
		t.Errorf("create with nothing installed: %v", err)
	}
}

// FreeBSD: no bubblewrap, so every container path becomes this machine's, under limits(1).
func TestArgsLimitsOnly(t *testing.T) {
	s := localSpec(t, nil)
	work := filepath.Join(t.TempDir(), "run")
	a, err := ArgsLimitsOnly(s, "/x/entrypoint.sh", "/x/agent", work, 123)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(a, " ")
	for _, want := range []string{"limits -m " + strconv.FormatInt(s.MemoryBytes, 10) + " env -i", "HOME=" + filepath.Join(work, "home"), "XDG_RUNTIME_DIR=" + filepath.Join(work, "tmp", "runtime"),
		"WITTYPLAY_ROM=/library/dc/game.chd", filepath.Join(work, "tools", "wittyplay-entrypoint") + " /data/emulators/flycast/2.4/flycast.AppImage /library/dc/game.chd"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in\n%s", want, line)
		}
	}
	if !strings.Contains(line, "WITTYPLAY_RTP_VIDEO=40492 ") || !strings.Contains(line, "WITTYPLAY_RTP_AUDIO=40494 ") {
		t.Errorf("the session's RTP ports are shared:\n%s", line)
	}
	if !strings.Contains(line, "DISPLAY=:123 ") || !strings.Contains(line, "PULSE_SERVER=unix:"+filepath.Join(work, "tmp", "runtime")) {
		t.Errorf("the display or the sound server is shared:\n%s", line)
	}
	for _, c := range []string{"=" + docker.ContainerROM + "/", "=" + docker.ContainerSaves + "/", " " + docker.ContainerProgram + "/", "HOME=/home", "RUNTIME_DIR=/tmp/", ":/tmp/"} {
		if strings.Contains(line, c) {
			t.Errorf("a container path %q is left in\n%s", c, line)
		}
	}
	if _, err := ArgsLimitsOnly(s, "e", "a", "relative", 123); err == nil {
		t.Error("a relative session folder was taken")
	}
}
