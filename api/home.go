// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/stream"
)

// The games home screen in one answer, and a game's check (what it needs and whether this
// server has it). Moved from the server's api_games_home.go (handleGamesHome, handleGameCheck).
//
// The home screen's games come from the server's host API, which lists only the games the person
// asking may see (it replaces the server's access rules on each list). What the host API gives is
// an item's id, library, kind, title and console (service.Item), not the server's whole
// library.Item: so there are no posters, ratings, genres or series here, no watch progress, and
// the "recent" shelf is the newest games by id (the order they were added in). Until the host API
// offers more, "shelves" is empty and the hero is picked without looking for a cover.

const (
	homeShelfGames = 24   // games on a shelf
	homeHero       = 15   // covers in the hero
	homeLibraryMax = 5000 // games read from one library, at most, for one home screen
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

type gameHomeContinue struct {
	Item       *service.Item `json:"item"`
	Plays      int           `json:"plays"`
	LastPlayed int64         `json:"lastPlayed"`
	Save       *gameHomeSave `json:"save,omitempty"` // where it would resume from
}

type gameHomeShelf struct {
	Kind  string          `json:"kind"` // genre, or series
	Name  string          `json:"name"`
	Count int             `json:"count"`
	Items []*service.Item `json:"items"`
}

// libraryGames are one library's games that a person may see, by id, newest first as a list.
func (a *App) libraryGames(ctx context.Context, on host.On, library int64) (map[int64]*service.Item, []*service.Item, error) {
	byID := map[int64]*service.Item{}
	list := []*service.Item{}
	const page = 500
	for offset := 0; offset < homeLibraryMax; offset += page {
		items, err := a.Host.Items(ctx, on, host.Query{Library: library, Kind: "game", Offset: offset, Limit: page})
		if err != nil {
			if offset == 0 {
				return nil, nil, err
			}
			break
		}
		for i := range items {
			it := &items[i]
			it.Files = nil // where it lives on disk is nobody's business here
			if byID[it.ID] == nil {
				byID[it.ID] = it
				list = append(list, it)
			}
		}
		if len(items) < page {
			break
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
	return byID, list, nil
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
	byID, all, err := a.libraryGames(ctx, host.For(r), lib)
	if err != nil {
		a.Log.WarnContext(ctx, "games home: the library's games could not be listed", "cat", "games", "err", err)
		writeErr(w, 502, "the server's games could not be listed")
		return
	}
	// Someone's own lists, in their own order, with what they can no longer see (or what isn't
	// in this library) left out.
	mine := func(entries []gameListEntry) []*service.Item {
		out := make([]*service.Item, 0, len(entries))
		for _, e := range entries {
			if it := byID[e.ItemID]; it != nil {
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

	cont := []gameHomeContinue{}
	for _, e := range played {
		if len(cont) >= 12 {
			break
		}
		if it := byID[e.ItemID]; it != nil {
			cont = append(cont, gameHomeContinue{Item: it, Plays: e.Plays, LastPlayed: e.LastPlayed, Save: resumeSave(a.saveDir(user, it.ID))})
		}
	}
	favItems := mine(favs)
	favIDs := make([]int64, 0, len(favItems))
	for _, it := range favItems {
		favIDs = append(favIDs, it.ID)
	}
	recent := capItems(all, homeShelfGames)
	writeJSON(w, 200, map[string]any{
		"hero":         pickHero(cont, capItems(favItems, homeShelfGames), all, recent),
		"continue":     cont,
		"recent":       recent,
		"favourites":   capItems(favItems, homeShelfGames),
		"favouriteIds": favIDs,
		"mostPlayed":   capItems(mine(most), homeShelfGames),
		"shelves":      []gameHomeShelf{}, // genres and series aren't in the host API yet
	})
}

func capItems(items []*service.Item, n int) []*service.Item {
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

// pickHero chooses the games at the top of the page: what someone's playing, what they love,
// then the rest of the library, which turns over each day so it doesn't go stale. (The server
// took the best rated, with covers; the host API says neither, so it's every game here.)
func pickHero(cont []gameHomeContinue, favs, others, recent []*service.Item) []*service.Item {
	out := make([]*service.Item, 0, homeHero)
	seen := map[int64]bool{}
	add := func(it *service.Item, limit int) {
		if !seen[it.ID] && len(out) < limit {
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
	shuffled := append([]*service.Item{}, others...)
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
