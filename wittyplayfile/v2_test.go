// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{200, 10, 10, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipOf(t *testing.T, members map[string][]byte, order ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if len(order) == 0 {
		order = sortedKeys(members)
	}
	for _, name := range order {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		w.Write(members[name])
	}
	zw.Close()
	return buf.Bytes()
}

func gameDoc(t *testing.T) *Doc {
	year := 1994
	d := &Doc{Version: Version, Language: "eng", Game: &About{
		Details:  &Details{Title: "Made Up Quest", Console: "snes", Year: &year, Genres: []string{"Adventure"}, Languages: []string{"ger"}},
		Identity: &Identity{FileName: "Made Up Quest (USA).sfc", Size: 1 << 20, CRC32: "1a2b3c4d"},
		Play:     &Play{Core: "snes9x", AspectRatio: "8:7", CoreOptions: map[string]string{"snes9x_overclock": "disabled"}},
		Cheats:   []Cheat{{Label: "Infinite lives", Code: "7E0DBE:09"}},
		Saves:    &Saves{Kind: "battery"},
	}}
	if _, err := d.SetImage("boxfront", pngBytes(t, 40, 56)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddScreenshot(pngBytes(t, 32, 28)); err != nil {
		t.Fatal(err)
	}
	return d
}

// A game's own file goes through an archive and comes back the same, pictures and all, and the
// same content gives the same bytes.
func TestArchiveRoundTrip(t *testing.T) {
	d := gameDoc(t)
	d.Stamp("Tester", "WittyPlay", "test", time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC))
	b, err := d.EncodeArchive()
	if err != nil {
		t.Fatal(err)
	}
	if !IsArchive(b) {
		t.Fatal("not an archive")
	}
	got, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Game.Details.Title != "Made Up Quest" || got.Game.Artwork.First("boxfront") != "images/boxfront.png" || len(got.Files) != 2 {
		t.Fatalf("read back wrongly: %+v files %v", got.Game, sortedKeys(got.Files))
	}
	if got.Language != "en" || got.Game.Details.Languages[0] != "de" {
		t.Errorf("tags are written in their usual form: %q %v", got.Language, got.Game.Details.Languages)
	}
	if got.Game.Cheats[0].ID == "" {
		t.Error("a cheat gets an id")
	}
	again, err := got.EncodeArchive()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, again) {
		t.Error("the same content wrote different bytes")
	}
	man, _, err := readArchive(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(man, []byte("{\n  \"wittyplay\": 2,")) {
		t.Errorf("manifest starts:\n%s", man[:40])
	}
	zr, _ := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if zr.File[0].Name != ManifestName || zr.File[0].Method != zip.Store {
		t.Error("manifest.json comes first, stored")
	}
}

