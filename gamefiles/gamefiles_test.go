// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

func pic(t *testing.T, w int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, w))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func put(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

const gamelist = `<?xml version="1.0"?>
<gameList>
	<provider><System>Super</System></provider>
	<game id="7">
		<path>./Made Up Quest (USA).sfc</path>
		<name>Made Up Quest</name>
		<desc>An adventure.</desc>
		<image>./media/screenshot/Made Up Quest (USA).png</image>
		<thumbnail>./images/Made Up Quest (USA)-thumb.png</thumbnail>
		<marquee>../../outside.png</marquee>
		<rating>0.8</rating>
		<releasedate>19940815T000000</releasedate>
		<developer>Example Studio</developer>
		<publisher>Example Publisher</publisher>
		<genre>Adventure, Puzzle</genre>
		<players>1-2</players>
		<region>us</region>
		<md5>0123456789ABCDEF0123456789ABCDEF</md5>
		<playcount>3</playcount>
	</game>
	<game><path>./Other.sfc</path><name>Other</name></game>
</gameList>
`

func TestGatherAndWriteOut(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "Made Up Quest (USA).sfc")
	put(t, game, []byte("rom"))
	put(t, filepath.Join(dir, "gamelist.xml"), []byte(gamelist))
	put(t, filepath.Join(dir, "media", "screenshot", "Made Up Quest (USA).png"), pic(t, 8))
	put(t, filepath.Join(dir, "images", "Made Up Quest (USA)-thumb.png"), pic(t, 9))
	put(t, filepath.Join(dir, "media", "box2dback", "Made Up Quest (USA).png"), pic(t, 10))
	put(t, filepath.Join(filepath.Dir(dir), "outside.png"), pic(t, 4)) // named by ../../, never read
	put(t, filepath.Join(dir, "Made Up Quest (USA).rmp"), []byte("input_player1_btn_a = \"0\"\ninput_player1_btn_b = \"8\"\ninput_libretro_device_p2 = \"0\"\n"))
	put(t, filepath.Join(dir, "Made Up Quest (USA).cfg"), []byte("video_shader = \"x\"\ninput_player1_a = \"x\"\ninput_player1_b_btn = \"0\"\ninput_player1_l_x_minus_axis = \"-0\"\ninput_save_state = \"f2\"\ninput_enable_hotkey_btn = \"8\"\ninput_analog_deadzone = \"0.150000\"\ninput_turbo_period = \"6\"\n"))

	if !Has(game) {
		t.Fatal("Has")
	}
	d, found, err := Gather(game)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if !found.Gamelist || !found.Remap || !found.Config {
		t.Fatalf("found %+v", found)
	}
	m := d.Game.Details
	if m.Title != "Made Up Quest" || m.Overview != "An adventure." || *m.Year != 1994 || m.ReleaseDate != "1994-08-15" || *m.Rating != 8 ||
		len(m.Genres) != 2 || m.Players.Max != 2 || m.Developer != "Example Studio" || d.Game.Identity.MD5 != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("details %+v", m)
	}
	if len(d.Game.Artwork["boxfront"]) == 0 || len(d.Game.Artwork["boxback"]) == 0 || len(d.Game.Screenshots) != 1 || len(d.Game.Artwork["logo"]) > 0 {
		t.Fatalf("pictures %v %v (a picture named outside the folder is never read)", d.Game.Artwork, d.Game.Screenshots)
	}
	set := d.Controls.For("snes")
	p1 := set.Player(1)
	if p1.Remap["a"] != "b" || p1.Remap["b"] != "a" || p1.Keyboard["a"] != "KeyX" || p1.Gamepad["b"] != "button:0" || p1.Gamepad["lstick-left"] != "axis:0-" {
		t.Fatalf("player 1 %+v", p1)
	}
	if set.Player(2).Device != "none" || set.Hotkeys.Actions["saveState"].Keyboard != "F2" || set.Hotkeys.Enable.Gamepad != "button:8" ||
		*set.Analog.Deadzone != 0.15 || set.Turbo.Rate != 10 {
		t.Fatalf("set %+v", set)
	}
	// What was gathered is a good .wittyplay.
	b, err := d.EncodeAny()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wittyplayfile.Parse(b); err != nil {
		t.Fatal(err)
	}

	// Written out to an empty folder, it reads back the same.
	out := t.TempDir()
	game2 := filepath.Join(out, "Made Up Quest (USA).sfc")
	put(t, game2, []byte("rom"))
	w, err := WriteOut(game2, d, "snes", Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"media/box2dfront/Made Up Quest (USA).png", "media/box2dback/Made Up Quest (USA).png", "Made Up Quest (USA).nfo", "gamelist.xml", "Made Up Quest (USA).rmp", "Made Up Quest (USA).cfg"} {
		if !contains(w.Files, want) {
			t.Errorf("didn't write %s: %+v", want, w)
		}
	}
	back, _, err := Gather(game2)
	if err != nil || back == nil {
		t.Fatal(err)
	}
	bm := back.Game.Details
	if bm.Title != m.Title || bm.Overview != m.Overview || *bm.Year != 1994 || *bm.Rating != 8 || bm.Players.Max != 2 || bm.Players.Min != 1 {
		t.Fatalf("back %+v", bm)
	}
	bs := back.Controls.For("snes")
	if bs.Player(1).Remap["a"] != "b" || bs.Player(1).Keyboard["a"] != "KeyX" || bs.Hotkeys.Actions["saveState"].Keyboard != "F2" || bs.Turbo.Rate != 10 {
		t.Fatalf("controls back %+v", bs.Player(1))
	}
	if len(back.Game.Artwork["boxback"]) == 0 || len(back.Game.Screenshots) != 1 {
		t.Fatalf("pictures back %v", back.Game.Artwork)
	}

	// Into the first folder again: the other entries, unknown elements and other lines stay.
	if _, err := WriteOut(game, d, "snes", Options{}); err != nil {
		t.Fatal(err)
	}
	gl, _ := os.ReadFile(filepath.Join(dir, "gamelist.xml"))
	for _, want := range []string{"<playcount>3</playcount>", "<name>Other</name>", "<System>Super</System>", `id="7"`} {
		if !strings.Contains(string(gl), want) {
			t.Errorf("gamelist lost %s:\n%s", want, gl)
		}
	}
	if strings.Count(string(gl), "Made Up Quest (USA).sfc") != 1 {
		t.Errorf("the game's entry is updated, not added again:\n%s", gl)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, "Made Up Quest (USA).cfg"))
	if !strings.Contains(string(cfg), `video_shader = "x"`) {
		t.Errorf("other lines are kept:\n%s", cfg)
	}
	thumb, _ := os.ReadFile(filepath.Join(dir, "images", "Made Up Quest (USA)-thumb.png"))
	if !bytes.Equal(thumb, pic(t, 9)) {
		t.Error("a picture that was there is kept")
	}
}

