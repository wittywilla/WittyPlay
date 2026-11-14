// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/ipc"
)

type settingsResp struct {
	Resolved wittyplay.Resolved `json:"resolved"`
	Ignored  []wittyplay.Issue  `json:"ignored"`
	Limits   wittyplay.Limits   `json:"limits"`
}

func TestGameSettingsFollowTheirLevels(t *testing.T) {
	_, h := testApp(t, personHost(t, nil))
	put := func(level string, values map[string]any) *httptest.ResponseRecorder {
		return as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": level, "item": 7, "settings": values})
	}
	read := func(w *httptest.ResponseRecorder) settingsResp {
		var s settingsResp
		decode(t, w, &s)
		return s
	}
	if w := put("person", map[string]any{"display.scanlines": 0.2}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := put("console", map[string]any{"display.scanlines": 0.3}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := put("game", map[string]any{"display.scanlines": 0.4, "made.up": 1, "display.curve": 42})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if s := read(w); len(s.Ignored) != 2 {
		t.Errorf("what wasn't used should be listed: %+v", s.Ignored)
	}
	s := read(as(t, h, "GET", "/games/settings?item=7", alex, nil))
	if s.Resolved.Values["display.scanlines"] != 0.4 || s.Resolved.Sources["display.scanlines"] != wittyplay.LevelGame {
		t.Errorf("got %v from %v", s.Resolved.Values["display.scanlines"], s.Resolved.Sources["display.scanlines"])
	}
	// Taking the game's away shows the console's.
	if w := put("game", map[string]any{"display.scanlines": nil}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v := read(as(t, h, "GET", "/games/settings?item=7", alex, nil)).Resolved.Values["display.scanlines"]; v != 0.3 {
		t.Errorf("after removing it: %v", v)
	}
	// Nothing of one person's is another's.
	if v := read(as(t, h, "GET", "/games/settings?item=7", sam, nil)).Resolved.Values["display.scanlines"]; v != 0.0 {
		t.Errorf("someone else's setting came through: %v", v)
	}
	// Resetting clears a level.
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "console", "item": 7, "reset": true}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v := read(as(t, h, "GET", "/games/settings?console=snes", alex, nil)).Resolved.Values["display.scanlines"]; v != 0.2 {
		t.Errorf("after reset: %v", v)
	}
	// A game or a console that isn't there, or isn't theirs to see.
	for _, path := range []string{"/games/settings?item=8", "/games/settings?item=12", "/games/settings?console=nosuch"} {
		if w := as(t, h, "GET", path, kit, nil); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "game", "settings": map[string]any{}}); w.Code != 400 {
		t.Errorf("a game level with no game: %d", w.Code)
	}
}

func TestOnlyAnAdminSetsWhatEveryoneStartsWith(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "server", "settings": map[string]any{"display.scanlines": 0.9}}); w.Code != 403 {
		t.Fatalf("a person set a server default: %d", w.Code)
	}
	if w := as(t, h, "PUT", "/games/settings", owner, map[string]any{"level": "server", "settings": map[string]any{"display.scanlines": 0.5, "osd.on": true}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := as(t, h, "PUT", "/games/settings", owner, map[string]any{"level": "serverConsole", "console": "snes", "settings": map[string]any{"display.glow": 0.25}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var s settingsResp
	decode(t, as(t, h, "GET", "/games/settings?item=7", alex, nil), &s)
	if s.Resolved.Values["display.scanlines"] != 0.5 || s.Resolved.Sources["display.scanlines"] != wittyplay.LevelServer || s.Resolved.Values["display.glow"] != 0.25 {
		t.Fatalf("got %+v", s.Resolved)
	}
	// And anyone can still choose otherwise for themselves.
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "person", "settings": map[string]any{"display.scanlines": 0.1}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	decode(t, as(t, h, "GET", "/games/settings?item=7", alex, nil), &s)
	if s.Resolved.Values["display.scanlines"] != 0.1 {
		t.Fatalf("got %v", s.Resolved.Values["display.scanlines"])
	}
	// What the server allows comes from WittyPlay's own settings.
	if err := a.SetConfig(t.Context(), "gamingMaxSpeed", "3"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetConfig(t.Context(), "gamingRunAhead", false); err != nil {
		t.Fatal(err)
	}
	decode(t, as(t, h, "GET", "/games/settings", alex, nil), &s)
	if s.Limits.MaxSpeed != 3 || s.Limits.RunAhead {
		t.Errorf("limits: %+v", s.Limits)
	}
}

func TestASettingsFileGoesThroughTheAPI(t *testing.T) {
	_, h := testApp(t, personHost(t, nil))
	w := as(t, h, "POST", "/games/settings/validate", alex, map[string]any{"item": 7, "document": "{\n \"settings\": {\n  \"display.curve\": 42\n }\n}"})
	var v struct {
		OK     bool                   `json:"ok"`
		Report wittyplay.CustomReport `json:"report"`
	}
	decode(t, w, &v)
	if v.OK || len(v.Report.Errors) != 1 || v.Report.Errors[0].Line != 3 {
		t.Fatalf("%+v", v)
	}
	// A bad file changes nothing.
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "game", "item": 7, "settings": map[string]any{"display.curve": 0.5}}); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if w := as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "game", "item": 7, "document": `{"settings":{"display.curve":42}}`}); w.Code != 422 {
		t.Fatalf("a bad file was saved: %d %s", w.Code, w.Body)
	}
	var s settingsResp
	decode(t, as(t, h, "GET", "/games/settings?item=7", alex, nil), &s)
	if s.Resolved.Values["display.curve"] != 0.5 {
		t.Fatalf("a bad file changed something: %v", s.Resolved.Values["display.curve"])
	}
	// A good one replaces the level.
	w = as(t, h, "PUT", "/games/settings", alex, map[string]any{"level": "game", "item": 7, "document": `{"wittyplay":1,"settings":{"display.glow":0.3},"exec":"rm -rf /"}`})
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	decode(t, w, &s)
	if s.Resolved.Values["display.glow"] != 0.3 || s.Resolved.Values["display.curve"] != 0.0 {
		t.Fatalf("got %+v", s.Resolved.Values)
	}
	ignored := ""
	for _, i := range s.Ignored {
		ignored += i.Path + " "
	}
	if !strings.Contains(ignored, "exec") {
		t.Fatalf("what was ignored wasn't said: %v", s.Ignored)
	}
	// Too large is refused without being read.
	huge := strings.Repeat(" ", wittyplay.MaxCustomSize*3)
	if w := as(t, h, "POST", "/games/settings/validate", alex, map[string]any{"item": 7, "document": huge}); w.Code == 200 {
		t.Fatalf("an oversized file was read: %d", w.Code)
	}
}

