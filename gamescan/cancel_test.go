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
