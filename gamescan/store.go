// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/wittywilla/wittyplay/store"
)

// What a scan of a games library keeps in WittyPlay's own database, each keyed by the
// library's id and the file's path (the server's ids, as they are; nothing refers to the
// server's tables):
//
//	game_files      what the last scan decided about each file (the verdict cache), so an
//	                unchanged file is never read again
//	game_decisions  what a person said a file is: add (a game for a console), ignore (not a
//	                game), own (never a version of another)
//	game_review     the files a scan left out or wants looked at, and why
//	game_entries    which file in a game's archive a person chose to play
//	game_serials    the product code a scan read in a game's file
//	game_items      what the last scan answered: each game, which game it's another version
//	                of, its discs, and the server's id for it once known
func init() {
	store.Register(store.Migration{Version: 5, SQL: `
CREATE TABLE game_files (
	library INTEGER NOT NULL,
	path    TEXT NOT NULL,
	console TEXT NOT NULL,
	size    INTEGER NOT NULL,
	mtime   INTEGER NOT NULL,
	version INTEGER NOT NULL,
	verdict TEXT NOT NULL,
	PRIMARY KEY (library, path)
);
CREATE TABLE game_decisions (
	library INTEGER NOT NULL,
	path    TEXT NOT NULL,
	action  TEXT NOT NULL,
	console TEXT NOT NULL DEFAULT '',
	at      INTEGER NOT NULL,
	PRIMARY KEY (library, path)
);
CREATE TABLE game_review (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	library INTEGER NOT NULL,
	path    TEXT NOT NULL,
	size    INTEGER NOT NULL DEFAULT 0,
	kind    TEXT NOT NULL,
	reason  TEXT NOT NULL DEFAULT '',
	suggest TEXT NOT NULL DEFAULT '[]',
	console TEXT NOT NULL DEFAULT '',
	UNIQUE (library, path)
);
CREATE TABLE game_entries (
	library INTEGER NOT NULL,
	path    TEXT NOT NULL,
	entry   TEXT NOT NULL,
	PRIMARY KEY (library, path)
);
CREATE TABLE game_serials (
	library INTEGER NOT NULL,
	path    TEXT NOT NULL,
	serial  TEXT NOT NULL,
	PRIMARY KEY (library, path)
);
CREATE TABLE game_items (
	library    INTEGER NOT NULL,
	path       TEXT NOT NULL,
	version_of TEXT NOT NULL DEFAULT '',
	label      TEXT NOT NULL DEFAULT '',
	tagline    TEXT NOT NULL DEFAULT '',
	discs      TEXT NOT NULL DEFAULT '',
	item       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (library, path)
);
CREATE INDEX game_items_item ON game_items(item);
CREATE INDEX game_items_version ON game_items(library, version_of);
`})
}

// ErrNotFound: no such line on the review list, or no such game.
var ErrNotFound = errors.New("not found")

