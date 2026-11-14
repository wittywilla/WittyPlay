// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/stream"
)

// The games home screen in one answer, and a game's check (what it needs and whether this
// server has it). Moved from the server's api_games_home.go (handleGamesHome, handleGameCheck).
//
// The home screen's games come from the server's host API (ItemsFull, Facets): the server's own
// item JSON, posters, ratings and watch progress included, for the games the person asking may see.
// So the answer is the one the apps were written against; only what someone played, loves and plays
// most is WittyPlay's own (gameList).

const (
	homeShelfGames = 24 // games on a shelf
	homeHero       = 15 // covers in the hero
	homeShelves    = 6  // genre shelves, and series shelves, at most
)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/home", w(handleGamesHome))
		mux.Handle("GET /games/{id}/check", w(handleGameCheck))
	})
}

type gameHomeSave struct {
	Slot      int   `json:"slot"`
	UpdatedAt int64 `json:"updatedAt"`
	HasThumb  bool  `json:"hasThumb"`
}

// homeItem is one of the server's items as it gave it, with what this needs to know about it.
type homeItem struct {
	ID     int64
	poster bool
	raw    json.RawMessage
}

func (h *homeItem) MarshalJSON() ([]byte, error) { return h.raw, nil }

type gameHomeContinue struct {
	Item       *homeItem     `json:"item"`
	Plays      int           `json:"plays"`
	LastPlayed int64         `json:"lastPlayed"`
	Save       *gameHomeSave `json:"save,omitempty"` // where it would resume from
}

type gameHomeShelf struct {
	Kind  string      `json:"kind"` // genre, or series
	Name  string      `json:"name"`
	Count int         `json:"count"`
	Items []*homeItem `json:"items"`
}

// homeList asks the server for one library's games, as a person may see them.
func (a *App) homeList(ctx context.Context, on host.On, q host.Query) []*homeItem {
	q.Kind = "game"
	raws, err := a.Host.ItemsFull(ctx, on, q)
	if err != nil {
		a.Log.WarnContext(ctx, "games home: a list failed", "cat", "games", "err", err)
		return []*homeItem{}
	}
	out := make([]*homeItem, 0, len(raws))
	for _, raw := range raws {
		var head struct {
			ID     int64  `json:"id"`
			Poster string `json:"poster"`
		}
		if json.Unmarshal(raw, &head) == nil && head.ID > 0 {
			out = append(out, &homeItem{ID: head.ID, poster: head.Poster != "", raw: raw})
		}
	}
	return out
}

