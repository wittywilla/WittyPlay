// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

func sidecarFor(t *testing.T, gamePath string, size int64, title string) {
	t.Helper()
	d := &wittyplayfile.Doc{Version: wittyplayfile.Version, Default: &wittyplayfile.Look{Picture: wittyplayfile.PictureCase},
		Game: &wittyplayfile.About{
			Details:  &wittyplayfile.Details{Title: title, Overview: "From the .wittyplay.", Genres: []string{"Puzzle"}},
			Identity: &wittyplayfile.Identity{Size: size},
		},
		Controls: &wittyplayfile.Controls{Default: &wittyplayfile.ControlSet{Players: []wittyplayfile.PlayerControls{
			{Player: 1, Keyboard: map[string]string{"a": "KeyM"}, Gamepad: map[string]string{"b": "button:3"}}}}},
	}
	if _, err := d.SetImage("boxfront", pngBytes(t)); err != nil {
		t.Fatal(err)
	}
	b, err := d.EncodeArchive()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(gamePath, filepath.Ext(gamePath))+".wittyplay", b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A scan reads a game's own .wittyplay: details and box art over everything else, its look, and
// its controls as the game's defaults. One made for another dump is flagged and not used.
func TestScanReadsAGamesWittyplay(t *testing.T) {
	f := newGameFixture(t)
	p := f.game(t, 7, "Made Up Quest (USA).sfc")
	st, _ := os.Stat(p)
	sidecarFor(t, p, st.Size(), "Made Up Quest: Owner's Cut")
	out := f.describe(t, nil, 7)
	if len(out.Items) != 1 || out.Items[0].Title != "Made Up Quest: Owner's Cut" || out.Items[0].Overview != "From the .wittyplay." || out.Items[0].Poster == "" {
		t.Fatalf("described %+v", out.Items)
	}
	if s := f.a.sidecarStateOf(f.ctx, 7); s == nil || s.Verdict == string(wittyplayfile.Other) {
		t.Fatalf("state %+v", s)
	}
	if l, _ := f.a.gamePictureFrom(f.ctx, 7, pictureByFile); l.Picture != wittyplayfile.PictureCase {
		t.Fatalf("look %+v", l)
	}
	w := callRaw(t, f.h, "GET", "/consoles/snes/controls?game=7", 3, false, nil)
	var c struct {
		Defaults struct {
			Keyboard map[string]string `json:"keyboard"`
			Gamepad  map[string]struct {
				B *int `json:"b"`
			} `json:"gamepad"`
		} `json:"defaults"`
		FromFile *wittyplayfile.ControlSet `json:"fromFile"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &c) != nil {
		t.Fatalf("controls: %d %s", w.Code, w.Body)
	}
	if c.Defaults.Keyboard["8"] != "KeyM" || c.Defaults.Gamepad["0"].B == nil || *c.Defaults.Gamepad["0"].B != 3 || c.FromFile == nil {
		t.Fatalf("the file's controls aren't the defaults: %s", w.Body)
	}

	// Another dump: its size differs.
	q := f.game(t, 8, "Other Quest (Europe).sfc")
	sidecarFor(t, q, 999999, "Wrong Dump Title")
	out = f.describe(t, nil, 8)
	if out.Items[0].Title == "Wrong Dump Title" {
		t.Fatal("a file for another dump was used")
	}
	if s := f.a.sidecarStateOf(f.ctx, 8); s == nil || s.Verdict != string(wittyplayfile.Other) || s.Problem == "" {
		t.Fatalf("not flagged: %+v", s)
	}

	// A broken one is noted, and the game is described as if it had none.
	r := f.game(t, 9, "Broken (USA).sfc")
	_ = os.WriteFile(strings.TrimSuffix(r, ".sfc")+".wittyplay", []byte("PK\x03\x04garbage"), 0o644)
	out = f.describe(t, nil, 9)
	if s := f.a.sidecarStateOf(f.ctx, 9); s == nil || s.Problem == "" || len(out.Items) != 1 {
		t.Fatalf("broken: %+v", s)
	}
}

func waitConversion(t *testing.T) convertStatus {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s := conversions.snapshot(); s.State != "running" {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the conversion didn't finish")
	return convertStatus{}
}

// The setup's conversion: counts, gather into .wittyplay files, write them back out.
func TestConvertAPI(t *testing.T) {
	f := newGameFixture(t)
	p := f.game(t, 7, "Made Up Quest (USA).sfc")
	f.game(t, 8, "Lonely (USA).sfc")
	_ = os.WriteFile(filepath.Join(f.dir, "gamelist.xml"), []byte(`<gameList><game><path>./Made Up Quest (USA).sfc</path><name>Made Up Quest</name><desc>Gathered.</desc><genre>Puzzle</genre></game></gameList>`), 0o644)
	_ = os.MkdirAll(filepath.Join(f.dir, "media", "box2dfront"), 0o755)
	_ = os.WriteFile(filepath.Join(f.dir, "media", "box2dfront", "Made Up Quest (USA).png"), pngBytes(t), 0o644)
	f.describe(t, nil, 7, 8)

	if w := callRaw(t, f.h, "GET", "/games/wittyplay/convert", 3, false, nil); w.Code != 403 {
		t.Fatalf("a viewer saw the conversion: %d", w.Code)
	}
	w := callRaw(t, f.h, "GET", "/games/wittyplay/convert", 1, true, nil)
	var counts struct {
		Games, WithWittyplay, WithStandard, ToGather, ToGatherLibrary, ToWriteOut int
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &counts) != nil || counts.Games != 2 || counts.ToGather != 1 || counts.ToGatherLibrary != 2 || counts.WithWittyplay != 0 {
		t.Fatalf("counts: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "POST", "/games/wittyplay/convert", 1, true, []byte(`{"direction":"sideways"}`)); w.Code != 400 {
		t.Fatalf("a bad direction: %d", w.Code)
	}
	if w := callRaw(t, f.h, "POST", "/games/wittyplay/convert", 1, true, []byte(`{"direction":"gather"}`)); w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	s := waitConversion(t)
	if s.State != "done" || s.Total != 2 || s.Written != 1 || s.Skipped != 1 || s.Failed != 0 {
		t.Fatalf("gather: %+v", s)
	}
	d, err := readSidecar(filepath.Join(f.dir, "Made Up Quest (USA).wittyplay"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Game.Details.Title != "Made Up Quest" || d.Game.Details.Console != "snes" || d.Game.Artwork["boxfront"] == "" || d.Game.Identity.SHA1 == "" || d.Authorship.Tool.Name != "WittyPlay" {
		t.Fatalf("gathered %+v", d.Game)
	}
	if v := wittyplayfile.Compare(d.Game.Identity, f.a.gameFacts(f.ctx, 7, p, d.Game.Identity)); v != wittyplayfile.Same {
		t.Fatalf("its identity is its own game's: %s", v)
	}

	// Write out: into a folder with nothing beside the game but its .wittyplay.
	_ = os.Remove(filepath.Join(f.dir, "gamelist.xml"))
	_ = os.RemoveAll(filepath.Join(f.dir, "media"))
	if w := callRaw(t, f.h, "POST", "/games/wittyplay/convert", 1, true, []byte(`{"direction":"writeout"}`)); w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	if s := waitConversion(t); s.State != "done" || s.Written != 1 {
		t.Fatalf("writeout: %+v", s)
	}
	gl, _ := os.ReadFile(filepath.Join(f.dir, "gamelist.xml"))
	if !strings.Contains(string(gl), "<desc>Gathered.</desc>") {
		t.Fatalf("gamelist.xml:\n%s", gl)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "media", "box2dfront", "Made Up Quest (USA).png")); err != nil {
		t.Fatal("box art wasn't written out")
	}
	if w := callRaw(t, f.h, "GET", "/games/wittyplay/convert/status", 1, true, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"direction":"writeout"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
}

// The editor: a draft from beside the game, saving with a picture, the picture, sharing, and an
// import for another dump refused.
func TestSidecarEditorAPI(t *testing.T) {
	f := newGameFixture(t)
	p := f.game(t, 7, "Made Up Quest (USA).sfc")
	f.describe(t, nil, 7)
	if w := callRaw(t, f.h, "GET", "/games/7/wittyplay", 3, false, nil); w.Code != 403 {
		t.Fatalf("a viewer opened the editor: %d", w.Code)
	}
	w := callRaw(t, f.h, "GET", "/games/7/wittyplay", 1, true, nil)
	var v struct {
		Exists, Draft bool
		Doc           json.RawMessage
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Exists || !v.Draft {
		t.Fatalf("draft: %d %s", w.Code, w.Body)
	}
	body, _ := json.Marshal(map[string]any{
		"doc":    json.RawMessage(`{"wittyplay":2,"game":{"details":{"title":"Edited","year":1994},"cheats":[{"label":"Lives","code":"7E0DBE:09"}]}}`),
		"images": map[string]string{"boxfront": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes(t))},
	})
	if w := callRaw(t, f.h, "PUT", "/games/7/wittyplay", 1, true, body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	bad, _ := json.Marshal(map[string]any{"doc": json.RawMessage(`{"wittyplay":2,"game":{"details":{"year":12},"play":{"rotation":45}}}`)})
	w = callRaw(t, f.h, "PUT", "/games/7/wittyplay", 1, true, bad)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "game.details.year") || !strings.Contains(w.Body.String(), "game.play.rotation") {
		t.Fatalf("every problem comes back: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "GET", "/games/7/wittyplay/picture?name=images/boxfront.png", 1, true, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("picture: %d", w.Code)
	}
	if w := callRaw(t, f.h, "GET", "/games/7/wittyplay/picture?name=../../etc/passwd", 1, true, nil); w.Code != 404 {
		t.Fatalf("a name outside: %d", w.Code)
	}
	w = callRaw(t, f.h, "GET", "/games/7/wittyplay/download", 1, true, nil)
	shared, err := wittyplayfile.Parse(w.Body.Bytes())
	if w.Code != 200 || err != nil || shared.Game.Details.Title != "Edited" || !strings.Contains(w.Header().Get("Content-Disposition"), "Made Up Quest (USA).wittyplay") {
		t.Fatalf("download: %d %v", w.Code, err)
	}
	other := &wittyplayfile.Doc{Version: 2, Game: &wittyplayfile.About{Details: &wittyplayfile.Details{Title: "Other"}, Identity: &wittyplayfile.Identity{Size: 123456789}}}
	ob, _ := other.EncodeAny()
	if w := callRaw(t, f.h, "POST", "/games/7/wittyplay/import", 1, true, ob); w.Code != 409 {
		t.Fatalf("another dump imported: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "POST", "/games/7/wittyplay/writeout", 1, true, []byte(`{}`)); w.Code != 200 || !strings.Contains(w.Body.String(), "gamelist.xml") {
		t.Fatalf("writeout: %d %s", w.Code, w.Body)
	}
	if ents, _ := os.ReadDir(filepath.Dir(p)); slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasSuffix(e.Name(), ".tmp") }) {
		t.Fatal("a temporary file was left behind")
	}
}
