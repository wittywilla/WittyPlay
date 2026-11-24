// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamemeta

import (
	"context"
	"sync/atomic"
	"testing"
)

// A scan describing one console's games while the worker asks about another's must not read
// both lists again for every game: each is read once while a few consoles fit in memory.
func TestLibretroListsStayLoadedAcrossConsoles(t *testing.T) {
	files := snesFile()
	files["Nintendo - Nintendo 64"] = makeRDB(map[string]any{"name": "Mega Racer 64 (USA)", "crc": crcBytes(0xCCCC0001)})
	var hits atomic.Int32
	srv := rdbServer(t, &hits, files)
	src := &Libretro{HTTP: srv.Client(), Dir: t.TempDir(), Base: srv.URL}
	ctx := context.Background()
	for range 5 {
		if _, err := src.Lookup(ctx, Query{Console: "snes", CRCs: []uint32{0xAAAA0002}}); err != nil {
			t.Fatal(err)
		}
		if _, err := src.Lookup(ctx, Query{Console: "n64", CRCs: []uint32{0xCCCC0001}}); err != nil {
			t.Fatal(err)
		}
	}
	// snes has three lists (two not on the server), n64 one: each read once.
	if src.reads > 2 {
		t.Errorf("lists read %d times for two consoles, want each once", src.reads)
	}
}

// A list not downloaded yet is used as it is by a scan's lookups, which may not download it,
// instead of being read again for every game; a lookup that may download still gets the rest.
func TestLibretroPartialListKeptForOfflineLookups(t *testing.T) {
	var hits atomic.Int32
	srv := rdbServer(t, &hits, snesFile())
	dir := t.TempDir()
	online := &Libretro{HTTP: srv.Client(), Dir: dir, Base: srv.URL}
	// Download only the main list, as the worker would have.
	if _, err := online.fetchFile(context.Background(), "Nintendo - Super Nintendo Entertainment System"); err != nil {
		t.Fatal(err)
	}
	src := &Libretro{HTTP: srv.Client(), Dir: dir, Base: srv.URL}
	off := Offline(context.Background())
	for range 10 {
		if _, err := src.Lookup(off, Query{Console: "snes", CRCs: []uint32{0xAAAA0002}}); err != nil {
			t.Fatal(err)
		}
	}
	if src.reads != 1 {
		t.Errorf("a partial list was read %d times by offline lookups, want once", src.reads)
	}
	before := hits.Load()
	if _, err := src.Lookup(context.Background(), Query{Console: "snes", CRCs: []uint32{0xAAAA0002}}); err != nil {
		t.Fatal(err)
	}
	if hits.Load() == before {
		t.Error("a lookup that may download didn't ask for the lists still missing")
	}
}
