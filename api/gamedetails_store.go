// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/gamemeta"
	"github.com/wittywilla/wittyplay/store"
)

// What WittyPlay finds out about games (moved from the server's game_meta and game_meta_sources,
// module API 5): one row per game, by the server's item id, with what the file is, the queue's
// state and the merged answer; and each source's own answer beside it. The server keeps the item
// and its generic fields; what's found is set there through the host API (SetItem).
//
// Also here: game pictures' looks (gamepictures.go), console and game studio logos (gamelogos.go)
// and console photos (consolephotos.go).
func init() {
	store.Register(store.Migration{Version: 6, SQL: `
CREATE TABLE IF NOT EXISTS wittyplay_game_details (
  item_id INTEGER PRIMARY KEY, library_id INTEGER NOT NULL DEFAULT 0, console TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '', files TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending', next_try INTEGER NOT NULL DEFAULT 0, tries INTEGER NOT NULL DEFAULT 0,
  locked INTEGER NOT NULL DEFAULT 0, edited TEXT NOT NULL DEFAULT '', applied TEXT NOT NULL DEFAULT '',
  file_size INTEGER NOT NULL DEFAULT -1, file_mtime INTEGER NOT NULL DEFAULT -1, crcs TEXT NOT NULL DEFAULT '', crc_size INTEGER NOT NULL DEFAULT 0,
  alt_crcs TEXT NOT NULL DEFAULT '', serial TEXT NOT NULL DEFAULT '', header_title TEXT NOT NULL DEFAULT '', entry_name TEXT NOT NULL DEFAULT '',
  chosen_entry TEXT NOT NULL DEFAULT '', query_title TEXT NOT NULL DEFAULT '', tags TEXT NOT NULL DEFAULT '',
  match_source TEXT NOT NULL DEFAULT '', match_id TEXT NOT NULL DEFAULT '', match_method TEXT NOT NULL DEFAULT '', confidence INTEGER NOT NULL DEFAULT 0,
  released TEXT NOT NULL DEFAULT '', developer TEXT NOT NULL DEFAULT '', publisher TEXT NOT NULL DEFAULT '', franchise TEXT NOT NULL DEFAULT '',
  players INTEGER NOT NULL DEFAULT 0, scores TEXT NOT NULL DEFAULT '', ages TEXT NOT NULL DEFAULT '', art TEXT NOT NULL DEFAULT '', credits TEXT NOT NULL DEFAULT '',
  poster_refs TEXT NOT NULL DEFAULT '', backdrop_refs TEXT NOT NULL DEFAULT '', poster_ref TEXT NOT NULL DEFAULT '', backdrop_ref TEXT NOT NULL DEFAULT '',
  poster_url TEXT NOT NULL DEFAULT '', backdrop_url TEXT NOT NULL DEFAULT '', has_overview INTEGER NOT NULL DEFAULT 0,
  art_wanted INTEGER NOT NULL DEFAULT 0, art_tries INTEGER NOT NULL DEFAULT 0, art_next INTEGER NOT NULL DEFAULT 0,
  fetched_at INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS wittyplay_game_details_queue ON wittyplay_game_details(status, next_try);
CREATE INDEX IF NOT EXISTS wittyplay_game_details_art ON wittyplay_game_details(art_wanted, art_next);
CREATE TABLE IF NOT EXISTS wittyplay_game_answers (
  item_id INTEGER NOT NULL, source TEXT NOT NULL, source_id TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '',
  confidence INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL DEFAULT '', fetched_at INTEGER NOT NULL,
  PRIMARY KEY(item_id, source));
CREATE TABLE IF NOT EXISTS wittyplay_game_pictures (
  item_id INTEGER NOT NULL, source TEXT NOT NULL, look TEXT NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY(item_id, source)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS wittyplay_logos (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, key TEXT NOT NULL, name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending', source TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '', license TEXT NOT NULL DEFAULT '', license_url TEXT NOT NULL DEFAULT '',
  credit TEXT NOT NULL DEFAULT '', file TEXT NOT NULL DEFAULT '', opaque INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL DEFAULT 0, tried_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE(kind, key));
CREATE TABLE IF NOT EXISTS wittyplay_console_photos (
  console TEXT PRIMARY KEY, none INTEGER NOT NULL DEFAULT 0, at INTEGER NOT NULL DEFAULT 0, file TEXT NOT NULL DEFAULT '',
  credit TEXT NOT NULL DEFAULT '', license TEXT NOT NULL DEFAULT '', license_url TEXT NOT NULL DEFAULT '', page_url TEXT NOT NULL DEFAULT '');`})
}

