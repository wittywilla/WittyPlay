// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/stream"
)

// A host with one games library (1): games 10 and 11 everyone sees, and 12 only user 3. Game 20
// is in another library.
func homeHost(t *testing.T) *host.Client {
	t.Helper()
	games := []service.Item{
		{ID: 10, Library: 1, Kind: "game", Title: "Ten", Facets: map[string]string{"console": wittyplay.TestConsole}, Files: []service.File{{Path: "/games/ten.wittytest"}}},
		{ID: 11, Library: 1, Kind: "game", Title: "Eleven", Facets: map[string]string{"console": "nosuch"}},
		{ID: 12, Library: 1, Kind: "game", Title: "Twelve", Facets: map[string]string{"console": "snes"}},
		{ID: 20, Library: 2, Kind: "game", Title: "Twenty", Facets: map[string]string{"console": "snes"}},
	}
	sees := func(r *http.Request, it service.Item) bool {
		return it.ID != 12 || r.Header.Get(host.HeaderOnUser) == "3"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		switch {
		case p == "/items":
			lib, _ := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
			out := []service.Item{}
			if r.URL.Query().Get("offset") == "" {
				for _, it := range games {
					if it.Library == lib && sees(r, it) {
						it.Files = nil
						out = append(out, it)
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
		case strings.HasPrefix(p, "/items/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/items/"), 10, 64)
			for _, it := range games {
				if it.ID == id && sees(r, it) {
					_ = json.NewEncoder(w).Encode(it)
					return
				}
			}
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such item"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

type homeAnswer struct {
	Hero         []service.Item `json:"hero"`
	Recent       []service.Item `json:"recent"`
	Favourites   []service.Item `json:"favourites"`
	FavouriteIDs []int64        `json:"favouriteIds"`
	MostPlayed   []service.Item `json:"mostPlayed"`
	Continue     []struct {
		Item  service.Item  `json:"item"`
		Plays int           `json:"plays"`
		Save  *gameHomeSave `json:"save"`
	} `json:"continue"`
	Shelves []gameHomeShelf `json:"shelves"`
}

func TestGamesHome(t *testing.T) {
	a, h := testApp(t, homeHost(t))
	if w := call(t, h, "GET", "/games/home", 4, nil); w.Code != 400 {
		t.Errorf("no library: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/home?library=1", 0, nil); w.Code != 401 {
		t.Errorf("signed out: %d", w.Code)
	}
	ctx := t.Context()
	// User 4 plays 10 (with an automatic save) and favours 11; and 20, in another library, and
	// 12, which they may not see.
	for _, id := range []int64{10, 20, 12} {
		if err := a.noteGamePlayed(ctx, 4, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.setGameMetaColumn(ctx, 4, 11, "favourite", 1); err != nil {
		t.Fatal(err)
	}
	slot, _ := stream.SlotName(0)
	dir := a.saveDir(4, 10)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, slot+".state"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(user int64) (homeAnswer, string) {
		t.Helper()
		w := call(t, h, "GET", "/games/home?library=1", user, nil)
		if w.Code != 200 {
			t.Fatalf("home: %d %s", w.Code, w.Body)
		}
		var out homeAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out, w.Body.String()
	}
	mine, body := read(4)
	for _, key := range []string{`"hero":`, `"continue":`, `"recent":`, `"favourites":`, `"favouriteIds":`, `"mostPlayed":`, `"shelves":[]`} {
		if !strings.Contains(body, key) {
			t.Errorf("the answer has no %s: %s", key, body)
		}
	}
	if len(mine.Recent) != 2 || mine.Recent[0].ID != 11 || mine.Recent[1].ID != 10 {
		t.Errorf("recent, newest first, only what they see: %+v", mine.Recent)
	}
	if len(mine.Continue) != 1 || mine.Continue[0].Item.ID != 10 || mine.Continue[0].Plays != 1 || mine.Continue[0].Save == nil || mine.Continue[0].Save.HasThumb {
		t.Errorf("continue: %+v", mine.Continue)
	}
	if len(mine.Favourites) != 1 || mine.Favourites[0].ID != 11 || len(mine.FavouriteIDs) != 1 || mine.FavouriteIDs[0] != 11 {
		t.Errorf("favourites: %+v %v", mine.Favourites, mine.FavouriteIDs)
	}
	if len(mine.MostPlayed) != 1 || mine.MostPlayed[0].ID != 10 {
		t.Errorf("most played: %+v", mine.MostPlayed)
	}
	if len(mine.Hero) != 2 || mine.Hero[0].ID != 10 || mine.Hero[1].ID != 11 {
		t.Errorf("hero: what they play, then what they love: %+v", mine.Hero)
	}
	if strings.Contains(body, `"path"`) || strings.Contains(body, "/games/") {
		t.Error("the home screen showed a file's path")
	}
	// Someone else sees none of user 4's own lists, and 12 is theirs to see.
	theirs, _ := read(3)
	if len(theirs.Favourites) != 0 || len(theirs.FavouriteIDs) != 0 || len(theirs.Continue) != 0 || len(theirs.Recent) != 3 {
		t.Errorf("another person's home: %+v", theirs)
	}
}

func TestGameCheck(t *testing.T) {
	t.Setenv("WMS_GAMING_TESTCORE", "1")
	_, h := testApp(t, homeHost(t))
	w := call(t, h, "GET", "/games/10/check", 4, nil)
	var c map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c["ready"] != true || c["console"] != wittyplay.TestConsole || c["bios"] == nil {
		t.Errorf("the test console should always be ready: %d %s", w.Code, w.Body)
	}
	w = call(t, h, "GET", "/games/11/check", 4, nil)
	c = nil
	_ = json.Unmarshal(w.Body.Bytes(), &c)
	if w.Code != 200 || c["ready"] != false || c["reason"] != "unknown" {
		t.Errorf("an unknown console: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/games/999999/check", 4, nil); w.Code != 404 {
		t.Errorf("a game that isn't there: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/12/check", 4, nil); w.Code != 404 {
		t.Errorf("a game they may not see: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/10/check", 0, nil); w.Code != 401 {
		t.Errorf("signed out: %d", w.Code)
	}
}
