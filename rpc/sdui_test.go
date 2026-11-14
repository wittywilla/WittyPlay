// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittyplay/cores"
)

// The consoles source: every console, ready ones first; only the ready ones on the Games
// page's row, and none when an administrator turned the row off; paged; each a tile that opens
// what this server plays.
func TestConsoleItems(t *testing.T) {
	dir := t.TempDir()
	// A core placed in the cores folder makes NES ready.
	if err := os.WriteFile(filepath.Join(dir, "fceumm_libretro"+coreExt()), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	lastCores.Store(&cores.Manager{Dir: dir, Extra: []string{}})
	t.Cleanup(func() { lastCores.Store(nil) })

	all, err := ConsoleItems(sdui.ItemsRequest{Source: sdui.Source{"type": "wittyplay.consoles", "only": "all"}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Items) < 20 || all.Items[0].ID != "nes" || all.Items[0].Subtitle != "On FCEUmm" || all.Items[0].Route != "/settings/games-support" {
		t.Fatalf("%+v", all.Items[:2])
	}
	for _, it := range all.Items[1:] {
		if len(it.Badges) == 0 {
			t.Errorf("%s isn't marked as not playing here yet", it.ID)
		}
	}
	ready, _ := ConsoleItems(sdui.ItemsRequest{Source: sdui.Source{"type": "wittyplay.consoles", "only": "ready"}, Limit: 100})
	if len(ready.Items) != 1 {
		t.Errorf("ready: %+v", ready.Items)
	}
	off, _ := ConsoleItems(sdui.ItemsRequest{Source: sdui.Source{"type": "wittyplay.consoles", "only": "ready"}, Settings: map[string]any{"showConsoles": false}})
	if len(off.Items) != 0 {
		t.Errorf("the row was turned off and still has %d", len(off.Items))
	}
	mine, _ := ConsoleItems(sdui.ItemsRequest{Source: sdui.Source{"type": "wittyplay.consoles", "only": "all"}, Settings: map[string]any{"onlyPlayable": true}})
	if len(mine.Items) != 1 {
		t.Errorf("a profile asking for what plays got %d", len(mine.Items))
	}
	page, _ := ConsoleItems(sdui.ItemsRequest{Source: sdui.Source{"type": "wittyplay.consoles", "only": "all"}, Offset: 2, Limit: 3})
	if len(page.Items) != 3 || !page.More || page.Total != len(all.Items) || page.Items[0].ID != all.Items[2].ID {
		t.Errorf("paging: %+v more=%v total=%d", page.Items, page.More, page.Total)
	}
}

func coreExt() string {
	switch runtime.GOOS {
	case "windows":
		return ".dll"
	case "darwin":
		return ".dylib"
	}
	return ".so"
}