func TestSettingsSchemaAndCoreOptions(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	if w := as(t, h, "GET", "/games/settings/schema", alex, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"maxSlot"`) || !strings.Contains(w.Body.String(), `"presets"`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// With no core for the console, nothing is known, and nothing breaks.
	var co struct {
		Core    string                 `json:"core"`
		Known   bool                   `json:"known"`
		Options []wittyplay.OptionInfo `json:"options"`
	}
	decode(t, as(t, h, "GET", "/games/7/core-options", alex, nil), &co)
	if co.Known || co.Options == nil {
		t.Errorf("%+v", co)
	}
	// What a core said it offers is kept, and shown for its games.
	old := gameCore
	gameCore = func(context.Context, *App, string, int64) (string, string, bool) { return "snes9x", "", true }
	t.Cleanup(func() { gameCore = old })
	a.keepCoreOptions("snes9x", []ipc.Option{{Key: "snes9x_region", Label: "Region", Choices: []string{"auto", "pal"}, Default: "auto"}})
	decode(t, as(t, h, "GET", "/games/7/core-options", alex, nil), &co)
	if co.Core != "snes9x" || !co.Known || len(co.Options) != 1 {
		t.Errorf("%+v", co)
	}
	if w := as(t, h, "GET", "/games/12/core-options", kit, nil); w.Code != 404 {
		t.Errorf("a game the profile may not see: %d", w.Code)
	}
}
