// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/gamemeta"
)

// Refresh game details: an administrator's one click, for a whole games library or for one game,
// that looks the games up again from the start: their titles (in their own script and in
// English), box art, screenshots and logos, and the rest of their details. Every source is asked
// again (what each said before is forgotten), and every picture is downloaded again. What people
// chose stays: a match picked by hand keeps its game, a locked match stays locked, and a field
// someone typed in the item editor is never changed (the worker leaves it alone, and the server
// refuses to change it anyway). The worker does the work in the background and shows it as its
// host task ("Finding out about games"); GET says how far it has got.
//
// The same refresh is what scan.describe does with refresh (the item editor's Refresh, and a Full
// rescan), so every way of asking gives the same answer.
//
// POST /libraries/{id}/details/refresh  → 202 {started, games}         (administrators)
// GET  /libraries/{id}/details/refresh  → refreshState                 (administrators)
// POST /games/{id}/metadata/refresh     → 202 {started}                (administrators; gamedetails.go)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("POST /libraries/{id}/details/refresh", w(adminRoute(handleLibraryRefresh)))
		mux.Handle("GET /libraries/{id}/details/refresh", w(adminRoute(handleLibraryRefreshState)))
	})
}

// refreshChunk is how many games one statement changes: SQLite's limit on parameters is far above.
const refreshChunk = 400

// refreshStarted is when each library's refresh was asked for, for how long it has been going.
var refreshStarted sync.Map // library id → time.Time

// refreshDetails sends games to be looked up again from the start: every source's answer but a
// person's own choice is forgotten, and their pictures are downloaded again. reread also reads
// each file again (its checksum and serial), as a Full rescan and the item editor's Refresh do.
func (g *gameMeta) refreshDetails(ctx context.Context, ids []int64, reread bool) error {
	for i := 0; i < len(ids); i += refreshChunk {
		chunk := ids[i:min(i+refreshChunk, len(ids))]
		in := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for k, id := range chunk {
			args[k] = id
		}
		// The pictures kept for them go, so they're downloaded again.
		rows, err := g.a.DB.QueryContext(ctx, `SELECT poster_ref, backdrop_ref FROM wittyplay_game_details WHERE item_id IN (`+in+`)`, args...)
		if err != nil {
			return err
		}
		var refs []string
		for rows.Next() {
			var p, b string
			if rows.Scan(&p, &b) == nil {
				refs = append(refs, p, b)
			}
		}
		rows.Close()
		g.forgetPictures(refs)
		if _, err := g.a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE method <> 'manual' AND item_id IN (`+in+`)`, args...); err != nil {
			return err
		}
		set := `status = 'pending', next_try = 0, tries = 0, poster_ref = '', backdrop_ref = '', poster_url = '', backdrop_url = '', art_tries = 0, art_next = 0`
		if reread {
			set += `, file_size = -1`
		}
		if _, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET `+set+` WHERE item_id IN (`+in+`)`, args...); err != nil {
			return err
		}
	}
	g.Kick()
	return nil
}

// forgetPictures removes pictures from WittyPlay's cache, by their references.
func (g *gameMeta) forgetPictures(refs []string) {
	dir := g.a.gameImagesDir()
	for _, ref := range refs {
		if ref == "" {
			continue
		}
		for ext := range pictureExtSet {
			_ = os.Remove(filepath.Join(dir, gameImageName(ref, ext)))
		}
	}
}

// pictureExtSet is every extension a cached picture can have (fetchPicture).
var pictureExtSet = map[string]bool{".jpg": true, ".png": true, ".webp": true, ".gif": true}

// libraryGameIDs are the games WittyPlay knows in a library.
func (a *App) libraryGameIDs(ctx context.Context, library int64) ([]int64, error) {
	return a.gameIDs(ctx, `SELECT item_id FROM wittyplay_game_details WHERE library_id = ? AND path <> '' ORDER BY item_id`, library)
}