func TestNothingBeside(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "Lonely.sfc")
	put(t, game, []byte("rom"))
	if Has(game) {
		t.Fatal("Has")
	}
	d, found, err := Gather(game)
	if d != nil || found.Any() || err != nil {
		t.Fatal("found something")
	}
}

// A gamelist.xml that isn't one, or names pictures outside the folder, gives nothing harmful.
func TestHostileFiles(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "G.sfc")
	put(t, game, []byte("rom"))
	put(t, filepath.Join(dir, "gamelist.xml"), []byte(`<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]><gameList><game><path>./G.sfc</path><name>&e;</name><image>C:\Windows\x.png</image><thumbnail>/etc/x.png</thumbnail></game></gameList>`))
	put(t, filepath.Join(dir, "G.nfo"), []byte(`<game><title>`+strings.Repeat("x", 300)+`</title></game>`))
	d, _, _ := Gather(game)
	if d != nil {
		if d.Game.Artwork != nil {
			t.Fatal("pictures outside the folder")
		}
		if _, err := d.EncodeAny(); err == nil {
			t.Fatal("an over-long title should be refused when written")
		}
	}
	for _, rel := range []string{"../x", "/abs", `C:\x`, `..\..\x`, "http://example.org/x.png", "a/../../x"} {
		if _, ok := inside(dir, rel); ok {
			t.Errorf("%s accepted", rel)
		}
	}
	if p, ok := inside(dir, "./media/a.png"); !ok || p != filepath.Join(dir, "media", "a.png") {
		t.Error("an ordinary path")
	}
}

// Several pictures of one kind: numbered extras in a media folder and beside the game are gathered
// after the first, written back as Game-02.png..., and never written over a file already there.
func TestSeveralPicturesGathered(t *testing.T) {
	dir := t.TempDir()
	game := filepath.Join(dir, "Quest (Europe).sfc")
	put(t, game, []byte("rom"))
	put(t, filepath.Join(dir, "media", "box2dfront", "Quest (Europe).png"), pic(t, 4))
	put(t, filepath.Join(dir, "media", "box2dfront", "Quest (Europe)-2.png"), pic(t, 5))
	put(t, filepath.Join(dir, "media", "box2dfront", "Quest (Europe)-10.png"), pic(t, 6))
	put(t, filepath.Join(dir, "Quest (Europe)-boxart.png"), pic(t, 7))
	put(t, filepath.Join(dir, "Quest (Europe)-boxart-2.png"), pic(t, 8))
	put(t, filepath.Join(dir, "Quest (Europe)-boxart-4.png"), pic(t, 9))               // after a gap: not read
	put(t, filepath.Join(dir, "media", "fanart", "Quest (Europe)-02.png"), pic(t, 10)) // no first: not read
	d, _, err := Gather(game)
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if got := d.Game.Artwork["boxfront"]; len(got) != 5 || len(d.Game.Artwork["fanart"]) != 0 {
		t.Fatalf("gathered %v", d.Game.Artwork)
	}
	if b, err := d.EncodeAny(); err != nil {
		t.Fatal(err)
	} else if _, err := wittyplayfile.Parse(b); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	game2 := filepath.Join(out, "Quest (Europe).sfc")
	put(t, game2, []byte("rom"))
	put(t, filepath.Join(out, "media", "box2dfront", "Quest (Europe)-03.png"), pic(t, 11)) // the owner's own
	w, err := WriteOut(game2, d, "snes", Options{Skip: map[string]bool{"nfo": true, "gamelist": true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"media/box2dfront/Quest (Europe).png", "media/box2dfront/Quest (Europe)-02.png", "media/box2dfront/Quest (Europe)-05.png"} {
		if !contains(w.Files, want) {
			t.Errorf("didn't write %s: %+v", want, w)
		}
	}
	if !contains(w.Kept, "media/box2dfront/Quest (Europe)-03.png") {
		t.Errorf("wrote over the owner's picture: %+v", w)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "media", "box2dfront", "Quest (Europe)-03.png")); !bytes.Equal(b, pic(t, 11)) {
		t.Errorf("the owner's picture changed")
	}
	back, _, _ := Gather(game2)
	if back == nil || len(back.Game.Artwork["boxfront"]) != 5 {
		t.Fatalf("back %v", back)
	}
}