// Decide records what a person said about a file, and the next scan goes by it. action is add
// (with a console), ignore, own (never put it under another version), or forget, which takes
// back whatever was said.
func Decide(ctx context.Context, db *sql.DB, library int64, p, action, console string) error {
	if action == "forget" {
		_, err := db.ExecContext(ctx, `DELETE FROM game_decisions WHERE library = ? AND path = ?`, library, p)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO game_decisions(library, path, action, console, at) VALUES(?,?,?,?,strftime('%s','now'))
		ON CONFLICT(library, path) DO UPDATE SET action = excluded.action, console = excluded.console, at = excluded.at`, library, p, action, console)
	return err
}

// DecideReviewed records what a person said about a file on the review list, by its line.
// It returns the file's path.
func DecideReviewed(ctx context.Context, db *sql.DB, library, reviewID int64, action, console string) (string, error) {
	var p string
	if err := db.QueryRowContext(ctx, `SELECT path FROM game_review WHERE id = ? AND library = ?`, reviewID, library).Scan(&p); err != nil {
		return "", ErrNotFound
	}
	return p, Decide(ctx, db, library, p, action, console)
}

// Entry says which file in a game's archive someone chose, or "".
func Entry(ctx context.Context, db *sql.DB, library int64, p string) string {
	if db == nil {
		return ""
	}
	var e string
	_ = db.QueryRowContext(ctx, `SELECT entry FROM game_entries WHERE library = ? AND path = ?`, library, p).Scan(&e)
	return e
}

// PinEntry says which file in a game's archive to play, or "" to choose by the rules.
func PinEntry(ctx context.Context, db *sql.DB, library int64, p, entry string) error {
	if entry == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM game_entries WHERE library = ? AND path = ?`, library, p)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO game_entries(library, path, entry) VALUES(?,?,?)
		ON CONFLICT(library, path) DO UPDATE SET entry = excluded.entry`, library, p, entry)
	return err
}

// Serial is the product code a scan read in a game's file (or what else set it), or "".
func Serial(ctx context.Context, db *sql.DB, library int64, p string) string {
	if db == nil {
		return ""
	}
	var s string
	_ = db.QueryRowContext(ctx, `SELECT serial FROM game_serials WHERE library = ? AND path = ?`, library, p).Scan(&s)
	return s
}

// SetSerial keeps a game's product code. An empty one changes nothing.
func SetSerial(ctx context.Context, db *sql.DB, library int64, p, serial string) error {
	if serial == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `INSERT INTO game_serials(library, path, serial) VALUES(?,?,?)
		ON CONFLICT(library, path) DO UPDATE SET serial = excluded.serial`, library, p, serial)
	return err
}

// GameDisc is one disc of a game, as far as anyone asking needs to know.
type GameDisc struct {
	Disc int    `json:"disc"`
	Path string `json:"-"`
	Name string `json:"name"`
}

// Listed is a game as the last scan answered it.
type Listed struct {
	Library   int64
	Path      string
	VersionOf string     // the path of the game it's another version of, or ""
	Label     string     // which version it is, when it's under another
	Tagline   string     // what the scan made of its name: region, revision
	Discs     []GameDisc // only for a game on several discs
	Item      int64      // the server's id for it, once known
}

func scanListed(rows *sql.Rows) ([]Listed, error) {
	defer rows.Close()
	var out []Listed
	for rows.Next() {
		var l Listed
		var discs string
		if err := rows.Scan(&l.Library, &l.Path, &l.VersionOf, &l.Label, &l.Tagline, &discs, &l.Item); err != nil {
			return nil, err
		}
		if discs != "" {
			_ = json.Unmarshal([]byte(discs), &l.Discs)
			for i := range l.Discs {
				l.Discs[i].Name = filepath.Base(l.Discs[i].Path)
			}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const listedCols = `library, path, version_of, label, tagline, discs, item`

// ListedGame is the game the last scan of a library answered at this path.
func ListedGame(ctx context.Context, db *sql.DB, library int64, p string) (*Listed, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+listedCols+` FROM game_items WHERE library = ? AND path = ?`, library, p)
	if err != nil {
		return nil, err
	}
	list, err := scanListed(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// VersionsOf are the games the last scan put under head, by path.
func VersionsOf(ctx context.Context, db *sql.DB, library int64, head string) ([]Listed, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+listedCols+` FROM game_items WHERE library = ? AND version_of = ? ORDER BY path`, library, head)
	if err != nil {
		return nil, err
	}
	return scanListed(rows)
}

// NoteItem remembers the server's id for a game's path, as the server says it (an item it sent).
func NoteItem(ctx context.Context, db *sql.DB, library int64, p string, item int64) error {
	if item <= 0 || p == "" {
		return nil
	}
	_, err := db.ExecContext(ctx, `UPDATE game_items SET item = ? WHERE library = ? AND path = ? AND item <> ?`, item, library, p, item)
	return err
}

// ItemAt is the server's id for the game at a path, or 0 when it isn't known.
func ItemAt(ctx context.Context, db *sql.DB, library int64, p string) int64 {
	var id int64
	_ = db.QueryRowContext(ctx, `SELECT item FROM game_items WHERE library = ? AND path = ?`, library, p).Scan(&id)
	return id
}

// ReviewCounts says how many files of each kind a library's review list has.
func ReviewCounts(ctx context.Context, db *sql.DB, library int64) (map[string]int, error) {
	rows, err := db.QueryContext(ctx, `SELECT kind, COUNT(*) FROM game_review WHERE library = ? GROUP BY 1`, library)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if rows.Scan(&k, &n) == nil {
			out[k] = n
		}
	}
	return out, rows.Err()
}

// GameReviewItem is one line of the review list.
type GameReviewItem struct {
	ID int64 `json:"id"`
	// Path is relative to the library's folder, Name the file's own name.
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	// Kind is needs (a person should look), blocked (it's listed, and can't play as it is),
	// or what it was left out as: bios, update, dlc, utility, audio, data, other, duplicate,
	// or ignored.
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	// Suggest lists consoles it might be, best first, each with a name to show.
	Suggest []GameSuggestion `json:"suggest"`
	// ItemID is the game this file is listed as, when it is.
	ItemID int64 `json:"itemId,omitempty"`
}

// GameSuggestion is a console a file might be for.
type GameSuggestion struct {
	Console string `json:"console"`
	Name    string `json:"name"`
	Short   string `json:"short"`
}

// GameReview is the review list, with how many of each kind there are.
type GameReview struct {
	Items  []GameReviewItem `json:"items"`
	Counts map[string]int   `json:"counts"`
	Total  int              `json:"total"`
}

// ReviewList reads the list for a library, whose folders are roots. kind narrows it: "needs",
// "blocked" or "left" for everything left out.
func ReviewList(ctx context.Context, db *sql.DB, library int64, roots []string, kind string, limit, offset int) (*GameReview, error) {
	out := &GameReview{Items: []GameReviewItem{}}
	counts, err := ReviewCounts(ctx, db, library)
	if err != nil {
		return nil, err
	}
	out.Counts = counts
	where := `r.library = ?`
	args := []any{library}
	switch kind {
	case "":
	case "left":
		where += ` AND r.kind NOT IN (?, ?)`
		args = append(args, reviewNeeds, reviewBlocked)
	default:
		where += ` AND r.kind = ?`
		args = append(args, kind)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_review r WHERE `+where, args...).Scan(&out.Total)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := db.QueryContext(ctx, `SELECT r.id, r.path, r.size, r.kind, r.reason, r.suggest, IFNULL(i.item, 0) FROM game_review r
		LEFT JOIN game_items i ON i.library = r.library AND i.path = r.path WHERE `+where+` ORDER BY r.kind, r.path LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it GameReviewItem
		var p, sug string
		if err := rows.Scan(&it.ID, &p, &it.Size, &it.Kind, &it.Reason, &sug, &it.ItemID); err != nil {
			return nil, err
		}
		it.Path, it.Name = relativeToLibrary(roots, p), filepath.Base(p)
		var ids []string
		_ = json.Unmarshal([]byte(sug), &ids)
		it.Suggest = suggestionsFor(ids)
		out.Items = append(out.Items, it)
	}
	return out, rows.Err()
}

// suggestionsFor names consoles, and adds the others people can pick from after them.
func suggestionsFor(ids []string) []GameSuggestion {
	out := []GameSuggestion{}
	for _, id := range ids {
		if c := consoles().byID(id); c != nil && !c.Hidden {
			out = append(out, GameSuggestion{Console: id, Name: c.Name, Short: c.Short})
		}
	}
	return out
}

func relativeToLibrary(roots []string, p string) string {
	best := ""
	for _, root := range roots {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") && len(root) > len(best) {
			best = root
		}
	}
	if best != "" {
		rel, _ := filepath.Rel(best, p)
		return filepath.ToSlash(rel)
	}
	return filepath.Base(p)
}

// ForgetLibrary drops everything kept about a library's files, once it's gone.
func ForgetLibrary(ctx context.Context, db *sql.DB, library int64) error {
	for _, t := range []string{"game_files", "game_decisions", "game_review", "game_entries", "game_serials", "game_items"} {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+t+` WHERE library = ?`, library); err != nil {
			return err
		}
	}
	return nil
}

// RegionsChanged makes the owner's new order of regions count at a library's next scan: an
// archive's file is worked out again, unless someone chose it, and so is which version leads a
// game.
func RegionsChanged(ctx context.Context, db *sql.DB, library int64) error {
	if _, err := db.ExecContext(ctx, `UPDATE game_files SET version = 0 WHERE library = ? AND json_extract(verdict, '$.entry') IS NOT NULL`, library); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE game_items SET version_of = '' WHERE library = ?`, library)
	return err
}

