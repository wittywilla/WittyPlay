// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// Every row says enough for a client to lay out its games and build its game menu.
func TestEveryConsoleRowIsComplete(t *testing.T) {
	eras := []string{eraFirst, era8Bit, era16Bit, eraEarly3D, eraDisc, eraPocket}
	looks := []string{lookClean, lookScanlines, lookLCD}
	for id, row := range consoleTraitsTable {
		if !slices.Contains(eras, row.Era) {
			t.Errorf("%s: era %q", id, row.Era)
		}
		if row.Media != mediaCart && row.Media != mediaPocket && row.Media != mediaDisc {
			t.Errorf("%s: media %q", id, row.Media)
		}
		// No end year is a console still made: the Switch.
		if row.Launched < 1970 || (row.Ended != 0 && row.Ended < row.Launched) || (row.Ended == 0 && id != "switch" && id != "switch2") {
			t.Errorf("%s: years %d to %d", id, row.Launched, row.Ended)
		}
		c := row.Caps
		if c.Input.Ports < 1 || c.Screens.Count < 1 || c.Picture.Aspect == "" || !slices.Contains(looks, c.Picture.Look) {
			t.Errorf("%s: %+v", id, c)
		}
		if c.Input.Multitap != 0 && c.Input.Multitap <= c.Input.Ports {
			t.Errorf("%s: a multitap with no more players than ports", id)
		}
		if c.Screens.Count == 2 {
			if c.Screens.Touch == 0 || len(c.Screens.Options) == 0 {
				t.Errorf("%s: two screens with no touch screen or no way to lay them out", id)
			}
			for core, o := range c.Screens.Options {
				for _, l := range c.Screens.Layouts {
					if _, ok := o.Values[l]; !ok || o.Layout == "" {
						t.Errorf("%s/%s: no value for layout %s", id, core, l)
					}
				}
			}
		}
	}
}

// The consoles the owner chose: 8-bit, 16-bit and the handhelds whose states are small enough to
// keep 20 seconds of in the memory a server allows by default. Not the 3D and disc consoles.
func TestRewindIsOnByDefaultForTheOlderConsoles(t *testing.T) {
	for _, id := range []string{"nes", "sms", "snes", "md", "pce", "gb", "gba", "gg", "a2600", "a7800"} {
		if !consoleRewinds(id) {
			t.Errorf("%s should rewind by default", id)
		}
	}
	for _, id := range []string{"n64", "ps1", "ps2", "dc", "nds", "3ds", "psp", "made-up"} {
		if consoleRewinds(id) {
			t.Errorf("%s shouldn't rewind by default", id)
		}
	}
}

// options.go asks here which consoles rewind by default.
func TestTheRewindHookIsSet(t *testing.T) {
	if !rewindByDefault("nes") || rewindByDefault("ps2") {
		t.Error("options.go's rewindByDefault isn't the consoles' traits")
	}
}

// With the module here, every console it knows has a row.
func TestEveryConsoleTheModuleKnowsHasARow(t *testing.T) {
	for _, c := range wittyplay.All() {
		if c.Hidden || c.ID == wittyplay.TestConsole || traitsFor(c) != nil {
			continue
		}
		t.Errorf("no row for %s (%s)", c.ID, c.Name)
	}
}

// Every console with a row has a case and a cartridge (or disc) to draw, of a known kind and shape.
func TestEveryConsoleHasACaseAndACartridge(t *testing.T) {
	cases := []string{"box", "clamshell", "jewel", "keep"}
	carts := []string{"cart", "card", "disc"}
	for id, row := range consoleTraitsTable {
		l, ok := shelfLooks[id]
		if !ok {
			t.Errorf("%s has no case or cartridge", id)
			continue
		}
		if !slices.Contains(cases, l.Case.Kind) || !slices.Contains(carts, l.Cart.Kind) {
			t.Errorf("%s: %+v", id, l)
		}
		for _, sh := range []ShelfShape{l.Case, l.Cart} {
			if sh.Aspect < 0.4 || sh.Aspect > 2 || sh.Height <= 0 || sh.Height > 1 {
				t.Errorf("%s: shape %+v", id, sh)
			}
		}
		if (row.Media == mediaDisc) != (l.Cart.Kind == "disc") && id != "psp" {
			t.Errorf("%s: a disc console's games are discs, and only theirs", id)
		}
	}
}

