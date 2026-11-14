// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
)

// The people on a fake server: an administrator, two adults, a Kids profile and another, and a
// guest. alex and sam were seen just now.
var (
	owner = service.Identity{User: 1, Kind: "adult", Admin: true}
	alex  = service.Identity{User: 2, Kind: "adult"}
	sam   = service.Identity{User: 3, Kind: "adult"}
	kit   = service.Identity{User: 4, Kind: "kids"}
	kira  = service.Identity{User: 5, Kind: "kids"}
	gus   = service.Identity{User: 6, Kind: "guest"}
)

// personHost is a server's host API as far as these routes ask: games 7 (snes) and 12 (which the
// Kids profiles may not see), every account, and its audit log.
func personHost(t *testing.T, audits *atomic.Int32) *host.Client {
	t.Helper()
	now := time.Now().Unix()
	users := []host.User{
		{ID: 1, Name: "owner", Kind: "adult", Admin: true},
		{ID: 2, Name: "alex", Kind: "adult", LastSeen: now},
		{ID: 3, Name: "sam", Kind: "adult", LastSeen: now},
		{ID: 4, Name: "kit", Kind: "kids"},
		{ID: 5, Name: "kira", Kind: "kids"},
		{ID: 6, Name: "gus", Kind: "guest"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		on, _ := strconv.ParseInt(r.Header.Get(host.HeaderOnUser), 10, 64)
		switch {
		case p == "/items/7":
			_ = json.NewEncoder(w).Encode(service.Item{ID: 7, Kind: "game", Title: "A game", Facets: map[string]string{"console": "snes"},
				Files: []service.File{{Path: "/games/a.sfc", Size: 1}}})
		case p == "/items/12" && on != 4 && on != 5:
			_ = json.NewEncoder(w).Encode(service.Item{ID: 12, Kind: "game", Title: "Grown-up game", Facets: map[string]string{"console": "snes"}})
		case p == "/items/9":
			_ = json.NewEncoder(w).Encode(service.Item{ID: 9, Kind: "movie", Title: "A film"})
		case p == "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
		case p == "/audit":
			if audits != nil {
				audits.Add(1)
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such thing"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

// as calls a route as someone, as the server would after checking who they are.
func as(t *testing.T, h http.Handler, method, path string, id service.Identity, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(service.HeaderUser, strconv.FormatInt(id.User, 10))
	r.Header.Set(service.HeaderKind, id.Kind)
	if id.Admin {
		r.Header.Set(service.HeaderAdmin, "1")
	}
	if id.Item != nil {
		r.Header.Set(service.HeaderItem, service.EncodeItem(*id.Item))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
}

func TestGameMetaIsEachPersonsOwn(t *testing.T) {
	_, h := testApp(t, personHost(t, nil))
	if w := as(t, h, "PUT", "/games/7/meta", alex, map[string]any{"favourite": true, "note": "private"}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := as(t, h, "GET", "/games/7/meta", sam, nil); strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"favourite":true`) {
		t.Errorf("another person read the note: %s", w.Body)
	}
	var m GamePersonal
	decode(t, as(t, h, "GET", "/games/7/meta", alex, nil), &m)
	if !m.Favourite || m.Note != "private" || m.HasBattery {
		t.Errorf("%+v", m)
	}
	if w := as(t, h, "PUT", "/games/7/meta", alex, map[string]any{"note": strings.Repeat("x", maxGameNote+1)}); w.Code != 400 {
		t.Errorf("a note too long: %d", w.Code)
	}
	for _, path := range []string{"/games/8/meta", "/games/9/meta", "/games/12/meta"} {
		if w := as(t, h, "GET", path, kit, nil); w.Code != 404 {
			t.Errorf("%s for a Kids profile: %d", path, w.Code)
		}
	}
	// What the server says about the route's item is taken as it is.
	withItem := alex
	withItem.Item = &service.Item{ID: 7, Kind: "game", Title: "A game"}
	if w := as(t, h, "GET", "/games/7/meta", withItem, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "private") {
		t.Errorf("with X-Witty-Item: %d %s", w.Code, w.Body)
	}
}

func TestPlayCountsAndRecentGames(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	for range 3 {
		if err := a.noteGamePlayed(t.Context(), alex.User, 7); err != nil {
			t.Fatal(err)
		}
	}
	_ = a.noteGamePlayed(t.Context(), alex.User, 12)
	var m GamePersonal
	decode(t, as(t, h, "GET", "/games/7/meta", alex, nil), &m)
	if m.Plays != 3 || m.LastPlayed == 0 {
		t.Fatalf("%+v", m)
	}
	w := as(t, h, "GET", "/games/recent", alex, nil)
	if !strings.Contains(w.Body.String(), `"plays":3`) || !strings.Contains(w.Body.String(), "Grown-up game") || strings.Contains(w.Body.String(), "/games/a.sfc") {
		t.Fatalf("%s", w.Body)
	}
	if w := as(t, h, "GET", "/games/recent?favourites=1", alex, nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("no favourites yet: %s", w.Body)
	}
	as(t, h, "PUT", "/games/7/meta", alex, map[string]any{"favourite": true})
	if w := as(t, h, "GET", "/games/recent?favourites=1", alex, nil); !strings.Contains(w.Body.String(), `"title":"A game"`) {
		t.Errorf("their own favourites didn't show: %s", w.Body)
	}
	if w := as(t, h, "GET", "/games/recent?favourites=1", sam, nil); strings.Contains(w.Body.String(), `"title"`) {
		t.Errorf("another person's favourites showed: %s", w.Body)
	}
	// A game someone can no longer see leaves their list.
	_ = a.noteGamePlayed(t.Context(), kit.User, 12)
	if w := as(t, h, "GET", "/games/recent", kit, nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("a game the profile may not see: %s", w.Body)
	}
	most, err := a.gameList(t.Context(), alex.User, false, true, 10)
	if err != nil || len(most) != 2 || most[0].ItemID != 7 {
		t.Errorf("most played: %+v %v", most, err)
	}
}
