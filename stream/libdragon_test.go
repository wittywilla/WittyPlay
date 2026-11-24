// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/wittywilla/wittyplay"
)

func n64File(t *testing.T, boot string) string {
	t.Helper()
	b := make([]byte, 0x1000)
	copy(b, []byte{0x80, 0x37, 0x12, 0x40})
	copy(b[0x500:], boot)
	p := filepath.Join(t.TempDir(), "game.z64")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// On a graphics card a libdragon game gets parallel-RDP over Vulkan: angrylion leaves it black.
func TestLibdragonGameOnACardGetsParallelRDP(t *testing.T) {
	start := map[string]string{"parallel-n64-gfxplugin": "gliden64", "parallel-n64-screensize": "1280x960"}
	args := []string{"-gpu", "-normal-priority"}
	got := forThisGame(Options{Console: "n64", CoreID: wittyplay.LibdragonCore, ROM: n64File(t, "Libdragon IPL3"), GPU: true, RunnerArgs: args, CoreOptions: start})
	for k, v := range libdragonCardOptions() {
		if got.CoreOptions[k] != v {
			t.Errorf("%s is %q", k, got.CoreOptions[k])
		}
	}
	if !got.GPU || !slices.Contains(got.RunnerArgs, "-gpu") || !slices.Contains(got.RunnerArgs, "-vulkan") {
		t.Errorf("gpu %v, args %v", got.GPU, got.RunnerArgs)
	}
	if len(got.Notices) != 1 {
		t.Errorf("notices: %v", got.Notices)
	}
	if slices.Contains(args, "-vulkan") || start["parallel-n64-gfxplugin"] != "gliden64" {
		t.Error("what was passed in was changed")
	}
}

// With no card it gets the software settings and no card.
func TestLibdragonGameGetsWhatItNeeds(t *testing.T) {
	start := map[string]string{"parallel-n64-gfxplugin": "gliden64", "parallel-n64-screensize": "1280x960"}
	args := []string{"-gpu", "-normal-priority"}
	got := forThisGame(Options{Console: "n64", CoreID: wittyplay.LibdragonCore, ROM: n64File(t, "Libdragon IPL3"), GPU: false, RunnerArgs: args, CoreOptions: start, Notices: []string{"No graphics card on this server, so its processor draws this game."}})
	for k, v := range wittyplay.LibdragonOptions() {
		if got.CoreOptions[k] != v {
			t.Errorf("%s is %q", k, got.CoreOptions[k])
		}
	}
	if got.CoreOptions["parallel-n64-screensize"] != "1280x960" {
		t.Error("an unrelated option went")
	}
	if got.GPU || slices.Contains(got.RunnerArgs, "-gpu") || !slices.Contains(got.RunnerArgs, "-normal-priority") {
		t.Errorf("gpu %v, args %v", got.GPU, got.RunnerArgs)
	}
	if len(got.Notices) != 1 {
		t.Errorf("notices: %v", got.Notices)
	}
	if len(got.Notices) != 1 || got.Notices[0] != libdragonNoCardNotice {
		t.Errorf("with no card the start screen should say it needs one: %v", got.Notices)
	}
	if !slices.Contains(args, "-gpu") || start["parallel-n64-gfxplugin"] != "gliden64" {
		t.Error("what was passed in was changed")
	}
}

func TestOtherGamesStartAsTheyAre(t *testing.T) {
	start := map[string]string{"a": "b"}
	for name, opt := range map[string]Options{
		"a commercial game":  {Console: "n64", CoreID: wittyplay.LibdragonCore, ROM: n64File(t, "")},
		"another core":       {Console: "n64", CoreID: "mupen64plus_next", ROM: n64File(t, "Libdragon IPL3")},
		"another console":    {Console: "nes", CoreID: wittyplay.LibdragonCore, ROM: n64File(t, "Libdragon IPL3")},
		"a file that's gone": {Console: "n64", CoreID: wittyplay.LibdragonCore, ROM: filepath.Join(t.TempDir(), "gone.z64")},
	} {
		opt.GPU, opt.CoreOptions = true, start
		got := forThisGame(opt)
		if !got.GPU || got.fixedOptions != nil || !maps.Equal(got.CoreOptions, start) || got.Notices != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// A settings change while it runs can't take from it what it can't run without.
func TestSettingsCannotUndoWhatAGameNeeds(t *testing.T) {
	got := withFixed(map[string]string{"parallel-n64-gfxplugin": "gliden64", "x": "y"}, wittyplay.LibdragonOptions())
	if got["parallel-n64-gfxplugin"] != "angrylion" || got["x"] != "y" {
		t.Errorf("%v", got)
	}
	mine := map[string]string{"x": "y"}
	if !maps.Equal(withFixed(mine, nil), mine) {
		t.Error("nothing fixed changed something")
	}
}
