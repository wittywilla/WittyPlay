// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestLifecyclePauseAndResume(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	l := &Lifecycle{RT: f}
	c, err := l.Start(ctx, spec(t, "aaaaaaaaaaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Pause(ctx, c.SessionID); err != nil || !c.Paused() {
		t.Fatalf("pause: %v, paused=%v", err, c.Paused())
	}
	if info, _ := f.Inspect(ctx, c.ID); !info.Paused {
		t.Error("the runtime wasn't asked to pause")
	}
	// Pausing twice is nothing; a paused game still counts, and still stops cleanly.
	if err := l.Pause(ctx, c.SessionID); err != nil || l.Count() != 1 {
		t.Fatalf("pause again: %v, count %d", err, l.Count())
	}
	if err := l.Resume(ctx, c.SessionID); err != nil || c.Paused() {
		t.Fatalf("resume: %v", err)
	}
	if err := l.Pause(ctx, "nobody"); err != ErrNoContainer {
		t.Errorf("pausing nothing: %v", err)
	}
	_ = l.Pause(ctx, c.SessionID)
	if err := l.Stop(ctx, c.SessionID); err != nil || f.Live() != 0 {
		t.Fatalf("stopping a paused game: %v, %d live", err, f.Live())
	}
}

func TestLifecycleOneGameEach(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	l := &Lifecycle{RT: f, Max: 4}
	first := spec(t, "aaaaaaaaaaaaaaaa")
	second := spec(t, "bbbbbbbbbbbbbbbb")
	second.Labels = map[string]string{LabelManaged: "true", LabelSession: "bbbbbbbbbbbbbbbb", LabelUser: first.Labels[LabelUser], LabelEmu: "pcsx2"}
	a, err := l.Start(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Start(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()
	if a.Reason() != "replaced" || l.Count() != 1 || l.Get(b.SessionID) == nil || f.Live() != 1 {
		t.Errorf("reason %q, count %d, live %d", a.Reason(), l.Count(), f.Live())
	}
}

// TestLocalSpec: the sandbox without Docker gets the same hardened spec, with no image, no
// network and no ports, and nothing can turn a Docker spec into one or back.
func TestLocalSpec(t *testing.T) {
	r := request()
	r.Local, r.Image, r.IODir, r.Network = true, "", "/tmp/wp-io-1", ""
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Local || s.Image != "" || s.Network != LocalNetwork || len(s.Publish) != 0 || !s.ReadOnlyRoot || s.PidsLimit <= 0 || s.MemoryBytes <= 0 || s.NanoCPUs <= 0 {
		t.Fatalf("local spec: %+v", s)
	}
	if err := Check(s); err != nil {
		t.Fatal(err)
	}
	env := strings.Join(s.Env, "\n")
	for _, want := range []string{"WITTYPLAY_IO=" + ContainerIO, "WITTYPLAY_RTP_HOST=127.0.0.1", "WITTYPLAY_RTP_VIDEO=5004"} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment lacks %s", want)
		}
	}
	if !slices.ContainsFunc(s.Mounts, func(m Mount) bool { return m.Target == ContainerIO && m.Source == "/tmp/wp-io-1" }) {
		t.Errorf("no socket folder: %+v", s.Mounts)
	}
	for name, m := range map[string]func(*Request){
		"with an image":    func(r *Request) { r.Image = pinned },
		"as a desktop":     func(r *Request) { r.Stream, r.KasmToken = "kasmvnc", NewSecret() },
		"with a port":      func(r *Request) { r.PublishLoopback = true },
		"without a socket": func(r *Request) { r.IODir = "" },
	} {
		bad := r
		m(&bad)
		if _, err := Build(bad); err == nil {
			t.Errorf("a local sandbox %s was accepted", name)
		}
	}
	docker := s
	docker.Local = false
	if Check(docker) == nil {
		t.Error("a spec with no network and no image passed as Docker's")
	}
	// The socket's folder is only ever mounted in a local sandbox.
	d, _ := Build(request())
	d.Mounts = append(slices.Clone(d.Mounts), Mount{Source: "/tmp/x", Target: ContainerIO})
	if Check(d) == nil {
		t.Error("a container was given the local sandbox's socket folder")
	}
}

