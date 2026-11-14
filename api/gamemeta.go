// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
)

// What a person keeps about each game: a favourite mark, a note, how often and when they last
// played it, and their cheats (cheats.go). Moved from the server (api_game_options.go and
// library/game_options.go); the rows are in WittyPlay's own wittyplay_game_meta table now.
//
// The server's /api/v1/items "popular" sort read this table; it doesn't any more, since the
// table isn't the server's.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/recent", w(handleGamesRecent))
		mux.Handle("GET /games/{id}/meta", w(handleGameMeta))
		mux.Handle("PUT /games/{id}/meta", w(handleGameMetaPut))
	})
}

// maxGameNote is how long a person's note about a game may be.
const maxGameNote = 8 << 10

// personalGame is the game a /games/{id}/... route names, if the person may see it. The server
// has already checked it (the route's "item"), and says what it is in X-Witty-Item; without
// that (a route called some other way) the server is asked.
func (a *App) personalGame(r *http.Request) (*service.Item, bool) {
	id, ok := pathInt(r, "id")
	if !ok {
		return nil, false
	}
	if it := who(r).Item; it != nil && it.ID == id {
		return it, it.Kind == "game"
	}
	it, err := a.visibleGame(r.Context(), r, id)
	return it, err == nil
}

// itemConsole is the console a game item is for.
func itemConsole(it *service.Item) string {
	if it == nil || it.Facets == nil {
		return ""
	}
	return it.Facets["console"]
}

// personGameDir is one account's folder for one game in WittyPlay's own folder: built only from
// numbers, as the server's <data>/gaming/saves/u<user>/i<item> was. Saves, the battery save and
// the person's patches live in it.
func (a *App) personGameDir(userID, itemID int64) string {
	return filepath.Join(a.Dir, "saves", "u"+strconv.FormatInt(userID, 10), "i"+strconv.FormatInt(itemID, 10))
}

var uploadNameRe = regexp.MustCompile(`[^\p{L}\p{N} ._()\[\]-]+`)

// cleanUploadName makes a person's file name safe to show. It's only ever a label: files are
// stored under numbers.
func cleanUploadName(name, fallback string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(uploadNameRe.ReplaceAllString(name, ""))
	if len(name) > 120 {
		name = name[:120]
	}
	if name == "" || name == "." {
		return fallback
	}
	return name
}

// GamePersonal is what a person keeps about one game.
type GamePersonal struct {
	Favourite  bool              `json:"favourite"`
	Note       string            `json:"note"`
	Plays      int               `json:"plays"`
	LastPlayed int64             `json:"lastPlayed"` // seconds since 1970; 0 for never
	Cheats     []wittyplay.Cheat `json:"cheats"`
	HasBattery bool              `json:"hasBattery"` // it has a battery save to download
}

func (a *App) gameMeta(ctx context.Context, userID, itemID int64) (*GamePersonal, error) {
	m := &GamePersonal{Cheats: []wittyplay.Cheat{}}
	var fav int
	var cheats string
	err := a.DB.QueryRowContext(ctx, `SELECT favourite, note, plays, last_played, cheats FROM wittyplay_game_meta WHERE user_id = ? AND item_id = ?`, userID, itemID).
		Scan(&fav, &m.Note, &m.Plays, &m.LastPlayed, &cheats)
	if errors.Is(err, sql.ErrNoRows) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	m.Favourite = fav != 0
	if cheats != "" && json.Unmarshal([]byte(cheats), &m.Cheats) != nil {
		m.Cheats = []wittyplay.Cheat{}
	}
	return m, nil
}

// gameMetaWithBattery is gameMeta, saying too whether there's a battery save to download.
func (a *App) gameMetaWithBattery(ctx context.Context, userID, itemID int64) (*GamePersonal, error) {
	m, err := a.gameMeta(ctx, userID, itemID)
	if err != nil {
		return nil, err
	}
	_, statErr := os.Stat(filepath.Join(a.personGameDir(userID, itemID), "save.srm"))
	m.HasBattery = statErr == nil
	return m, nil
}