// refreshState is how far refreshing a library's games has got, and what came back.
type refreshState struct {
	Games int `json:"games"`
	// Waiting: games still to be looked up now; Later: those waiting for a source that asked to
	// be left alone for a while; Pictures: games whose pictures are still to download.
	Waiting  int  `json:"waiting"`
	Later    int  `json:"later"`
	Pictures int  `json:"pictures"`
	Running  bool `json:"running"`
	// What's known now: games with a title in their own script, with an English title, and with
	// a logo (the game's name drawn as its logo).
	Native  int `json:"native"`
	English int `json:"english"`
	Logos   int `json:"logos"`
	// Since: when this refresh was asked for (Unix seconds), 0 for none since WittyPlay started.
	Since int64 `json:"since"`
}

// libraryRefreshState counts a library's games' refresh. The worker's queue is shared by every
// library: Running says whether any of this library's games are still to do.
func (a *App) libraryRefreshState(ctx context.Context, library int64) (refreshState, error) {
	var s refreshState
	now := time.Now().Unix()
	err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*), IFNULL(SUM(status = 'pending' AND next_try <= ?), 0), IFNULL(SUM(status = 'pending' AND next_try > ?), 0),
		IFNULL(SUM(art_wanted = 1), 0), IFNULL(SUM(art LIKE '%"kind":"logo"%'), 0)
		FROM wittyplay_game_details WHERE library_id = ? AND path <> ''`, now, now, library).Scan(&s.Games, &s.Waiting, &s.Later, &s.Pictures, &s.Logos)
	if err != nil {
		return s, err
	}
	err = a.DB.QueryRowContext(ctx, `SELECT IFNULL(SUM(n), 0), IFNULL(SUM(e), 0) FROM (
		SELECT MAX(json_extract(s.data, '$.native') <> '') AS n, MAX(json_extract(s.data, '$.english') <> '') AS e
		FROM wittyplay_game_answers s JOIN wittyplay_game_details d ON d.item_id = s.item_id
		WHERE d.library_id = ? AND d.path <> '' AND s.data <> '' AND s.confidence >= ? GROUP BY s.item_id)`, library, gamemeta.RenameConfidence).Scan(&s.Native, &s.English)
	if err != nil {
		return s, err
	}
	s.Running = s.Waiting > 0 || s.Pictures > 0
	if t, ok := refreshStarted.Load(library); ok {
		s.Since = t.(time.Time).Unix()
	}
	return s, nil
}

// refreshWords is the state in a few words, for a row's end and a status line.
func refreshWords(s refreshState) string {
	switch {
	case s.Waiting > 0:
		return fmt.Sprintf("Looking up %s of %s", thousands(s.Games-s.Waiting-s.Later), thousands(s.Games))
	case s.Pictures > 0:
		return fmt.Sprintf("Downloading pictures: %s to go", thousands(s.Pictures))
	case s.Later > 0:
		return fmt.Sprintf("%s waiting for a source", thousands(s.Later))
	}
	return "Up to date"
}

func handleLibraryRefresh(a *App, w http.ResponseWriter, r *http.Request) {
	lib, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 404, "no such library")
		return
	}
	ctx := r.Context()
	ids, err := a.libraryGameIDs(ctx, lib)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(ids) == 0 {
		writeErr(w, 404, "WittyPlay knows no games in this library yet. Scan it first.")
		return
	}
	if err := a.gameWorker().refreshDetails(ctx, ids, false); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	refreshStarted.Store(lib, time.Now())
	a.Log.InfoContext(ctx, "wittyplay: refreshing game details", "cat", "games", "library", lib, "games", len(ids), "by", who(r).User)
	writeJSON(w, 202, map[string]any{"started": true, "games": len(ids)})
}

func handleLibraryRefreshState(a *App, w http.ResponseWriter, r *http.Request) {
	lib, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 404, "no such library")
		return
	}
	s, err := a.libraryRefreshState(r.Context(), lib)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, s)
}
