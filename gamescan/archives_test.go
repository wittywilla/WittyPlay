// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/gameid/fixture"
)

func copyArchive(t *testing.T, name, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "gamearchive", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverGamesInEveryKindOfArchive(t *testing.T) {
	root := t.TempDir()
	copyArchive(t, "cart.tar.bz2", filepath.Join(root, "Super Nintendo (SNES)", "A Game (USA).tar.bz2"))
	copyArchive(t, "cart.rar", filepath.Join(root, "SNES", "B Game (Europe).rar"))
	discZip(t, filepath.Join(root, "Playstation (PSX)", "C Game.zip"))
	copyArchive(t, "disc-solid.7z", filepath.Join(root, "Playstation (PSX)", "Not Really.7z"))
	copyArchive(t, "cart.7z", filepath.Join(root, "Unsorted", "D Game.7z")) // sorted by what's inside
	copyArchive(t, "disc-solid.7z", filepath.Join(root, "Unsorted", "E Game.7z"))
	copyArchive(t, "locked-names.7z", filepath.Join(root, "SNES", "F Locked.7z"))
	copyArchive(t, "split.part1.rar", filepath.Join(root, "SNES", "G Split.part1.rar"))
	copyArchive(t, "split.part2.rar", filepath.Join(root, "SNES", "G Split.part2.rar"))
	copyArchive(t, "split.part3.rar", filepath.Join(root, "SNES", "G Split.part3.rar"))
	copyArchive(t, "split7.7z.001", filepath.Join(root, "SNES", "H Split.7z.001"))
	copyArchive(t, "split7.7z.002", filepath.Join(root, "SNES", "H Split.7z.002"))
	copyArchive(t, "split7.7z.003", filepath.Join(root, "SNES", "H Split.7z.003"))
	writeFiles(t, root, "SNES/backup.001") // not an archive, whatever folder it's in
	got, err := discoverGames(root, "auto")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Playstation (PSX)/C Game.zip=ps1", "SNES/B Game (Europe).rar=snes", "SNES/G Split.part1.rar=snes", "SNES/H Split.7z.001=snes",
		"Super Nintendo (SNES)/A Game (USA).tar.bz2=snes", "Unsorted/D Game.7z=snes",
	}
	if !equal(names(got, root), want) {
		t.Fatalf("got %v, want %v", names(got, root), want)
	}
	skipped := skippedGameFiles(root, "auto")
	// E Game holds only a .cue and .bin, which say nothing without a folder to go by, and
	// the same disc beside the PlayStation games doesn't have what a PlayStation disc has.
	if skipped["encrypted"] != 1 || skipped[".7z"] != 2 || len(skipped) != 2 {
		t.Fatalf("skipped: %v", skipped)
	}

	// A library of one console: anything in it holding a game for that console.
	got, err = discoverGames(filepath.Join(root, "Playstation (PSX)"), "ps1")
	if err != nil || len(got) != 1 {
		t.Fatalf("one console: %v %v", got, err)
	}
	// In a library set to a console, a game for another one wants a look. It isn't
	// quietly left out, and it isn't quietly listed.
	ix := newArchiveIndex(nil, 0)
	defer ix.close()
	if got, _ := ix.discover(filepath.Join(root, "Playstation (PSX)"), "snes"); len(got) != 0 {
		t.Fatalf("a disc isn't a Super Nintendo game: %v", got)
	}
	if len(ix.notes) == 0 {
		t.Fatal("nothing was said about it")
	}
}

// discZip makes an archive holding a disc that's really a disc.
func discZip(t *testing.T, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	e, _ := w.Create("Game.bin")
	_, _ = e.Write(fixture.Raw(fixture.PS1("SLUS_123.45")))
	c, _ := w.Create("Game.cue")
	_, _ = c.Write([]byte("FILE \"Game.bin\" BINARY\n  TRACK 01 MODE2/2352\n"))
	_ = w.Close()
}

func TestArchiveTitlesKeepTheirTags(t *testing.T) {
	for in, want := range map[string]string{
		"Mega Game, The (USA) (Rev 1) [!].7z": "Mega Game, The (USA) (Rev 1) [!]",
		"Game (USA).tar.gz":                   "Game (USA)",
		"Game (USA).sfc.7z":                   "Game (USA)",
		"Game (Japan).part1.rar":              "Game (Japan)",
		"Game (Japan).7z.001":                 "Game (Japan)",
		"Game (Japan).sfc.xz":                 "Game (Japan)",
		"Game.sfc.gz":                         "Game",
		"Game.sfc":                            "Game",
		"Dr. Game.zip":                        "Dr. Game",
	} {
		if got := gameBaseName(in); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
	title, note := ParseGameName(gameBaseName("Mega Game, The (USA) (Rev 1) [!].7z"))
	if title != "The Mega Game" || note != "USA, Rev 1" {
		t.Errorf("%q, %q", title, note)
	}
}

// Scanning an unchanged archive doesn't open it.
func TestScanRemembersWhatArchivesHold(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	root := t.TempDir()
	const lib = 5
	game := filepath.Join(root, "Game (USA).7z")
	copyArchive(t, "cart.7z", game)
	gone := filepath.Join(root, "Gone.rar")
	copyArchive(t, "cart.rar", gone)
	unreadable := filepath.Join(root, "Rubbish.7z")
	if err := os.WriteFile(unreadable, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	// How many games were found, and whether anything had to be read to find them:
	// a lister only exists once something did.
	scan := func() (int, bool) {
		ix := newArchiveIndex(d, lib)
		defer ix.close()
		c, err := ix.discover(root, "snes")
		if err != nil {
			t.Fatal(err)
		}
		ix.save(ctx, true)
		return len(c), ix.lister != nil
	}
	if n, read := scan(); n != 2 || !read {
		t.Fatalf("first scan: %d, %v", n, read)
	}
	// The scan's progress hears about each archive it really opens, and only those.
	ix := newArchiveIndex(d, lib)
	var told []string
	ix.reading = func(path string) { told = append(told, filepath.Base(path)) }
	fresh := filepath.Join(root, "Fresh.7z")
	copyArchive(t, "cart.7z", fresh)
	if _, err := ix.discover(root, "snes"); err != nil {
		t.Fatal(err)
	}
	ix.close()
	if len(told) != 1 || told[0] != "Fresh.7z" {
		t.Fatalf("told about %v", told)
	}
	if err := os.Remove(fresh); err != nil {
		t.Fatal(err)
	}
	if n, read := scan(); n != 2 || read {
		t.Fatalf("second scan: %d, %v", n, read)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(game, future, future); err != nil {
		t.Fatal(err)
	}
	if n, read := scan(); n != 2 || !read {
		t.Fatalf("a changed file wasn't read: %d, %v", n, read)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if n, read := scan(); n != 1 || read {
		t.Fatalf("after removing one: %d, %v", n, read)
	}
	var rows int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_files WHERE library = ?`, lib).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 { // Game (USA).7z, and that Rubbish.7z is rubbish
		t.Fatalf("rows: %d", rows)
	}
}