// GET /games/consoles: the apps read it, so its shape stays the server's. Everyone sees whether a
// console plays; only an administrator sees the custom cores and the defaults.
func TestTheConsolesList(t *testing.T) {
	a, h, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A card"})
	putCores(t, a, "fceumm")
	ph.counts["nes"] = 3
	ph.items[7] = service.Item{ID: 7, Library: 4, Kind: "game", Facets: map[string]string{"console": "nes"}}
	ph.items[8] = service.Item{ID: 8, Library: 5, Kind: "game", Facets: map[string]string{"console": "nes"}}
	if err := a.SetConfig(context.Background(), settingCores, map[string]string{"nes": "fceumm"}); err != nil {
		t.Fatal(err)
	}
	var raw []map[string]any
	w := asWho(t, h, "GET", "/games/consoles", otherID, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &raw) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var nes map[string]any
	for _, c := range raw {
		if c["id"] == "nes" {
			nes = c
		}
		if c["id"] == wittyplay.TestConsole {
			t.Error("the test console is listed")
		}
	}
	for _, k := range []string{"id", "name", "installed", "ready", "pad", "coreSupport", "coreAbout", "era", "capabilities", "shelf", "logoName", "totalGames"} {
		if _, ok := nes[k]; !ok {
			t.Errorf("nes has no %s: %v", k, nes)
		}
	}
	if nes["ready"] != true || nes["playsOn"] != "fceumm" || nes["totalGames"] != float64(3) || nes["defaultCore"] != nil {
		t.Errorf("nes: %v", nes)
	}
	// An administrator sees the default; one library's games are counted.
	var list []ConsoleView
	_ = json.Unmarshal(asWho(t, h, "GET", "/games/consoles?library=4", adminID, nil).Body.Bytes(), &list)
	for _, c := range list {
		if c.ID == "nes" && (c.DefaultCore != "fceumm" || c.Count != 1 || c.TotalGames != 0) {
			t.Errorf("nes for an administrator, library 4: %+v", c)
		}
		if c.ID == "snes" && (c.Ready || c.Reason != "core") {
			t.Errorf("snes with no core: %+v", c)
		}
	}
	// How a client connects, and the emulators for a game: none set up here.
	if w := asWho(t, h, "GET", "/games/modes?client=browser", otherID, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"client":"browser"`) || !strings.Contains(w.Body.String(), `"modes":[`) {
		t.Errorf("modes: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "GET", "/games/modes?client=anything", otherID, nil); !strings.Contains(w.Body.String(), `"client":"native"`) {
		t.Errorf("an app: %s", w.Body)
	}
	it := ph.items[7]
	id := otherID
	id.Item = &it
	if w := asWho(t, h, "GET", "/games/7/emulators", id, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"emulators":[]}` {
		t.Errorf("emulators: %d %s", w.Code, w.Body)
	}
	if w := asWho(t, h, "GET", "/games/99/emulators", otherID, nil); w.Code != 404 {
		t.Errorf("a game nobody may see: %d", w.Code)
	}
}

// A console no core plays, and nothing set up to play it, says what it needs.
func TestStandaloneWords(t *testing.T) {
	xbox := wittyplay.ByID("xbox")
	if xbox == nil {
		t.Skip("no Xbox in the table")
	}
	if w := StandaloneWords(xbox, nil); !strings.Contains(w, "Standalone emulators") {
		t.Errorf("%q", w)
	}
	if w := StandaloneWords(xbox, []rpc.Offer{{ID: "xemu", Why: "It needs its BIOS."}}); w != "It needs its BIOS." {
		t.Errorf("%q", w)
	}
	if e := ReadyEmulator([]rpc.Offer{{ID: "a"}, {ID: "b", Ready: true}}); e == nil || e.ID != "b" {
		t.Errorf("%+v", e)
	}
	v := ConsoleView{Console: *xbox}
	applyStandalone(&v, []rpc.Offer{{ID: "xemu", Ready: true}})
	if !v.Ready || v.Standalone != "xemu" {
		t.Errorf("%+v", v)
	}
}