// gameRow is one row of wittyplay_game_details, as far as the worker needs it.
type gameRow struct {
	ItemID, LibraryID      int64
	Console, Title, Path   string
	Files                  []string
	Status                 string
	Tries                  int
	Locked                 bool
	Edited, Applied        map[string]bool
	FileSize, FileMTime    int64
	CRCs, AltCRCs          []uint32
	CRCSize                int64
	Serial, QueryTitle     string
	HeaderTitle, EntryName string
	ChosenEntry, Tags      string
	PosterRefs, BackRefs   []string
	PosterRef, BackdropRef string
	PosterURL, BackdropURL string
}

const gameRowCols = `item_id, library_id, console, title, path, files, status, tries, locked, edited, applied, file_size, file_mtime, crcs, crc_size, alt_crcs, serial,
	query_title, header_title, entry_name, chosen_entry, tags, poster_refs, backdrop_refs, poster_ref, backdrop_ref, poster_url, backdrop_url`

func scanGameRow(sc interface{ Scan(...any) error }) (gameRow, error) {
	var r gameRow
	var locked int
	var files, edited, applied, crcs, alts, posters, backs string
	err := sc.Scan(&r.ItemID, &r.LibraryID, &r.Console, &r.Title, &r.Path, &files, &r.Status, &r.Tries, &locked, &edited, &applied, &r.FileSize, &r.FileMTime,
		&crcs, &r.CRCSize, &alts, &r.Serial, &r.QueryTitle, &r.HeaderTitle, &r.EntryName, &r.ChosenEntry, &r.Tags, &posters, &backs, &r.PosterRef, &r.BackdropRef,
		&r.PosterURL, &r.BackdropURL)
	r.Locked = locked != 0
	r.Edited, r.Applied = gmSetOf(edited), gmSetOf(applied)
	r.CRCs, r.AltCRCs = gmParseCRCs(crcs), gmParseCRCs(alts)
	r.PosterRefs, r.BackRefs = gmRefList(posters), gmRefList(backs)
	_ = json.Unmarshal([]byte(files), &r.Files)
	return r, err
}

func (a *App) gameRow(ctx context.Context, id int64) (gameRow, error) {
	return scanGameRow(a.DB.QueryRowContext(ctx, `SELECT `+gameRowCols+` FROM wittyplay_game_details WHERE item_id = ?`, id))
}

