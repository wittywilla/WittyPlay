// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/store"
)

// A server's host API, as much as these routes ask of it.
func fakeHost(t *testing.T, legacy map[string][]string, files map[string][]byte) *host.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		switch {
		case p == "/items/7":
			_ = json.NewEncoder(w).Encode(service.Item{ID: 7, Kind: "game", Title: "A game", Facets: map[string]string{"console": "snes"}})
		case strings.HasPrefix(p, "/items/"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such item"}`))
		case strings.HasPrefix(p, "/legacy/"):
			rows := []json.RawMessage{}
			if r.URL.Query().Get("after") == "0" {
				for _, s := range legacy[strings.TrimPrefix(p, "/legacy/")] {
					rows = append(rows, json.RawMessage(s))
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"rows": rows, "next": len(rows)})
		case p == "/legacy-files/gaming/backgrounds":
			names := []string{}
			for n := range files {
				names = append(names, n)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": names})
		case strings.HasPrefix(p, "/legacy-files/gaming/backgrounds/"):
			_, _ = w.Write(files[strings.TrimPrefix(p, "/legacy-files/gaming/backgrounds/")])
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

func testApp(t *testing.T, h *host.Client) (*App, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{DB: db, Dir: dir, Host: h, Log: testLog()}
	appMu.Lock()
	shared = a
	appMu.Unlock()
	t.Cleanup(func() { appMu.Lock(); shared = nil; appMu.Unlock() })
	mux := http.NewServeMux()
	Routes(mux)
	return a, mux
}

func call(t *testing.T, h http.Handler, method, path string, user int64, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	if user > 0 {
		r.Header.Set(service.HeaderUser, itoa(user))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestControlsAndProfiles(t *testing.T) {
	_, h := testApp(t, fakeHost(t, nil, nil))
	if w := call(t, h, "GET", "/consoles/snes/controls", 0, nil); w.Code != 401 {
		t.Fatalf("nobody: %d", w.Code)
	}
	if w := call(t, h, "GET", "/consoles/nosuch/controls", 3, nil); w.Code != 404 {
		t.Fatalf("no console: %d", w.Code)
	}
	w := call(t, h, "PUT", "/consoles/snes/controls", 3, map[string]any{"kind": "touch", "key": "phone", "orientation": "landscape", "data": nil})
	if w.Code != 200 {
		t.Fatalf("removing nothing: %d %s", w.Code, w.Body)
	}
	w = call(t, h, "GET", "/consoles/snes/controls?device=phone&game=7", 3, nil)
	var c map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c["console"] != "snes" || c["game"].(float64) != 7 || c["pad"] == nil || c["defaults"] == nil {
		t.Fatalf("controls: %d %s", w.Code, w.Body)
	}
	// A profile for a game: its title comes from the server.
	w = call(t, h, "POST", "/game-profiles", 3, map[string]any{"name": "Mine", "kind": "gamepad", "controller": "045e:028e", "console": "snes", "game": 7, "active": true})
	var p map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 201 || p["gameTitle"] != "A game" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/game-profiles", 3, map[string]any{"name": "Other", "kind": "gamepad", "console": "snes", "game": 8}); w.Code != 400 {
		t.Fatalf("a game that isn't there: %d %s", w.Code, w.Body)
	}
	id := itoa(int64(p["id"].(float64)))
	if w := call(t, h, "GET", "/game-profiles/"+id, 4, nil); w.Code != 404 {
		t.Fatalf("someone else's profile: %d", w.Code)
	}
	if w := call(t, h, "GET", "/game-profiles/resolve?kind=gamepad&controller=045e:028e&console=snes&game=7", 3, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Mine"`) {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/game-controllers", 3, nil); w.Code != 200 {
		t.Fatalf("controllers: %d", w.Code)
	}
	if w := call(t, h, "GET", "/game-background", 3, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"fit":"cover"`) {
		t.Fatalf("background: %d %s", w.Code, w.Body)
	}
}

func TestForget(t *testing.T) {
	a, h := testApp(t, fakeHost(t, nil, nil))
	if w := call(t, h, "POST", "/game-profiles", 5, map[string]any{"name": "Mine", "kind": "gamepad", "console": "snes"}); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if err := writeFileAtomic(a.backgroundPath(5), []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	a.forget(5)
	if w := call(t, h, "GET", "/game-profiles", 5, nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("after the account went: %s", w.Body)
	}
	if _, err := os.Stat(a.backgroundPath(5)); err == nil {
		t.Fatal("the picture stayed")
	}
}

func testLog() *slog.Logger { return slog.New(slog.DiscardHandler) }