func (a *App) setGameMetaColumn(ctx context.Context, userID, itemID int64, column string, v any) error {
	switch column {
	case "favourite", "note", "cheats":
	default:
		return errors.New("not a column of a game's own data")
	}
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_game_meta(user_id, item_id, `+column+`) VALUES(?,?,?)
		ON CONFLICT(user_id, item_id) DO UPDATE SET `+column+` = excluded.`+column, userID, itemID, v)
	return err
}

// noteGamePlayed counts a game being started (for the group that starts games).
func (a *App) noteGamePlayed(ctx context.Context, userID, itemID int64) error {
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_game_meta(user_id, item_id, plays, last_played) VALUES(?,?,1,?)
		ON CONFLICT(user_id, item_id) DO UPDATE SET plays = plays + 1, last_played = excluded.last_played`, userID, itemID, time.Now().Unix())
	return err
}

// gameListEntry is one row of a person's recent or favourite games.
type gameListEntry struct {
	ItemID     int64
	Favourite  bool
	Plays      int
	LastPlayed int64
}

// gameList lists a person's most recently played games, or their favourites, newest first (or
// those played most, with mostPlayed). Whether they can still see each one is the caller's to
// check.
func (a *App) gameList(ctx context.Context, userID int64, favourites, mostPlayed bool, limit int) ([]gameListEntry, error) {
	q := `SELECT item_id, favourite, plays, last_played FROM wittyplay_game_meta WHERE user_id = ? AND last_played > 0 ORDER BY last_played DESC, item_id DESC LIMIT ?`
	switch {
	case favourites:
		q = `SELECT item_id, favourite, plays, last_played FROM wittyplay_game_meta WHERE user_id = ? AND favourite != 0 ORDER BY last_played DESC, item_id DESC LIMIT ?`
	case mostPlayed:
		q = `SELECT item_id, favourite, plays, last_played FROM wittyplay_game_meta WHERE user_id = ? AND plays > 0 ORDER BY plays DESC, last_played DESC LIMIT ?`
	}
	rows, err := a.DB.QueryContext(ctx, q, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []gameListEntry{}
	for rows.Next() {
		var e gameListEntry
		var fav int
		if err := rows.Scan(&e.ItemID, &fav, &e.Plays, &e.LastPlayed); err != nil {
			return nil, err
		}
		e.Favourite = fav != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// capBody holds a request's body to n bytes: the server's readJSON did so for every route.
func capBody(w http.ResponseWriter, r *http.Request, n int64) *http.Request {
	r.Body = http.MaxBytesReader(w, r.Body, n)
	return r
}

func queryInt(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

// GET /games/recent: a person's recently played games, or their favourites (?favourites=1),
// each as the server describes it, as far as they may still see it.
func handleGamesRecent(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := min(max(queryInt(r, "limit", 24), 1), 200)
	rows, err := a.gameList(ctx, who(r).User, r.URL.Query().Get("favourites") == "1", false, limit*2)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type entry struct {
		Item       *service.Item `json:"item"`
		Favourite  bool          `json:"favourite"`
		Plays      int           `json:"plays"`
		LastPlayed int64         `json:"lastPlayed"`
	}
	out := []entry{}
	for _, e := range rows {
		if len(out) >= limit {
			break
		}
		it, err := a.visibleGame(ctx, r, e.ItemID)
		if err != nil {
			continue // no longer theirs to see
		}
		it.Files = nil // where it lives on disk is nobody's business here
		out = append(out, entry{it, e.Favourite, e.Plays, e.LastPlayed})
	}
	writeJSON(w, 200, out)
}

func handleGameMeta(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	m, err := a.gameMetaWithBattery(r.Context(), who(r).User, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}

func handleGameMetaPut(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Favourite *bool   `json:"favourite"`
		Note      *string `json:"note"`
	}
	if err := readJSON(capBody(w, r, maxGameNote*2), &b); err != nil {
		writeErr(w, 400, "this can't be read")
		return
	}
	if b.Note != nil && len(*b.Note) > maxGameNote {
		writeErr(w, 400, "a note can be 8 KB at most")
		return
	}
	ctx, id := r.Context(), who(r).User
	if b.Favourite != nil {
		if err := a.setGameMetaColumn(ctx, id, it.ID, "favourite", boolInt(*b.Favourite)); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if b.Note != nil {
		if err := a.setGameMetaColumn(ctx, id, it.ID, "note", *b.Note); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	m, err := a.gameMetaWithBattery(ctx, id, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m)
}