// ListedItem is the game the last scan answered that the server knows by this id.
func ListedItem(ctx context.Context, db *sql.DB, item int64) (*Listed, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+listedCols+` FROM game_items WHERE item = ? LIMIT 1`, item)
	if err != nil {
		return nil, err
	}
	list, err := scanListed(rows)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return &list[0], nil
}

// Detected is how a game's file was identified, as its metadata pages show it.
type Detected struct {
	How, Proof, Problem, ProblemText string
	Sure                             int
	Title, Serial, Name, Entry       string // what the file calls itself; the archive entry played
}

// Detection is how the file at a path was identified, from the verdict the scan kept.
func Detection(ctx context.Context, db *sql.DB, library int64, p string) (Detected, bool) {
	var raw string
	if err := db.QueryRowContext(ctx, `SELECT verdict FROM game_files WHERE library = ? AND path = ?`, library, p).Scan(&raw); err != nil {
		return Detected{}, false
	}
	var v gameVerdict
	if json.Unmarshal([]byte(raw), &v) != nil {
		return Detected{}, false
	}
	how := v.How
	if how == "" {
		how = howFolder
	}
	d := Detected{How: how, Sure: v.Sure, Proof: v.Proof, Problem: v.Problem, Title: v.Title, Serial: v.Serial, Name: v.Name, Entry: v.Entry}
	if v.Problem != "" {
		d.ProblemText = ProblemText(v.Problem, v.Proof)
	}
	return d, true
}