// GET /games/home?library=
func handleGamesHome(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lib := int64(queryInt(r, "library", 0))
	if lib <= 0 {
		writeErr(w, 400, "which library?")
		return
	}
	user := who(r).User
	on := host.For(r)
	list := func(q host.Query) []*homeItem {
		q.Library = lib
		return a.homeList(ctx, on, q)
	}
	// Someone's own lists, in their own order, with what they can no longer see (or what isn't
	// in this library) left out.
	mine := func(entries []gameListEntry) []*homeItem {
		ids := make([]int64, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.ItemID)
		}
		found := map[int64]*homeItem{}
		if len(ids) > 0 {
			for _, it := range list(host.Query{IDs: ids, Limit: len(ids)}) {
				found[it.ID] = it
			}
		}
		out := make([]*homeItem, 0, len(ids))
		for _, id := range ids {
			if it := found[id]; it != nil {
				out = append(out, it)
			}
		}
		return out
	}
	played, err := a.gameList(ctx, user, false, false, 60)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	favs, err := a.gameList(ctx, user, true, false, 60)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	most, err := a.gameList(ctx, user, false, true, 60)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	entryOf := map[int64]gameListEntry{}
	for _, e := range played {
		entryOf[e.ItemID] = e
	}
	cont := []gameHomeContinue{}
	for _, it := range mine(played) {
		if len(cont) >= 12 {
			break
		}
		e := entryOf[it.ID]
		cont = append(cont, gameHomeContinue{Item: it, Plays: e.Plays, LastPlayed: e.LastPlayed, Save: resumeSave(a.saveDir(user, it.ID))})
	}
	allFavs := mine(favs)
	favItems := capItems(allFavs, homeShelfGames)
	mostItems := capItems(mine(most), homeShelfGames)
	recent := list(host.Query{Sort: "added", Desc: true, Limit: homeShelfGames})
	rated := list(host.Query{Sort: "rating", Desc: true, MinRating: 0.1, Limit: 60})

	shelves := []gameHomeShelf{}
	if raw, err := a.Host.Facets(ctx, on, lib); err == nil {
		var facets struct {
			Genres []struct {
				Value string `json:"value"`
				Count int    `json:"count"`
			} `json:"genres"`
			Series []struct {
				Value string `json:"value"`
				Count int    `json:"count"`
			} `json:"series"`
		}
		if json.Unmarshal(raw, &facets) == nil {
			for i, g := range facets.Genres {
				if i >= homeShelves || g.Count < 4 {
					break
				}
				// Best rated first (the server's "popular" order counted plays, which are WittyPlay's now).
				shelves = append(shelves, gameHomeShelf{Kind: "genre", Name: g.Value, Count: g.Count, Items: list(host.Query{Genre: g.Value, Sort: "rating", Desc: true, Limit: homeShelfGames})})
			}
			for i, f := range facets.Series {
				if i >= homeShelves || f.Count < 2 {
					break
				}
				shelves = append(shelves, gameHomeShelf{Kind: "series", Name: f.Value, Count: f.Count, Items: list(host.Query{Series: f.Value, Sort: "oldest", Limit: homeShelfGames})})
			}
		}
	}

	favIDs := make([]int64, 0, len(allFavs))
	for _, it := range allFavs {
		favIDs = append(favIDs, it.ID)
	}
	writeJSON(w, 200, map[string]any{
		"hero":         pickHero(cont, favItems, rated, recent),
		"continue":     cont,
		"recent":       recent,
		"favourites":   favItems,
		"favouriteIds": favIDs,
		"mostPlayed":   mostItems,
		"shelves":      shelves,
	})
}

func capItems(items []*homeItem, n int) []*homeItem {
	if len(items) > n {
		return items[:n]
	}
	return items
}

// resumeSave looks for the automatic save among a game's files.
func resumeSave(dir string) *gameHomeSave {
	name, err := stream.SlotName(0)
	if err != nil {
		return nil
	}
	st, err := os.Stat(filepath.Join(dir, name+".state"))
	if err != nil {
		return nil
	}
	_, thumb := os.Stat(filepath.Join(dir, name+".png"))
	return &gameHomeSave{Slot: 0, UpdatedAt: st.ModTime().Unix(), HasThumb: thumb == nil}
}

// pickHero chooses the covers at the top of the page: what someone's playing, what they love,
// then the best rated, which turn over each day so they don't go stale, then the newest.
func pickHero(cont []gameHomeContinue, favs, rated, recent []*homeItem) []*homeItem {
	out := make([]*homeItem, 0, homeHero)
	seen := map[int64]bool{}
	add := func(it *homeItem, limit int) {
		if it.poster && !seen[it.ID] && len(out) < limit {
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	for _, c := range cont {
		add(c.Item, 3)
	}
	for _, it := range favs {
		add(it, 6)
	}
	shuffled := append([]*homeItem{}, rated...)
	day := time.Now().Unix() / 86400
	rand.New(rand.NewSource(day)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for _, it := range shuffled {
		add(it, homeHero-2)
	}
	for _, it := range recent {
		add(it, homeHero)
	}
	return out
}

// GET /games/{id}/check says what a game needs and whether this server has it, in time for the
// page to say so rather than the Play button finding out. The server checked the person may see
// it (the route's "item"); the answer is group C's CheckGame, the server's gameCheck.
func handleGameCheck(a *App, w http.ResponseWriter, r *http.Request) {
	it := gameFromRoute(a, w, r)
	if it == nil {
		return
	}
	// An unknown console (or the test one, outside testing) answers ready: false, reason unknown.
	writeJSON(w, 200, a.CheckGame(r.Context(), it, console(itemConsole(it))))
}
