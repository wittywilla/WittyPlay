// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
)

// What the scans of games libraries going on now have found so far. The server keeps a scan's
// games only once WittyPlay has sorted every file, which on a big combined folder takes minutes:
// meanwhile the consoles, the empty shelves and TV mode say that games are being found, how
// many, and for which consoles, rather than that there's no games library (GET /games/scanning).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/scanning", w(handleGamesScanning))
	})
}

// gamesFinding is one games library's scan going on now, as far as it has got.
type gamesFinding struct {
	Library int64  `json:"library"`
	Name    string `json:"name"`
	// Found is how many games the files sorted so far hold, and Consoles how many for each.
	Found    int            `json:"found"`
	Consoles map[string]int `json:"consoles"`
	// Files is how many of the library's files WittyPlay has been sent, of Total (0: not known).
	Files int `json:"files"`
	Total int `json:"total"`
}

// findingGames is what the games libraries this profile may open that are being scanned now
// have found so far, in the order the server lists them. Nil when none is.
func (a *App) findingGames(ctx context.Context, on host.On) []gamesFinding {
	if !anyScanning() || a.Host == nil {
		return nil
	}
	libs, err := a.Host.Libraries(ctx)
	if err != nil {
		return nil
	}
	var out []gamesFinding
	for _, l := range libs {
		if l.Kind != "games" {
			continue
		}
		s := scanning(l.ID)
		if s == nil {
			continue
		}
		if _, err := a.Host.CountsIn(ctx, on, l.ID, "console"); err != nil {
			continue // a library this person can't open
		}
		by, n := s.Found()
		files, total, _ := s.Progress()
		out = append(out, gamesFinding{Library: l.ID, Name: l.Name, Found: n, Consoles: by, Files: files, Total: total})
	}
	return out
}

// anyScanning says whether WittyPlay is in the middle of any scan, without asking the server.
func anyScanning() bool {
	scans.Lock()
	defer scans.Unlock()
	return len(scans.m) > 0
}

func handleGamesScanning(a *App, w http.ResponseWriter, r *http.Request) {
	out := a.findingGames(r.Context(), host.For(r))
	if out == nil {
		out = []gamesFinding{}
	}
	writeJSON(w, 200, map[string]any{"scanning": out})
}

// findingItems are cards for the libraries being scanned, for a shelf that would otherwise be
// empty: "Finding your games", "1,204 found so far in Games", each opening its library.
func findingItems(found []gamesFinding) []sdui.Item {
	out := make([]sdui.Item, 0, len(found))
	for _, f := range found {
		sub := "Looking through " + f.Name + "…"
		if f.Found > 0 {
			sub = fmt.Sprintf("%s found so far in %s", countWords(f.Found), f.Name)
		}
		out = append(out, sdui.Item{ID: "finding-" + strconv.FormatInt(f.Library, 10), Kind: "view", Title: "Finding your games", Subtitle: sub,
			Icon: "game", Route: "/library/" + strconv.FormatInt(f.Library, 10)})
	}
	return out
}

// foundConsoles adds up what the scans going on have found for each console, and the library
// that found the most of each.
func foundConsoles(found []gamesFinding) (count map[string]int, home map[string]int64) {
	count, home = map[string]int{}, map[string]int64{}
	most := map[string]int{}
	for _, f := range found {
		for c, n := range f.Consoles {
			count[c] += n
			if n > most[c] {
				most[c], home[c] = n, f.Library
			}
		}
	}
	return count, home
}

// countWords writes a count with commas: 2,293.
func countWords(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
