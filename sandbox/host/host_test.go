// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package host

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/sandbox/docker"
)

func hostSpec(t *testing.T) docker.Spec {
	t.Helper()
	dir := t.TempDir()
	req := docker.Request{SessionID: "0123456789abcdef0123456789abcdef", UserID: 7, Emulator: "dolphin", ROM: filepath.Join(dir, "games", "a game.iso"),
		SaveDir: filepath.Join(dir, "saves"), BIOSDir: filepath.Join(dir, "bios"), ProgramDir: filepath.Join(dir, "app"),
		Cmd:    []string{docker.ContainerProgram + "/Dolphin.app/Contents/MacOS/Dolphin", "-u", docker.ContainerSaves + "/config", "--bios=" + docker.ContainerBIOS, "-e", docker.ContainerROM + "/a game.iso"},
		Env:    []string{"DOLPHIN_EMU_USERPATH=" + docker.ContainerSaves + "/config"},
		Stream: "pipe", Network: "x", RTPHost: "127.0.0.1", RTPVideo: 5100, RTPAudio: 5102, InputToken: strings.Repeat("k", 32), Host: true,
		Width: 1280, Height: 720, FPS: 60, Kbps: 6000, Encoder: "videotoolbox", Limits: docker.Limits{UID: 1000}}
	s, err := docker.Build(req)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A spec built for a container's paths runs here with this machine's own: the program from its
// installed folder, the game, the save folder and the BIOS folder, in arguments and variables.
func TestPlanTurnsContainerPathsIntoThisMachines(t *testing.T) {
	s := hostSpec(t)
	p, err := planFor(s)
	if err != nil {
		t.Fatal(err)
	}
	var app, saves, bios, rom string
	for _, m := range s.Mounts {
		switch {
		case m.Target == docker.ContainerProgram:
			app = m.Source
		case m.Target == docker.ContainerSaves:
			saves = m.Source
		case m.Target == docker.ContainerBIOS:
			bios = m.Source
		default:
			rom = m.Source
		}
	}
	if want := filepath.Join(app, "Dolphin.app", "Contents", "MacOS", "Dolphin"); p.Program != want {
		t.Errorf("program %q, want %q", p.Program, want)
	}
	want := []string{"-u", filepath.Join(saves, "config"), "--bios=" + bios, "-e", rom}
	if !slices.Equal(p.Args, want) {
		t.Errorf("args %q, want %q", p.Args, want)
	}
	if !slices.Contains(p.Env, "DOLPHIN_EMU_USERPATH="+filepath.Join(saves, "config")) {
		t.Errorf("env %q", p.Env)
	}
	for _, kv := range p.Env {
		if strings.HasPrefix(kv, "WITTYPLAY_") || strings.HasPrefix(kv, "DISPLAY=") {
			t.Errorf("the emulator was given the stream's own variable %s", kv)
		}
	}
	if p.RTPVideo != 5100 || p.RTPAudio != 5102 || p.Width != 1280 || p.FPS != 60 || p.Encoder != "videotoolbox" {
		t.Errorf("stream %+v", p)
	}
}

// Only a spec for this machine, sending to loopback, with its program inside its own folder.
func TestPlanRefusals(t *testing.T) {
	s := hostSpec(t)
	bad := s
	bad.Host, bad.Network = false, docker.LocalNetwork
	if _, err := planFor(bad); err == nil {
		t.Error("a spec that isn't for this machine was taken")
	}
	out := s
	out.Cmd = append([]string{"/usr/bin/somewhere-else"}, s.Cmd[1:]...)
	if _, err := planFor(out); err == nil {
		t.Error("a program outside the installed folder was taken")
	}
	if _, err := docker.Build(docker.Request{SessionID: "0123456789abcdef0123456789abcdef", Emulator: "x", Host: true, RTPHost: "10.0.0.2", Stream: "pipe"}); err == nil {
		t.Error("an emulator on this machine streaming off loopback was built")
	}
}

func TestEncoderArgs(t *testing.T) {
	a := strings.Join(EncoderArgs([]string{"-f", "rawvideo", "-i", "pipe:0", "-f", "s16le", "-i", "pipe:3"}, "", 60, 6000, 5100, 5102), " ")
	for _, want := range []string{"-c:v libx264", "-tune zerolatency", "-bf 0", "-g 60", "-b:v 6000k", "-bufsize 1500k", "rtp://127.0.0.1:5100?pkt_size=1200", "-c:a libopus", "-frame_duration 10", "rtp://127.0.0.1:5102?pkt_size=1200"} {
		if !strings.Contains(a, want) {
			t.Errorf("missing %q in %s", want, a)
		}
	}
	if !strings.Contains(strings.Join(EncoderArgs(nil, "videotoolbox", 30, 3000, 1, 2), " "), "h264_videotoolbox -realtime 1") {
		t.Error("VideoToolbox isn't asked for real time")
	}
}
