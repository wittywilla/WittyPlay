// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gameid"
	"github.com/wittywilla/wittyplay/gameid/fixture"
)

// put writes a file for a test.
func put(t *testing.T, root, rel string, b []byte) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func putZip(t *testing.T, root, rel string, files map[string][]byte) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, b := range files {
		e, _ := w.Create(name)
		_, _ = e.Write(b)
	}
	_ = w.Close()
	return p
}

func discovered(t *testing.T, root, console string) ([]string, map[string]gameFileNote) {
	t.Helper()
	ix := newArchiveIndex(nil, 0)
	defer ix.close()
	c, err := ix.discover(root, console)
	if err != nil {
		t.Fatal(err)
	}
	notes := map[string]gameFileNote{}
	for _, n := range ix.notes {
		rel, _ := filepath.Rel(root, n.path)
		notes[filepath.ToSlash(rel)] = n
	}
	return names(c, root), notes
}

// The heart of it: what was indexed wrongly before, each with the file it was.
func TestNonGamesAreNotGames(t *testing.T) {
	root := t.TempDir()
	// In a folder for a console that uses these extensions, but not games at all.
	put(t, root, "Sega Genesis/README.md", []byte("# Notes\n\nMade up text about these games.\n"))
	put(t, root, "Sega Genesis/Real Game.md", gameBytes("x.md"))
	put(t, root, "Game Boy/Installer.dmg", append([]byte("koly"), make([]byte, 64<<10)...))
	put(t, root, "Virtual Boy/Program.vb", []byte("Module Program\n  Sub Main()\n  End Sub\nEnd Module\n"))
	// With no console folder at all: only their contents could say, and they say no.
	put(t, root, "notes.md", []byte("# Plain text\n"))
	put(t, root, "Setup.dmg", append([]byte("koly"), make([]byte, 64<<10)...))
	put(t, root, "Source.vb", []byte("Imports System\n"))
	put(t, root, "script.ws", []byte("WScript.Echo(1)\n"))
	// A BIOS, by name and by its bytes, wherever it is.
	put(t, root, "Super Nintendo/Super Made Up BIOS (V1.0).sfc", gameBytes("y.sfc"))
	put(t, root, "Playstation 2 (PS2)/SCPH-39001.bin", biosFixture())
	// Update and extra content for a 3DS game, told apart by their title ids.
	put(t, root, "Nintendo 3DS/Game Update.cia", fixture.CIA(fixture.NCCH(true, false, 1<<14), 0, 0x0004000E))
	put(t, root, "Nintendo 3DS/Game Extras.cia", fixture.CIA(fixture.NCCH(true, false, 1<<14), 0, 0x0004008C))
	put(t, root, "Nintendo 3DS/Game (USA).cia", fixture.CIA(fixture.NCCH(true, false, 1<<14), 0, 0x00040000))
	// A music CD's sheet, and a data track with no sheet.
	put(t, root, "Playstation/Album.bin", make([]byte, 4096))
	put(t, root, "Playstation/Album.cue", []byte("FILE \"Album.bin\" BINARY\n  TRACK 01 AUDIO\n    INDEX 01 00:00:00\n"))
	put(t, root, "Playstation/Lost (Track 02).bin", make([]byte, 4096))

	got, notes := discovered(t, root, "auto")
	want := []string{"Nintendo 3DS/Game (USA).cia=3ds", "Sega Genesis/Real Game.md=md"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for rel, kind := range map[string]string{
		"Sega Genesis/README.md":                       string(gameid.KindOther),
		"Game Boy/Installer.dmg":                       reviewNeeds,
		"Virtual Boy/Program.vb":                       string(gameid.KindOther),
		"Super Nintendo/Super Made Up BIOS (V1.0).sfc": string(gameid.KindBIOS),
		"Playstation 2 (PS2)/SCPH-39001.bin":           string(gameid.KindBIOS),
		"Nintendo 3DS/Game Update.cia":                 string(gameid.KindUpdate),
		"Nintendo 3DS/Game Extras.cia":                 string(gameid.KindDLC),
		"Playstation/Album.cue":                        string(gameid.KindAudio),
		"Playstation/Lost (Track 02).bin":              string(gameid.KindData),
	} {
		if n, ok := notes[rel]; !ok || n.kind != kind {
			t.Errorf("%s: %+v, want %s", rel, n, kind)
		} else if n.reason == "" {
			t.Errorf("%s: no reason given", rel)
		}
	}
	if _, listed := notes["Playstation/Album.bin"]; listed {
		t.Error("a track its sheet names should be part of the sheet, not a file of its own")
	}
}

func biosFixture() []byte {
	b := make([]byte, 4<<20)
	copy(b[0x2740:], "RESET\x00")
	return b
}

// Folders and extensions make a guess, and a file's bytes settle it.
func TestConsoleComesFromTheBytes(t *testing.T) {
	root := t.TempDir()
	// In the wrong folder: the file wins.
	put(t, root, "Playstation/Really a PS2 Game.iso", fixture.PS2("SLUS_543.21"))
	put(t, root, "Playstation Portable/Really a PS1 Game.iso", fixture.PS1("SLUS_123.45"))
	put(t, root, "Playstation 2/Really a PSP Game.cso", fixture.CSO(fixture.PSP("Made Up", "ULUS10000")))
	put(t, root, "Super Nintendo/Really an N64 Game.bin", fixture.N64("MADE UP", "NXXE"))
	// In no folder at all: the file says.
	put(t, root, "Mystery PS2.iso", fixture.PS2("SLUS_100.00"))
	put(t, root, "Mystery PSP.iso", fixture.PSP("Made Up Two", "ULUS10001"))
	put(t, root, "Mystery Raw PS1.bin", fixture.Raw(fixture.PS1("SLUS_200.00")))
	put(t, root, "Mystery Dreamcast.iso", fixture.Boot("SEGA SEGAKATANA"))
	put(t, root, "Mystery Mega Drive.bin", fixture.MegaDrive("MADE UP", "GM 00000000-00"))
	// A console this server can't play gets named, and left for a person.
	put(t, root, "Mystery Saturn.iso", fixture.Boot("SEGA SEGASATURN"))
	// The GameCube is one it plays (the Dolphin core, or Dolphin), so its disc is a game.
	put(t, root, "Mystery GameCube.iso", gcFixture())
	// A disc with nothing to say, and a folder that's no help.
	put(t, root, "Plain Disc.iso", fixture.ISO("LINUX"))
	// A folder says one thing, and nothing in the file backs it up.
	put(t, root, "Playstation 2/Not a Disc.iso", make([]byte, 64<<10))
	put(t, root, "NES/Not a Cartridge.nes", make([]byte, 64<<10))

	// A dump with no header isn't nothing, if it could be a cartridge's memory.
	headerless := make([]byte, 64<<10)
	headerless[0] = 0x78
	put(t, root, "NES/Plain Dump.nes", headerless)

	got, notes := discovered(t, root, "auto")
	want := []string{
		"Mystery Dreamcast.iso=dc", "Mystery GameCube.iso=gc", "Mystery Mega Drive.bin=md", "Mystery PS2.iso=ps2", "Mystery PSP.iso=psp", "Mystery Raw PS1.bin=ps1",
		"NES/Plain Dump.nes=nes", "Playstation 2/Really a PSP Game.cso=psp", "Playstation Portable/Really a PS1 Game.iso=ps1", "Playstation/Really a PS2 Game.iso=ps2",
		"Super Nintendo/Really an N64 Game.bin=n64",
	}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, rel := range []string{"Mystery Saturn.iso", "Plain Disc.iso", "Playstation 2/Not a Disc.iso", "NES/Not a Cartridge.nes"} {
		if n := notes[rel]; n.kind != reviewNeeds || n.reason == "" {
			t.Errorf("%s: %+v", rel, n)
		}
	}
	if !strings.Contains(notes["Mystery Saturn.iso"].reason, "Saturn") {
		t.Errorf("it should say what it found: %q", notes["Mystery Saturn.iso"].reason)
	}
	if len(notes["Playstation 2/Not a Disc.iso"].suggest) == 0 {
		t.Error("the folder's console should be offered as the fix")
	}
}

func gcFixture() []byte {
	b := make([]byte, 1<<20)
	copy(b, "GXXE01")
	copy(b[0x1C:], []byte{0xC2, 0x33, 0x9F, 0x3D})
	return b
}

// An archive with no console folder used to go to whichever console first claimed any
// extension in it, however small: a readme.md made it a Mega Drive game.
func TestArchivesAreNotSortedBySmallFiles(t *testing.T) {
	root := t.TempDir()
	putZip(t, root, "A PC Game.zip", map[string][]byte{"readme.md": []byte("# Read me\n"), "game.exe": append([]byte("MZ"), make([]byte, 1<<20)...)})
	putZip(t, root, "A Real Game.zip", map[string][]byte{"readme.md": []byte("# Read me\n"), "game.z64": fixture.N64("MADE UP", "NXXE")})
	putZip(t, root, "A PS2 Game.zip", map[string][]byte{"game.iso": fixture.PS2("SLUS_543.21")})
	putZip(t, root, "Super Nintendo/Holds a NES Game.zip", map[string][]byte{"game.nes": fixture.NES()})
	got, notes := discovered(t, root, "auto")
	want := []string{"A PS2 Game.zip=ps2", "A Real Game.zip=n64", "Super Nintendo/Holds a NES Game.zip=nes"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if n := notes["A PC Game.zip"]; n.kind == reviewNeeds {
		t.Errorf("a program with a readme is nothing to look at: %+v", n)
	}
}

func TestSetsOfCartridges(t *testing.T) {
	root := t.TempDir()
	snes := func(rel string, names ...string) {
		files := map[string][]byte{}
		for _, n := range names {
			files[n] = gameBytes(n)
		}
		putZip(t, root, "Super Nintendo/"+rel, files)
	}
	snes("Copier BIOS Set.zip", "Made Up V1 BIOS.smc", "Made Up V2 BIOS [o1].smc")
	snes("Cheat Device.zip", "Game Genie (U).smc")
	snes("Public Domain.zip", "Intro One (PD).smc", "Intro Two (PD).smc", "Demo Three (PD).smc")
	snes("Fan Hacks.zip", "Alpha (Made Up Hack).smc", "Beta (Made Up Hack).smc", "Gamma (Made Up Hack).smc", "Delta (Made Up Hack).smc")
	snes("Grab Bag.zip", "One (U).smc", "Two (U).smc", "Three (U).smc", "Four (U).smc", "Five (U).smc", "Six (U).smc", "Seven (U).smc", "Eight (U).smc")
	snes("Made Up Quest.zip", "Made Up Quest (U) [!].smc", "Made Up Quest (U) [b1].smc", "Made Up Quest (U) [T+Fre].smc", "Made Up Quest (E).smc", "Other Name (J).smc", "Made Up Quest (Made Up Hack).smc")
	snes("Made Up Generations.zip", "Made Up Generation A (ST).smc", "Made Up Generation B (ST).smc", "Made Up Generation C (ST).smc", "Made Up Generation D (ST).smc", "Made Up Generation E (ST).smc", "Made Up Generation F (ST).smc", "Made Up Generation G (ST).smc", "Made Up Generation H (ST).smc")
	snes("Legend of Stuff, The.zip", "Legend of Stuff, The (U).smc", "Side Story One (J).smc", "Side Story Two (J).smc", "Side Story Three (J).smc", "Side Story Four (J).smc", "Side Story Five (J).smc", "Side Story Six (J).smc", "Side Story Seven (J).smc", "Side Story Eight (J).smc")
	snes("With a BIOS Inside.zip", "With a BIOS Inside + ST BIOS (ST) [h1].smc")
	got, notes := discovered(t, root, "auto")
	want := []string{"Super Nintendo/Legend of Stuff, The.zip=snes", "Super Nintendo/Made Up Generations.zip=snes", "Super Nintendo/Made Up Quest.zip=snes", "Super Nintendo/With a BIOS Inside.zip=snes"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for rel, kind := range map[string]string{
		"Super Nintendo/Copier BIOS Set.zip": string(gameid.KindBIOS),
		"Super Nintendo/Cheat Device.zip":    string(gameid.KindUtility),
		"Super Nintendo/Public Domain.zip":   reviewNeeds,
		"Super Nintendo/Fan Hacks.zip":       reviewNeeds,
		"Super Nintendo/Grab Bag.zip":        reviewNeeds,
	} {
		if n := notes[rel]; n.kind != kind {
			t.Errorf("%s: %+v, want %s", rel, n, kind)
		}
	}
}

// A file with no proof goes on the list, with the fix one click away, and that click
// sticks. Nothing about it is forgotten at the next scan.
func TestNeedsALookFlow(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	root := t.TempDir()
	lib := wittymodule.ScanLibrary{ID: 7, Kind: "games", Facet: "auto", Paths: []string{root}}
	headerless := put(t, root, "NES/Odd Dump (USA).nes", make([]byte, 64<<10))
	put(t, root, "NES/Good (USA).nes", gameBytes("Good.nes"))
	bios := put(t, root, "NES/Made Up BIOS.nes", gameBytes("Bios.nes"))
	listed := func() map[string]wittymodule.ScanItem {
		t.Helper()
		out := map[string]wittymodule.ScanItem{}
		for _, it := range scanLibrary(t, d, lib).Items {
			out[it.Files[0]] = it
		}
		return out
	}
	listed()
	r, err := ReviewList(ctx, d, lib.ID, lib.Paths, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Counts[reviewNeeds] != 1 || r.Counts[string(gameid.KindBIOS)] != 1 {
		t.Fatalf("counts: %v", r.Counts)
	}
	var needs *GameReviewItem
	for i := range r.Items {
		if r.Items[i].Kind == reviewNeeds {
			needs = &r.Items[i]
		}
	}
	if needs == nil || needs.Path != "NES/Odd Dump (USA).nes" || needs.Reason == "" || len(needs.Suggest) == 0 || needs.Suggest[0].Console != "nes" {
		t.Fatalf("%+v", needs)
	}
	// One click: it's a NES game.
	if _, err := DecideReviewed(ctx, d, lib.ID, needs.ID, "add", "nes"); err != nil {
		t.Fatal(err)
	}
	if it, ok := listed()[headerless]; !ok || it.Facets["console"] != "nes" || it.Title != "Odd Dump" {
		t.Fatalf("%+v %v", it, ok)
	}
	if r, _ = ReviewList(ctx, d, lib.ID, lib.Paths, reviewNeeds, 100, 0); r.Total != 0 {
		t.Fatalf("it should have left the list: %+v", r.Items)
	}
	// And a person can say a game isn't one: ignored, and it stays that way.
	r, _ = ReviewList(ctx, d, lib.ID, lib.Paths, "left", 100, 0)
	if r.Total != 1 {
		t.Fatalf("left out: %+v", r)
	}
	if _, err := DecideReviewed(ctx, d, lib.ID, r.Items[0].ID, "add", "nes"); err != nil {
		t.Fatal(err)
	}
	if _, ok := listed()[bios]; !ok {
		t.Fatal("overruled a left-out file, and it isn't listed")
	}
	if err := Decide(ctx, d, lib.ID, bios, "ignore", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := listed()[bios]; ok {
		t.Fatal("ignored, and still listed")
	}
	if r, _ = ReviewList(ctx, d, lib.ID, lib.Paths, reviewIgnored, 100, 0); r.Total != 1 {
		t.Fatalf("ignored list: %+v", r)
	}
}

// A game on several discs is one game, in whichever of the ways it's kept. The same disc
// twice isn't listed twice, and its checksums prove it.
func TestDiscsAndDuplicates(t *testing.T) {
	root := t.TempDir()
	disc := func(serial string) []byte { return fixture.Raw(fixture.PS1(serial)) }
	cue := func(bin string) []byte { return []byte("FILE \"" + bin + "\" BINARY\n  TRACK 01 MODE2/2352\n") }
	put(t, root, "PS1/Made Up Game Disc 1.bin", disc("SLUS_100.01"))
	put(t, root, "PS1/Made Up Game Disc 1.cue", cue("Made Up Game Disc 1.bin"))
	// The same disc again, in an archive named for a web address, and the other two only there.
	putZip(t, root, "PS1/made-up-game-disc1of3-u-slus-10001-.zip", map[string][]byte{"Made Up Game Disc 1.bin": disc("SLUS_100.01"), "Made Up Game Disc 1.cue": cue("Made Up Game Disc 1.bin")})
	putZip(t, root, "PS1/made-up-game-disc2of3-u-slus-10002-.zip", map[string][]byte{"Made Up Game Disc 2.bin": disc("SLUS_100.02"), "Made Up Game Disc 2.cue": cue("Made Up Game Disc 2.bin")})
	putZip(t, root, "PS1/made-up-game-disc3of3-u-slus-10003-.zip", map[string][]byte{"Made Up Game Disc 3.bin": disc("SLUS_100.03"), "Made Up Game Disc 3.cue": cue("Made Up Game Disc 3.bin")})
	// Discs in folders of their own.
	put(t, root, "PS1/Other Game/Disc 1/game.bin", disc("SLUS_200.01"))
	put(t, root, "PS1/Other Game/Disc 2/game.bin", disc("SLUS_200.02"))
	// A plain copy under another name, in another folder.
	put(t, root, "NES/Game (USA).nes", gameBytes("Game (USA).nes"))
	put(t, root, "NES/Favourites/Game Copy.nes", gameBytes("Game (USA).nes"))

	ix := newArchiveIndex(nil, 0)
	defer ix.close()
	found, err := ix.discover(root, "auto")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]candidate{}
	for _, c := range found {
		byName[filepath.Base(c.path)] = c
	}
	if len(found) != 3 {
		t.Fatalf("got %v", names(found, root))
	}
	set := byName["Made Up Game Disc 1.cue"]
	if len(set.discs) != 3 || set.discs[0].disc != 1 || set.discs[2].disc != 3 {
		t.Fatalf("discs: %+v", set.discs)
	}
	if other := byName["game.bin"]; len(other.discs) != 2 {
		t.Fatalf("discs in folders: %+v", other)
	}
	dups := 0
	for _, n := range ix.notes {
		if n.kind == reviewDuplicate {
			dups++
			if !strings.Contains(n.reason, "checksums") {
				t.Errorf("why? %q", n.reason)
			}
		}
	}
	if dups != 2 {
		t.Fatalf("%d duplicates: %+v", dups, ix.notes)
	}
}

func TestVersionsOfOneGameShareATile(t *testing.T) {
	ctx := context.Background()
	d := testDB(t)
	root := t.TempDir()
	lib := wittymodule.ScanLibrary{ID: 3, Kind: "games", Facet: "nes", Paths: []string{root}}
	for _, name := range []string{"Mega Game (Japan).nes", "Mega Game (USA).nes", "Mega Game (USA) (Rev 1).nes", "Mega Game (USA) (Beta).nes", "Other Game (USA).nes"} {
		put(t, root, name, gameBytes(name))
	}
	// tiles are the games standing on their own; under, the versions under each, by path.
	scan := func() (tiles []wittymodule.ScanItem, under map[string][]wittymodule.ScanItem) {
		t.Helper()
		under = map[string][]wittymodule.ScanItem{}
		for _, it := range scanLibrary(t, d, lib).Items {
			if it.VersionOf == "" {
				tiles = append(tiles, it)
			} else {
				under[it.VersionOf] = append(under[it.VersionOf], it)
			}
		}
		return tiles, under
	}
	tiles, under := scan()
	if len(tiles) != 2 {
		t.Fatalf("got %d tiles", len(tiles))
	}
	var head wittymodule.ScanItem
	for _, it := range tiles {
		if it.Title == "Mega Game" {
			head = it
		}
	}
	if head.Tagline != "USA, Rev 1" {
		t.Fatalf("the best version should head them: %+v", head)
	}
	if versions := under[head.Files[0]]; len(versions) != 3 {
		t.Fatalf("versions: %d", len(versions))
	}
	for _, v := range under[head.Files[0]] {
		if v.VersionLabel == "" {
			t.Fatalf("a version with nothing to say which: %+v", v)
		}
	}
	// The same version stays at the head, as saves belong to it, even if a better one arrives.
	put(t, root, "Mega Game (World) (Rev 2).nes", gameBytes("newer.nes"))
	tiles, under = scan()
	if len(tiles) != 2 || len(under[head.Files[0]]) != 4 {
		t.Fatalf("the head changed: %d tiles, %+v", len(tiles), under)
	}
	// Someone splits one off: it stays split.
	japan := filepath.Join(root, "Mega Game (Japan).nes")
	if err := Decide(ctx, d, lib.ID, japan, "own", ""); err != nil {
		t.Fatal(err)
	}
	if tiles, _ = scan(); len(tiles) != 3 {
		t.Fatalf("after a split: %d tiles", len(tiles))
	}
	// What the routes read back.
	l, err := ListedGame(ctx, d, lib.ID, japan)
	if err != nil || l.VersionOf != "" {
		t.Fatalf("a split came back: %+v %v", l, err)
	}
	if vs, err := VersionsOf(ctx, d, lib.ID, head.Files[0]); err != nil || len(vs) != 3 {
		t.Fatalf("versions kept: %+v %v", vs, err)
	}
}

// The owner's order of regions decides which version leads a game, once the scan is told the
// order changed.
func TestTheOwnersRegionOrderDecidesWhatComesFirst(t *testing.T) {
	t.Cleanup(func() { gamearchive.SetRegionOrder("") })
	gamearchive.SetRegionOrder("")
	ctx := context.Background()
	d := testDB(t)
	root := t.TempDir()
	lib := wittymodule.ScanLibrary{ID: 4, Kind: "games", Facet: "nes", Paths: []string{root}}
	for _, name := range []string{"Mega Game (Japan).nes", "Mega Game (USA).nes", "Other Game (Japan).nes", "Other Game (USA).nes"} {
		put(t, root, name, gameBytes(name))
	}
	heads := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, it := range scanLibrary(t, d, lib).Items {
			if it.VersionOf == "" {
				out[it.Title] = it.Tagline
			}
		}
		return out
	}
	if first := heads(); len(first) != 2 || first["Mega Game"] != "USA" || first["Other Game"] != "USA" {
		t.Fatalf("usual order: %+v", first)
	}
	gamearchive.SetRegionOrder("japan")
	if after := heads(); after["Mega Game"] != "USA" {
		t.Fatalf("a head keeps its place until the order is said to have changed: %+v", after)
	}
	if err := RegionsChanged(ctx, d, lib.ID); err != nil {
		t.Fatal(err)
	}
	if after := heads(); len(after) != 2 || after["Mega Game"] != "Japan" || after["Other Game"] != "Japan" {
		t.Fatalf("the new order: %+v", after)
	}
}

// Encrypted 3DS files can't play here. Switch and Wii U games are encrypted as a rule,
// and play in a standalone emulator with the owner's keys: they're games, not problems.
func TestFilesThatNeedTheOwnersKeysAreGames(t *testing.T) {
	if v := proven(gameid.Finding{Console: "3ds", Kind: gameid.KindGame, Encrypted: true}, ""); v.Problem != "encrypted" {
		t.Fatalf("an encrypted 3DS file: %+v", v)
	}
	if v := proven(gameid.Finding{Console: "3ds", Kind: gameid.KindGame, Encrypted: true, NeedsKeys: true}, ""); v.Problem != "" {
		t.Fatalf("a file that needs the owner's keys: %+v", v)
	}
}

// A Switch game's file in a Switch 2 folder or library is a Switch 2 game: the file can't say,
// so the folder does, and it isn't sent to Needs a look as a game for another console.
func TestSwitch2ByFolder(t *testing.T) {
	root := t.TempDir()
	nsp := fixture.NSP("0100000000010000.nca", "0100000000010000.tik")
	for _, rel := range []string{"Switch 2/game.nsp", "Switch/game.nsp"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nsp, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if v := judgeFile(filepath.Join(root, "Switch 2", "game.nsp"), "Switch 2/game.nsp", wittyplay.Auto, int64(len(nsp))); v.Console != "switch2" || v.Left != "" {
		t.Errorf("in a Switch 2 folder: %+v", v)
	}
	if v := judgeFile(filepath.Join(root, "Switch 2", "game.nsp"), "Switch 2/game.nsp", "switch2", int64(len(nsp))); v.Console != "switch2" || v.Left != "" {
		t.Errorf("in a Switch 2 library: %+v", v)
	}
	if v := judgeFile(filepath.Join(root, "Switch", "game.nsp"), "Switch/game.nsp", wittyplay.Auto, int64(len(nsp))); v.Console != "switch" {
		t.Errorf("in a Switch folder: %+v", v)
	}
}
