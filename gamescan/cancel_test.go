// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wittywilla/wittymodule"
)

// cancelAfter is a context that ends once it has been asked n times whether it has.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	c.n--
	if c.n < 0 {
		return context.Canceled
	}
	return nil
}

// A cancelled scan keeps what it really worked out, and nothing else: what it didn't get to
// isn't taken for a file that isn't a game, now or in any scan after it.
func TestCancellingAScanDoesNotChangeWhatTheNextOneFinds(t *testing.T) {
	d := testDB(t)
	root := t.TempDir()
	for i, name := range []string{"Alpha Quest.sfc", "Bravo Racer.sfc", "Charlie Golf.sfc", "Delta Force Ten.sfc"} {
		if err := os.WriteFile(filepath.Join(root, name), bytes.Repeat([]byte{byte(i + 1)}, 512<<10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	whole := func(id int64) int {
		ix := newArchiveIndex(d, id)
		defer ix.close()
		c, err := ix.discover(root, "snes")
		if err != nil {
			t.Fatal(err)
		}
		return len(c)
	}
	want := whole(2)
	if want != 4 {
		t.Fatalf("found %d of 4 to begin with", want)
	}

	// Cancelled partway: it says so, rather than handing back a short list.
	ix := newArchiveIndex(d, 1)
	found, err := ix.discoverCtx(&cancelAfter{Context: context.Background(), n: 2}, root, "snes")
	ix.close()
	if !errors.Is(err, context.Canceled) || found != nil {
		t.Fatalf("cancelled: %v, %v", found, err)
	}
	if n := whole(1); n != want {
		t.Fatalf("after a cancelled scan: %d, want %d", n, want)
	}

	// Through a Scan: a batch that's stopped answers with the stop, and the next scan of the
	// library still finds everything.
	lib := wittymodule.ScanLibrary{ID: 9, Kind: "games", Facet: "snes", Paths: []string{root}}
	s := Begin(d, "stopped", lib)
	if err := s.Add(&cancelAfter{Context: context.Background(), n: 1}, walk(root), 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped batch: %v", err)
	}
	s.Close()
	if got := scanLibrary(t, d, lib); len(got.Items) != want {
		t.Fatalf("after a stopped scan: %d items", len(got.Items))
	}
}

// A scan reports its progress as it moves: each batch's files, then Finish's work, ending at
// its total.
func TestScanReportsAsItMoves(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "a.sfc", "b.sfc", "c.gba", "d/e.nes", "readme.txt")
	lib := wittymodule.ScanLibrary{ID: 1, Kind: "games", Facet: "auto", Paths: []string{root}}
	s := Begin(testDB(t), "report", lib)
	var got [][2]int
	s.Report = func(done, total int, _ string) { got = append(got, [2]int{done, total}) }
	files := walk(root)
	ctx := context.Background()
	if err := s.Add(ctx, files[:2], len(files)); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(ctx, files[2:], len(files)); err != nil {
		t.Fatal(err)
	}
	adding := len(got)
	if adding == 0 || got[adding-1] != [2]int{len(files), len(files)} {
		t.Fatalf("adding reported %v", got)
	}
	if _, err := s.Finish(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) == adding {
		t.Fatal("finishing reported nothing")
	}
	last := got[len(got)-1]
	if last[0] != last[1] || last[1] == 0 {
		t.Fatalf("finishing ended at %v", last)
	}
	for i := 1; i < len(got); i++ {
		if got[i][0] < got[i-1][0] && i != adding {
			t.Fatalf("progress went back: %v", got)
		}
	}
}

// While a scan's batches come in, it counts the games it has sorted for each console, and its
// reports name the console it's on: the pages show consoles before the scan ends (#88).
func TestScanCountsConsolesAsItSorts(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, "a.sfc", "b.sfc", "c.gba", "readme.txt")
	lib := wittymodule.ScanLibrary{ID: 1, Kind: "games", Facet: "auto", Paths: []string{root}}
	s := Begin(testDB(t), "found", lib)
	if by, n := s.Found(); n != 0 || len(by) != 0 {
		t.Fatalf("found %v before any file came", by)
	}
	var said []string
	s.Report = func(_, _ int, console string) { said = append(said, console) }
	files := walk(root)
	if err := s.Add(context.Background(), files, len(files)); err != nil {
		t.Fatal(err)
	}
	by, n := s.Found()
	if n != 3 || by["snes"] != 2 || by["gba"] != 1 {
		t.Fatalf("found %v (%d in all)", by, n)
	}
	if len(said) == 0 || said[len(said)-1] == "" {
		t.Fatalf("reports named no console: %q", said)
	}
	out, err := s.Finish(context.Background(), nil)
	if err != nil || len(out.Items) != 3 {
		t.Fatalf("finished with %d items, %v", len(out.Items), err)
	}
}