// gameIDs reads a query's first column of item ids. The rows are closed before anything else
// asks the database: it has one connection.
func (a *App) gameIDs(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := a.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func gmSetOf(s string) map[string]bool {
	out := map[string]bool{}
	for _, x := range strings.Split(s, ",") {
		if x != "" {
			out[x] = true
		}
	}
	return out
}

func gmListOf(m map[string]bool) string {
	var out []string
	for k, on := range m {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func gmParseCRCs(s string) []uint32 {
	var out []uint32
	for _, x := range strings.Split(s, ",") {
		var v uint32
		if _, err := fmt.Sscanf(x, "%08x", &v); err == nil && x != "" {
			out = append(out, v)
		}
	}
	return out
}

func gmFormatCRCs(v []uint32) string {
	parts := make([]string, len(v))
	for i, c := range v {
		parts[i] = fmt.Sprintf("%08x", c)
	}
	return strings.Join(parts, ",")
}

func gmRefList(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

// gameEnqueued is what describing a game knows about it: the item as the server sent it, and the
// fields that are a person's or the owner's own files'.
type gameEnqueued struct {
	ItemID, LibraryID int64
	Console, Title    string
	Files             []string
	// Local are fields the owner's own files beside the game gave (an NFO, its box art); Edited
	// those a person typed. Sources leave both alone.
	Local, Edited []string
}

// enqueueGame puts a game in the queue, because it's new or its files changed. A locked match
// stays as it is; what the file is may still need working out again.
func (a *App) enqueueGame(ctx context.Context, e gameEnqueued) error {
	var was, applied string
	_ = a.DB.QueryRowContext(ctx, `SELECT edited, applied FROM wittyplay_game_details WHERE item_id = ?`, e.ItemID).Scan(&was, &applied)
	edited, done := map[string]bool{}, gmSetOf(applied)
	// What the server says a person typed is the whole list now; the owner's files add to it.
	for _, f := range append(append([]string{}, e.Edited...), e.Local...) {
		edited[f] = true
		delete(done, f)
	}
	path := ""
	if len(e.Files) > 0 {
		path = e.Files[0]
	}
	files, _ := json.Marshal(e.Files)
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_game_details(item_id, library_id, console, title, path, files, edited, applied) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET library_id = excluded.library_id, console = excluded.console, title = excluded.title, path = excluded.path, files = excluded.files,
		  status = CASE WHEN locked = 1 THEN status ELSE 'pending' END, next_try = 0, tries = 0, edited = excluded.edited, applied = excluded.applied`,
		e.ItemID, e.LibraryID, e.Console, e.Title, path, string(files), gmListOf(edited), gmListOf(done))
	if err != nil {
		return err
	}
	_, err = a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE item_id = ? AND method <> 'manual'`, e.ItemID)
	return err
}

// noteGameFile keeps what WittyPlay's identification found in a game's file: its serial, the
// title its header holds, the archive entry it's known by, and the entry someone chose to play.
// Identification (gamescan) calls it; what's asked of sources changes with it.
func (a *App) noteGameFile(ctx context.Context, itemID int64, serial, headerTitle, entryName, chosenEntry string) error {
	if _, err := a.DB.ExecContext(ctx, `INSERT OR IGNORE INTO wittyplay_game_details(item_id) VALUES(?)`, itemID); err != nil {
		return err
	}
	_, err := a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET serial = CASE WHEN ? <> '' THEN ? ELSE serial END, header_title = ?, entry_name = ?,
		file_size = CASE WHEN chosen_entry <> ? THEN -1 ELSE file_size END, chosen_entry = ? WHERE item_id = ?`,
		serial, serial, headerTitle, entryName, chosenEntry, chosenEntry, itemID)
	return err
}

// gameAnswers reads what sources have said about a game. done lists every source that has
// answered, including those that said they don't know it.
func (a *App) gameAnswers(ctx context.Context, id int64) (map[string]*gamemeta.Info, map[string]bool, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT source, method, data FROM wittyplay_game_answers WHERE item_id = ?`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	answers, done := map[string]*gamemeta.Info{}, map[string]bool{}
	for rows.Next() {
		var source, method, data string
		if err := rows.Scan(&source, &method, &data); err != nil {
			return nil, nil, err
		}
		done[source] = true
		if method == "none" || data == "" {
			continue
		}
		var in gamemeta.Info
		if json.Unmarshal([]byte(data), &in) == nil {
			answers[source] = &in
		}
	}
	return answers, done, rows.Err()
}

func (a *App) saveGameAnswer(ctx context.Context, id int64, source string, in *gamemeta.Info) error {
	sid, method, conf, data := "", "none", 0, ""
	if in != nil {
		sid, method, conf, data = in.ID, in.Method, in.Confidence, toJSON(in)
	}
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_game_answers(item_id, source, source_id, method, confidence, data, fetched_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(item_id, source) DO UPDATE SET source_id = excluded.source_id, method = excluded.method, confidence = excluded.confidence, data = excluded.data, fetched_at = excluded.fetched_at`,
		id, source, sid, method, conf, data, time.Now().Unix())
	return err
}

// gameEntry says which file in a game's archive someone chose, or "".
func (a *App) gameEntry(ctx context.Context, id int64) string {
	var e string
	_ = a.DB.QueryRowContext(ctx, `SELECT chosen_entry FROM wittyplay_game_details WHERE item_id = ?`, id).Scan(&e)
	return e
}

// forgetGames drops what's known about games the server no longer has.
func (a *App) forgetGames(ctx context.Context, ids []int64) error {
	for _, id := range ids {
		for _, t := range []string{"wittyplay_game_details", "wittyplay_game_answers", "wittyplay_game_pictures"} {
			if _, err := a.DB.ExecContext(ctx, `DELETE FROM `+t+` WHERE item_id = ?`, id); err != nil {
				return err
			}
		}
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