func TestStreamSettingsReachTheContainer(t *testing.T) {
	r := request()
	r.Width, r.Height, r.FPS, r.Kbps, r.Keys = 1920, 1080, 30, 3000, `[{"a":"c"}]`
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	env := strings.Join(s.Env, "\n")
	for _, want := range []string{"WITTYPLAY_WIDTH=1920", "WITTYPLAY_HEIGHT=1080", "WITTYPLAY_FPS=30", "WITTYPLAY_KBPS=3000", `WITTYPLAY_KEYS=[{"a":"c"}]`} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment lacks %s:\n%s", want, env)
		}
	}
	d, _ := Build(request())
	if e := strings.Join(d.Env, "\n"); !strings.Contains(e, "WITTYPLAY_WIDTH=1280") || !strings.Contains(e, "WITTYPLAY_KBPS=6000") {
		t.Errorf("defaults: %s", e)
	}
	for name, m := range map[string]func(*Request){
		"odd width":              func(r *Request) { r.Width, r.Height = 1281, 720 },
		"huge":                   func(r *Request) { r.Width, r.Height = 7680, 4320 },
		"no frames":              func(r *Request) { r.FPS = 1 },
		"no bitrate":             func(r *Request) { r.Kbps = 5 },
		"unknown encoder":        func(r *Request) { r.Encoder = "sh -c" },
		"vaapi, no card":         func(r *Request) { r.Encoder = "vaapi" },
		"keys with a line break": func(r *Request) { r.Keys = "[]\nLD_PRELOAD=x" },
	} {
		bad := request()
		m(&bad)
		if _, err := Build(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	gpu := request()
	gpu.Encoder, gpu.GPU.DRI = "vaapi", true
	if s, err := Build(gpu); err != nil || !slices.Contains(s.Env, "WITTYPLAY_ENCODER=vaapi") {
		t.Errorf("vaapi with the card passed through: %v", err)
	}
}

func TestImagesBuiltHere(t *testing.T) {
	id := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !Pinned(id) || !LocalImage(id) || LocalImage(pinned) || Pinned("sha256:abc") || Pinned("myimage") || Pinned("myimage:latest") {
		t.Fatal("what counts as pinned is wrong")
	}
	r := request()
	r.Image = id
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	// It's never pulled: there's nowhere to pull an id from.
	f := &Fake{}
	l := &Lifecycle{RT: f, Pull: true}
	if _, err := l.Start(context.Background(), s); err != nil || len(f.Pulled) != 0 {
		t.Errorf("start: %v, pulled %v", err, f.Pulled)
	}
	if _, err := l.Start(context.Background(), spec(t, "cccccccccccccccc")); err != nil || len(f.Pulled) != 1 {
		t.Errorf("a registry image wasn't pulled: %v %v", err, f.Pulled)
	}
}

func TestHostPaths(t *testing.T) {
	for _, p := range []string{"/", "/etc", "/etc/shadow", "/proc/1/root", "/sys/kernel", "/dev/sda", "/boot/efi", "/root/.ssh", "/run", "/var", "/usr", "/home",
		"/var/run/docker.sock", "/run/docker/x", "/var/lib/docker/volumes", "/run/user/1000/docker.sock", "/x/containerd.sock", "relative", "/a/../b", "/a/", ""} {
		if hostPath(p) == nil {
			t.Errorf("%q can be mounted", p)
		}
	}
	for _, p := range []string{"/library/games/ps2/game.iso", "/run/media/me/Disk/games/a.iso", "/home/me/games", "/var/lib/wittymediaserver/modules/wittyplay/docker/saves/7/pcsx2", "/mnt/nas/bios"} {
		if err := hostPath(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestSettingsRuntime(t *testing.T) {
	s := DefaultSettings()
	s.Enabled, s.Runtime = true, RuntimeLocal
	// The local sandbox streams everything, whatever the list or an override says of a desktop.
	if b, err := Choose(s, true, "cemu", "kasmvnc"); err != nil || b != BackendLocalWebRTC {
		t.Errorf("local: %v %v", b, err)
	}
	s.Overrides["cemu"] = BackendProcess
	if _, err := Choose(s, true, "cemu", "kasmvnc"); err == nil {
		t.Error("an emulator turned off ran in the local sandbox")
	}
	s.Runtime = "chroot"
	if _, err := s.Clean(); err == nil {
		t.Error("an unknown runtime was accepted")
	}
}
