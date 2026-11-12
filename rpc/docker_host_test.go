// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/wittywilla/wittyplay/emulators"
)

func TestCommandLine(t *testing.T) {
	dolphin := emulators.Find("dolphin")
	art := &emulators.Artifact{Args: []string{"-u", "{config}", "-b", "{settings}", "-e", "{game}"}}
	got := CommandLine(dolphin, art, "Dolphin.app/Contents/MacOS/Dolphin", []string{"--extra"}, "g.iso")
	want := []string{"/opt/emulator/Dolphin.app/Contents/MacOS/Dolphin", "-u", "/saves/config", "-b", "--extra", "-e", "/game/g.iso"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// No {game}: it goes last, with the settings' arguments before it.
	got = CommandLine(&emulators.Emulator{Args: []string{"-f"}}, &emulators.Artifact{ConfigDir: "home/x"}, "e", []string{"--s"}, "g.nds")
	if want := []string{"/opt/emulator/e", "-f", "--s", "/game/g.nds"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	// {config} is the build's own settings folder.
	got = CommandLine(&emulators.Emulator{Args: []string{"--config={config}/a.ini", "{game}"}}, &emulators.Artifact{ConfigDir: "config/ppsspp"}, "e", nil, "g")
	if got[1] != "--config=/saves/config/ppsspp/a.ini" {
		t.Errorf("got %q", got)
	}
}

func TestHostPaths(t *testing.T) {
	v := hostPaths(emulators.Values{"biosFolder": "/bios", "other": "/biosphere", "n": 2.0, "saves": "/saves/memcards"}, "/s", "/b")
	if v["biosFolder"] != "/b" || v["other"] != "/biosphere" || v["n"] != 2.0 || v["saves"] != filepath.Join("/s", "memcards") {
		t.Errorf("%v", v)
	}
}

// An administrator's own program: a real program file on this server, for consoles that exist,
// found by Find and ForSystem once set, and taking the settings of the emulator it's a build of.
func TestOwnPrograms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("programs are marked differently there")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "rpcs3")
	if err := os.WriteFile(prog, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ok := func(c string) bool { return c == "ps3" || c == "switch" }
	o, err := emulators.CheckOwn(emulators.Own{Name: "RPCS3 from packages", Systems: []string{"ps3"}, Program: prog, Like: "rpcs3"}, ok)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID != "own-rpcs3-from-packages" {
		t.Errorf("id %q", o.ID)
	}
	for name, bad := range map[string]emulators.Own{
		"relative path": {Name: "x", Systems: []string{"ps3"}, Program: "rpcs3"},
		"missing":       {Name: "x", Systems: []string{"ps3"}, Program: filepath.Join(dir, "nope")},
		"a folder":      {Name: "x", Systems: []string{"ps3"}, Program: dir},
		"no console":    {Name: "x", Program: prog},
		"wrong console": {Name: "x", Systems: []string{"nes64"}, Program: prog},
		"line break":    {Name: "x", Systems: []string{"ps3"}, Program: prog, Args: []string{"a\nb"}},
		"settings out":  {Name: "x", Systems: []string{"ps3"}, Program: prog, ConfigDir: "../../etc"},
		"unknown like":  {Name: "x", Systems: []string{"ps3"}, Program: prog, Like: "nothing"},
	} {
		if _, err := emulators.CheckOwn(bad, ok); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	notExec := filepath.Join(dir, "data")
	_ = os.WriteFile(notExec, nil, 0o644)
	if _, err := emulators.CheckOwn(emulators.Own{Name: "x", Systems: []string{"ps3"}, Program: notExec}, ok); err == nil {
		t.Error("a file that can't run was accepted")
	}
	emulators.SetOwn([]emulators.Own{o})
	t.Cleanup(func() { emulators.SetOwn(nil) })
	e := emulators.Find(o.ID)
	if e == nil || !e.Own || e.Base() != "rpcs3" || !e.NeedsBios || len(e.Args) == 0 {
		t.Fatalf("%+v", e)
	}
	if !slices.ContainsFunc(emulators.ForSystem("ps3"), func(x emulators.Emulator) bool { return x.ID == o.ID }) {
		t.Error("ForSystem doesn't list it")
	}
	b := &DockerBackend{}
	if in := b.installed(e); in == nil || in.Dir != dir || in.Entry != "rpcs3" {
		t.Errorf("installed %+v", in)
	}
}

func TestDumpRoot(t *testing.T) {
	for in, want := range map[string][2]string{
		"/g/PS3/Game/PS3_DISC.SFB":  {"/g/PS3/Game", ""},
		"/g/WiiU/Game/code/run.rpx": {"/g/WiiU/Game", "code/run.rpx"},
		"/g/WiiU/run.rpx":           {"/g/WiiU/run.rpx", ""},
		"/g/PS2/game.iso":           {"/g/PS2/game.iso", ""},
	} {
		root, rel := dumpRoot(filepath.FromSlash(in))
		if root != filepath.FromSlash(want[0]) || rel != want[1] {
			t.Errorf("%s: %q %q", in, root, rel)
		}
	}
	got := CommandLine(&emulators.Emulator{Args: []string{"-g", "{game}"}}, nil, "Cemu", nil, path.Join("Game", "code/run.rpx"))
	if got[2] != "/game/Game/code/run.rpx" {
		t.Errorf("%q", got)
	}
}
