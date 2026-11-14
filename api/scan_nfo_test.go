// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
)

// A library that doesn't read other programs' NFOs gets nothing from one beside a game, and one
// this server wrote is read either way.
func TestDescribeKeepsToTheLibrarysNFOSetting(t *testing.T) {
	// The libraries' settings are kept for a while: start without another test's.
	nfoMu.Lock()
	nfoLibs = nil
	nfoMu.Unlock()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == host.Base+"/libraries" {
			_, _ = w.Write([]byte(`{"libraries":[{"id":1,"kind":"games","foreignNfo":false},{"id":2,"kind":"games","foreignNfo":true}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	a, _ := testApp(t, host.NewAt(srv.URL, "t", nil))
	dir := t.TempDir()
	game := filepath.Join(dir, "Zelda (USA).sfc")
	_ = os.WriteFile(game, []byte("x"), 0o644)
	nfo := filepath.Join(dir, "Zelda (USA).nfo")
	_ = os.WriteFile(nfo, []byte(`<game><title>From another program</title></game>`), 0o644)
	it := wittymodule.ScanDescribeItem{ID: 5, Kind: "game", Title: "Zelda", Files: []string{game}}
	ctx := context.Background()
	if f, _ := a.localGameFields(ctx, it, "snes", a.foreignNFO(ctx, 1)); f.Title != "" {
		t.Fatalf("library 1 read another program's NFO: %q", f.Title)
	}
	if f, _ := a.localGameFields(ctx, it, "snes", a.foreignNFO(ctx, 2)); f.Title != "From another program" {
		t.Fatalf("library 2 didn't read it: %q", f.Title)
	}
	_ = os.WriteFile(nfo, []byte(nfoMarker+"\n<game><title>Ours</title></game>"), 0o644)
	if f, _ := a.localGameFields(ctx, it, "snes", a.foreignNFO(ctx, 1)); f.Title != "Ours" {
		t.Fatalf("the server's own NFO wasn't read: %q", f.Title)
	}
}