// Without parts version 2 added, a file is written as version 1, so older readers keep reading it.
func TestLowestVersionWritten(t *testing.T) {
	d, err := Parse([]byte(`{"wittyplay":1,"default":{"picture":"case"}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := d.Encode()
	if !bytes.Contains(out, []byte(`"wittyplay": 1`)) {
		t.Fatalf("written as:\n%s", out)
	}
	d.Controls = &Controls{Default: &ControlSet{Turbo: &Turbo{Rate: 10}}}
	out, _ = d.Encode()
	if !bytes.Contains(out, []byte(`"wittyplay": 2`)) {
		t.Fatalf("controls need version 2:\n%s", out)
	}
	if _, err := Parse(out); err != nil {
		t.Fatal(err)
	}
}

// A version 2 file keeps saying 2 when what's left in it would fit version 1: a writer never
// lowers a file's version.
func TestVersionNeverLowered(t *testing.T) {
	d, err := Parse([]byte(`{"wittyplay":2,"default":{"picture":"case"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := d.Encode(); !bytes.Contains(out, []byte(`"wittyplay": 2`)) {
		t.Fatalf("written as:\n%s", out)
	}
}

// A newer file is read for what this version knows. Values it doesn't know (a new artwork role,
// button or hotkey action) and fields it doesn't know are kept, and the version stays.
func TestNewerFileKept(t *testing.T) {
	pic := pngBytes(t, 4, 4)
	in := map[string][]byte{
		"manifest.json": []byte(`{"wittyplay":3,"shiny":{"a":1},
			"game":{"details":{"title":"Later Quest","sparkle":true},
				"artwork":{"boxfront":"images/boxfront.png","hologram":"images/hologram.png"}},
			"controls":{"default":{"players":[{"player":1,"device":"wand","gamepad":{"a":"button:0","grip":"button:9"}}],
				"hotkeys":{"actions":{"teleport":{"keyboard":"F9"}}}}}}`),
		"images/boxfront.png": pic,
		"images/hologram.png": pic,
	}
	d, err := Parse(zipOf(t, in))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Newer() || d.Game.Details.Title != "Later Quest" || d.Game.Artwork.First("boxfront") != "images/boxfront.png" {
		t.Fatalf("read %+v", d.Game)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("a newer file's own values are refused: %v", err)
	}
	out, err := d.EncodeAny()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(back)
	for _, want := range []string{`"wittyplay":3`, `"shiny"`, `"sparkle"`, `"hologram"`, `"wand"`, `"grip"`, `"teleport"`} {
		if !bytes.Contains(m, []byte(want)) {
			t.Errorf("%s lost:\n%s", want, m)
		}
	}
	if len(back.Files) != 2 {
		t.Errorf("pictures %d", len(back.Files))
	}
	// The same values in a version 2 file are refused.
	in["manifest.json"] = bytes.Replace(in["manifest.json"], []byte(`"wittyplay":3`), []byte(`"wittyplay":2`), 1)
	if d, err := Parse(zipOf(t, in)); err == nil {
		var e *Error
		if err := d.Validate(); !errors.As(err, &e) || len(e.Problems) < 4 {
			t.Errorf("version 2 with unknown values: %v", err)
		}
	}
}

// A role holds one picture or a list, the first shown; one is still written as a plain name.
func TestSeveralPicturesARole(t *testing.T) {
	pic := pngBytes(t, 4, 4)
	d := &Doc{Version: Version}
	if _, err := d.SetImage("boxfront", pic); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(d)
	if !bytes.Contains(m, []byte(`"boxfront":"images/boxfront.png"`)) {
		t.Fatalf("one picture: %s", m)
	}
	n2, _ := d.AddImage("boxfront", pic)
	n3, _ := d.AddImage("boxfront", pic)
	if n2 != "images/boxfront-2.png" || n3 != "images/boxfront-3.png" {
		t.Fatalf("names %s %s", n2, n3)
	}
	if !d.ShowFirst("boxfront", n3) || d.Game.Artwork.First("boxfront") != n3 {
		t.Fatalf("show first: %v", d.Game.Artwork)
	}
	if _, err := d.SetImage("boxfront", pic); err != nil {
		t.Fatal(err)
	}
	// Setting replaces the one shown (boxfront-3) and keeps the others.
	if got := d.Game.Artwork["boxfront"]; len(got) != 3 || got[0] != "images/boxfront-4.png" || got[1] != "images/boxfront.png" || d.Files[n3] != nil {
		t.Fatalf("after set: %v", got)
	}
	if !d.RemoveImage("boxfront", n2) || len(d.Game.Artwork["boxfront"]) != 2 {
		t.Fatalf("remove: %v", d.Game.Artwork)
	}
	out, err := d.EncodeAny()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Game.Artwork["boxfront"]) != 2 || back.Game.Artwork.First("boxfront") != d.Game.Artwork.First("boxfront") {
		t.Fatalf("back %v", back.Game.Artwork)
	}
	for i := 0; i < MaxPicturesPerRole; i++ {
		d.AddImage("fanart", pic)
	}
	if _, err := d.AddImage("fanart", pic); err == nil {
		t.Errorf("more than %d pictures added", MaxPicturesPerRole)
	}
	d.Game.Artwork["logo"] = Pictures{"images/boxfront.png", "images/boxfront.png"}
	var e *Error
	if err := d.Validate(); !errors.As(err, &e) || !strings.Contains(err.Error(), "twice") {
		t.Errorf("a name twice: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"x":3}`), &Pictures{}); err == nil {
		t.Errorf("a number taken as pictures")
	}
}

// What a reader doesn't know, inside the parts version 2 added, comes out of a write as it went in.
func TestV2UnknownKept(t *testing.T) {
	in := `{"wittyplay":2,"game":{"details":{"title":"X","sparkle":1},"identity":{"crc32":"00000000","future":"yes"},
	  "play":{"core":"c","glow":[1]},"cheats":[{"label":"l","code":"AB","why":"k"}],"x-own":{"a":1}},
	  "controls":{"default":{"players":[{"player":1,"keyboard":{"a":"KeyX"},"chord":true}],"vibe":2},"later":3},
	  "authorship":{"author":"me","mood":"good"}}`
	d, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sparkle": 1`, `"future": "yes"`, `"glow"`, `"why": "k"`, `"x-own"`, `"chord": true`, `"vibe": 2`, `"later": 3`, `"mood": "good"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("lost %s in\n%s", want, out)
		}
	}
}

func problemsOf(t *testing.T, in string) []Problem {
	t.Helper()
	_, err := Parse([]byte(in))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("want a list of problems for %s, got %v", in, err)
	}
	return e.Problems
}

// Every problem in the parts version 2 added is found at once, each with where it is.
func TestV2Refused(t *testing.T) {
	ps := problemsOf(t, `{"wittyplay":2,"game":{"details":{"year":12,"console":"Bad Console"},"identity":{"crc32":"xyz","fileName":"../a"},
	  "artwork":{"boxfront":"https://example.org/a.png","poster":"images/a.png"},"screenshots":["../x.png"],
	  "play":{"rotation":45,"aspectRatio":"wide"},"cheats":[{"label":"l","code":"rm -rf /"}],"saves":{"kind":"cloud"}},
	  "controls":{"default":{"players":[{"player":9,"gamepad":{"a":"button:99"},"keyboard":{"fly":"KeyF"},"mouse":{"aim":"gun"}}],
	  "hotkeys":{"actions":{"hack":{"keyboard":"F1"},"saveState":{"keyboard":"Hyper+F2"}}},"turbo":{"rate":100}}},
	  "language":"klingonese","translations":{"xx-!!":{}}}`)
	var where []string
	for _, p := range ps {
		where = append(where, p.Where)
	}
	all := strings.Join(where, " ")
	for _, want := range []string{"game.details.year", "game.details.console", "game.identity.crc32", "game.identity.fileName",
		"game.artwork.boxfront", "game.artwork.poster", "game.screenshots[0]", "game.play.rotation", "game.play.aspectRatio",
		"game.cheats[0].code", "game.saves.kind", "controls.default.players[0].player", "controls.default.players[0].gamepad.a",
		"controls.default.players[0].keyboard.fly", "controls.default.players[0].mouse.aim", "controls.default.hotkeys.actions.hack",
		"controls.default.hotkeys.actions.saveState.keyboard", "controls.default.turbo.rate", "language", "translations.xx-!!"} {
		if !strings.Contains(" "+all+" ", " "+want+" ") {
			t.Errorf("no problem at %s; found %s", want, all)
		}
	}
	if _, err := Parse([]byte(`{"wittyplay":2,"game":{"details":{"year":"soon"}}}`)); err == nil || !strings.Contains(err.Error(), "year has the wrong kind") {
		t.Errorf("a wrong kind of value says where: %v", err)
	}
	if _, err := Parse([]byte(`{"wittyplay":2,"game":` + strings.Repeat(`{"a":`, 30) + `1` + strings.Repeat(`}`, 31))); err == nil {
		t.Error("deep nesting accepted")
	}
}

// An archive is refused whole for any of the ways one could be used to do harm.
func TestArchiveRefused(t *testing.T) {
	man := []byte(`{"wittyplay":2}`)
	pic := pngBytes(t, 4, 4)
	cases := map[string][]byte{
		"climbs out":      zipOf(t, map[string][]byte{ManifestName: man, "../evil.png": pic}),
		"absolute":        zipOf(t, map[string][]byte{ManifestName: man, "/etc/evil.png": pic}),
		"backslash":       zipOf(t, map[string][]byte{ManifestName: man, `images\a.png`: pic}),
		"no manifest":     zipOf(t, map[string][]byte{"images/a.png": pic}),
		"twice":           zipOf(t, map[string][]byte{ManifestName: man, "images/a.png": pic, "IMAGES/A.png": pic}),
		"wrong type":      zipOf(t, map[string][]byte{ManifestName: man, "images/a.jpg": pic}),
		"not a picture":   zipOf(t, map[string][]byte{ManifestName: man, "images/a.png": []byte("<script>")}),
		"script":          zipOf(t, map[string][]byte{ManifestName: man, "images/a.svg": []byte("<svg/>")}),
		"bad manifest":    zipOf(t, map[string][]byte{ManifestName: []byte(`[1]`)}),
		"names a missing": zipOf(t, map[string][]byte{ManifestName: []byte(`{"wittyplay":2,"game":{"artwork":{"logo":"images/logo.png"}}}`)}),
		"bomb":            zipOf(t, map[string][]byte{ManifestName: man, "notes/big.txt": bytes.Repeat([]byte{0}, 3<<20)}),
		"too big member":  zipOf(t, map[string][]byte{ManifestName: man, "other.bin": bytes.Repeat([]byte("ab"), MaxOther)}),
	}
	many := map[string][]byte{ManifestName: man}
	for i := 0; i < MaxMembers; i++ {
		many[fmt.Sprintf("x/%d.txt", i)] = []byte("x")
	}
	cases["too many"] = zipOf(t, many)
	cases["not a zip"] = append([]byte("PK\x03\x04"), bytes.Repeat([]byte{1}, 100)...)
	for name, b := range cases {
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Another member is kept, never used.
	d, err := Parse(zipOf(t, map[string][]byte{ManifestName: man, "notes/readme.txt": []byte("hi")}))
	if err != nil || string(d.Files["notes/readme.txt"]) != "hi" {
		t.Fatalf("other members are kept: %v", err)
	}
}

func TestControlsForConsole(t *testing.T) {
	d, err := Parse([]byte(`{"wittyplay":2,"controls":{
	  "default":{"players":[{"player":1,"keyboard":{"a":"KeyX","b":"KeyZ"},"gamepad":{"a":"button:1"}}],
	    "hotkeys":{"enable":{"gamepad":"button:8"},"actions":{"saveState":{"keyboard":"F2"},"menu":{"keyboard":"Escape"}}},"turbo":{"rate":10}},
	  "consoles":{"n64":{"players":[{"player":1,"keyboard":{"a":"KeyC"},"mouse":{"aim":"lstick"}},{"player":2,"device":"none"}],
	    "hotkeys":{"actions":{"saveState":{"keyboard":"F5","gamepad":"button:8+button:5"}}},"analog":{"deadzone":0.2}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s := d.Controls.For("n64")
	p1 := s.Player(1)
	if p1 == nil || p1.Keyboard["a"] != "KeyC" || p1.Keyboard["b"] != "KeyZ" || p1.Gamepad["a"] != "button:1" || p1.Mouse["aim"] != "lstick" {
		t.Fatalf("player 1 = %+v", p1)
	}
	if s.Player(2) == nil || s.Player(2).Device != "none" {
		t.Fatal("player 2 is the console's")
	}
	if s.Hotkeys.Actions["saveState"].Keyboard != "F5" || s.Hotkeys.Actions["menu"].Keyboard != "Escape" || s.Hotkeys.Enable == nil {
		t.Fatalf("hotkeys = %+v", s.Hotkeys)
	}
	if s.Turbo.Rate != 10 || *s.Analog.Deadzone != 0.2 {
		t.Fatal("turbo and analog")
	}
	if got := d.Controls.For("snes").Player(1).Keyboard["a"]; got != "KeyX" {
		t.Fatalf("another console takes the default: %q", got)
	}
}

func TestGamepadBindings(t *testing.T) {
	for _, s := range []string{"button:0", "axis:1-", "axis:5+", "axis:2+@-1", "hat:0:up", "hat:3:left"} {
		b, ok := ParseGamepad(s)
		if !ok || b.String() != s {
			t.Errorf("%s read as %+v (%v), written %s", s, b, ok, b.String())
		}
	}
	for _, s := range []string{"button:64", "axis:1", "hat:0:north", "button:-1", "key:A", ""} {
		if _, ok := ParseGamepad(s); ok {
			t.Errorf("%s accepted", s)
		}
	}
	if TargetID("b") != 0 || TargetID("a") != 8 || TargetID("lstick-left") != 16 || TargetID("rstick-down") != 23 || TargetID("z") != -1 {
		t.Error("targets are numbered as libretro numbers them")
	}
}

func TestLocalize(t *testing.T) {
	d, err := Parse([]byte(`{"wittyplay":2,"language":"en","game":{"details":{"title":"Made Up Quest","overview":"An adventure."},
	  "cheats":[{"id":"c1","label":"Infinite lives","code":"AB"}],"notes":"n"},
	  "translations":{"ger":{"details":{"overview":"Ein Abenteuer."},"cheats":{"c1":"Unendliche Leben"}},"pt_br":{"notes":"nota"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := d.Localize("de-AT")
	if g.Details.Overview != "Ein Abenteuer." || g.Details.Title != "Made Up Quest" || g.Cheats[0].Label != "Unendliche Leben" {
		t.Fatalf("de-AT: %+v", g.Details)
	}
	if d.Game.Details.Overview != "An adventure." {
		t.Fatal("localizing changed the file")
	}
	if g := d.Localize("fr"); g.Details.Overview != "An adventure." {
		t.Fatal("no French: its own text")
	}
	d.Tidy()
	if d.Translations["de"] == nil || d.Translations["pt-BR"] == nil {
		t.Fatalf("tags tidied: %v", sortedKeys(d.Translations))
	}
	if _, err := Parse([]byte(`{"wittyplay":2,"translations":{"de":{"cheats":{"nope":"x"}}}}`)); err == nil {
		t.Fatal("a translation of a cheat that isn't there")
	}
}

func TestIdentity(t *testing.T) {
	id := &Identity{Size: 4, CRC32: "AABBCCDD", MD5: "0123456789abcdef0123456789abcdef"}
	if v := Compare(id, Facts{Size: 4, CRC32: "aabbccdd"}); v != Same {
		t.Errorf("same crc: %s", v)
	}
	if v := Compare(id, Facts{Size: 5}); v != Other {
		t.Errorf("another size: %s", v)
	}
	if v := Compare(id, Facts{Size: 4, CRC32: "00000000"}); v != Other {
		t.Errorf("another crc: %s", v)
	}
	if v := Compare(id, Facts{Size: 4}); v != Unknown {
		t.Errorf("size only: %s", v)
	}
	if v := Compare(id, Facts{Size: 16, MoreCRC32: []string{"aabbccdd"}}); v != Same {
		t.Errorf("a header left out: %s", v)
	}
	if v := Compare(nil, Facts{}); v != Unknown {
		t.Error("no identity")
	}
	p := filepath.Join(t.TempDir(), "Game (USA).sfc")
	os.WriteFile(p, []byte("game"), 0o644)
	got, err := IdentityOf(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("game"))
	if got.CRC32 != fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte("game"))) || got.MD5 != hex.EncodeToString(sum[:]) || len(got.SHA1) != 40 ||
		got.Size != 4 || got.FileName != "Game (USA).sfc" {
		t.Fatalf("identity = %+v", got)
	}
	f, _ := Hash(p, got.Wanted()...)
	if Compare(got, f) != Same {
		t.Fatal("a file is its own dump")
	}
}

// The spec's example of a game's own file reads.
func TestSpecGameExample(t *testing.T) {
	dir := specDir(t)
	spec, err := os.ReadFile(dir + "/wittyplay-file.md")
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(spec, []byte("```json\n"))
	if len(parts) < 3 {
		t.Fatal("the spec has no game example")
	}
	ex := parts[2][:bytes.Index(parts[2], []byte("```"))]
	var man map[string]json.RawMessage
	if err := json.Unmarshal(ex, &man); err != nil {
		t.Fatal(err)
	}
	pic := pngBytes(t, 8, 8)
	b := zipOf(t, map[string][]byte{ManifestName: ex, "images/boxfront.png": pic, "images/screenshots/01.png": pic})
	d, err := Parse(b)
	if err != nil {
		t.Fatalf("the spec's game example doesn't read: %v", err)
	}
	if d.Game.Details.Title != "Made Up Quest" || d.Controls.For("snes").Player(1).Keyboard["a"] != "KeyX" {
		t.Fatal("read wrongly")
	}
}
