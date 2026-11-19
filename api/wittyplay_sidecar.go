// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamefiles"
	"github.com/wittywilla/wittyplay/store"
	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// A game's own .wittyplay beside it (Made Up Quest (USA).wittyplay, or Made Up Quest (USA).sfc.wittyplay)
// is read on every scan. Its look is the game's (as a version 1 file's always was), and, when it
// was made for this dump or can't tell, its details and pictures are the owner's own and stand over
// what sources find, its core becomes the game's core when the game has none chosen yet, and its
// controls are the game's defaults (controls.go). A file made for another dump is flagged and
// nothing in it is used but its look.
//
// What each scan found is kept in wittyplay_sidecars, so the editor and the setup's conversion
// can say which games have one and which were flagged. Nothing is ever written beside a game
// here: only an administrator's editor or conversion does that (wittyplay_edit.go,
// wittyplay_convert.go).
func init() {
	store.Register(store.Migration{Version: 7, SQL: `
CREATE TABLE IF NOT EXISTS wittyplay_sidecars (
  item_id INTEGER PRIMARY KEY, path TEXT NOT NULL DEFAULT '', verdict TEXT NOT NULL DEFAULT '',
  problem TEXT NOT NULL DEFAULT '', at INTEGER NOT NULL DEFAULT 0);`})
	gamefiles.BaseOf = func(gamePath string) string { return gameBaseName(filepath.Base(gamePath)) }
}

// findSidecar is the game's own .wittyplay, if it has one.
func findSidecar(gamePath string) string {
	dir, name := filepath.Dir(gamePath), filepath.Base(gamePath)
	for _, p := range []string{filepath.Join(dir, gameBaseName(name)+wittyplayfile.Ext), gamePath + wittyplayfile.Ext} {
		if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
			return p
		}
	}
	return ""
}

// readSidecar reads a .wittyplay, refusing anything over the format's limit before reading it.
func readSidecar(p string) (*wittyplayfile.Doc, error) {
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > wittyplayfile.MaxFile {
		return nil, wittyplayfile.ErrTooBig
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	return wittyplayfile.Parse(b)
}

// gameFacts are what is known of a game's file, to compare with a .wittyplay's identity: its size
// and the CRC32s the scan worked out, and an MD5 or SHA-1 worked out now for a file small enough
// when the identity gives only those.
func (a *App) gameFacts(ctx context.Context, itemID int64, gamePath string, id *wittyplayfile.Identity) wittyplayfile.Facts {
	f := wittyplayfile.Facts{Name: filepath.Base(gamePath)}
	if st, err := os.Stat(gamePath); err == nil {
		f.Size = st.Size()
	}
	if r, err := a.gameRow(ctx, itemID); err == nil {
		for _, c := range append(append([]uint32{}, r.CRCs...), r.AltCRCs...) {
			f.MoreCRC32 = append(f.MoreCRC32, fmt.Sprintf("%08x", c))
		}
	}
	if id == nil || id.CRC32 != "" && len(f.MoreCRC32) > 0 {
		return f
	}
	if want := id.Wanted(); len(want) > 0 && f.Size <= 64<<20 {
		if h, err := wittyplayfile.Hash(gamePath, want...); err == nil {
			f.CRC32, f.MD5, f.SHA1 = h.CRC32, h.MD5, h.SHA1
		}
	}
	return f
}

// sidecarState is one row of wittyplay_sidecars.
type sidecarState struct {
	Path    string `json:"path,omitempty"`
	Verdict string `json:"verdict,omitempty"` // same, other, unknown; "" when the file couldn't be read
	Problem string `json:"problem,omitempty"`
}

func (a *App) setSidecarState(ctx context.Context, itemID int64, s *sidecarState) {
	if s == nil {
		_, _ = a.DB.ExecContext(ctx, `DELETE FROM wittyplay_sidecars WHERE item_id = ?`, itemID)
		return
	}
	_, _ = a.DB.ExecContext(ctx, `INSERT INTO wittyplay_sidecars(item_id, path, verdict, problem, at) VALUES(?,?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET path = excluded.path, verdict = excluded.verdict, problem = excluded.problem, at = excluded.at`,
		itemID, s.Path, s.Verdict, s.Problem, time.Now().Unix())
}

func (a *App) sidecarStateOf(ctx context.Context, itemID int64) *sidecarState {
	var s sidecarState
	if err := a.DB.QueryRowContext(ctx, `SELECT path, verdict, problem FROM wittyplay_sidecars WHERE item_id = ?`, itemID).Scan(&s.Path, &s.Verdict, &s.Problem); err != nil {
		return nil
	}
	return &s
}

// applySidecar reads a game's own .wittyplay during a scan: its look always, and the rest when it
// is for this dump. f and local are the scan's fields for the game and the fields its owner's files
// gave, which sources then leave alone.
func (a *App) applySidecar(ctx context.Context, it wittymodule.ScanDescribeItem, console string, f *wittymodule.ItemFields, local *[]string) {
	p := findSidecar(it.Files[0])
	if p == "" {
		_ = a.setGamePicture(ctx, it.ID, pictureByFile, wittyplayfile.Look{})
		a.setSidecarState(ctx, it.ID, nil)
		return
	}
	d, err := readSidecar(p)
	if err != nil {
		_ = a.setGamePicture(ctx, it.ID, pictureByFile, wittyplayfile.Look{})
		a.setSidecarState(ctx, it.ID, &sidecarState{Path: p, Problem: err.Error()})
		a.Log.Info("wittyplay: a .wittyplay beside a game can't be used", "cat", "games", "item", it.ID, "err", err)
		return
	}
	_ = a.setGamePicture(ctx, it.ID, pictureByFile, d.For(0, console, filepath.Base(it.Files[0]), it.Title))
	if d.Game == nil {
		a.setSidecarState(ctx, it.ID, &sidecarState{Path: p, Verdict: string(wittyplayfile.Unknown)})
		return
	}
	v := wittyplayfile.Compare(d.Game.Identity, a.gameFacts(ctx, it.ID, it.Files[0], d.Game.Identity))
	st := &sidecarState{Path: p, Verdict: string(v)}
	if v == wittyplayfile.Other {
		st.Problem = "made for another dump of this game (its size or checksum differs): not used"
		a.setSidecarState(ctx, it.ID, st)
		return
	}
	a.setSidecarState(ctx, it.ID, st)
	g := d.Localize("") // the file's own language: the scan has no viewer to suit
	note := func(field string) { *local = append(*local, field) }
	if m := g.Details; m != nil {
		if m.Title != "" {
			f.Title = m.Title
			note("title")
		}
		if m.SortTitle != "" {
			f.SortTitle = m.SortTitle
		}
		if m.Overview != "" {
			f.Overview = m.Overview
			note("overview")
		}
		if m.Year != nil {
			f.Year = *m.Year
			note("year")
		}
		if len(m.Genres) > 0 {
			f.Genres = m.Genres
			note("genres")
		}
		if m.Rating != nil {
			r := *m.Rating
			f.Rating = &r
			note("rating")
		}
		if s := firstOf(m.Developer, m.Publisher); s != "" {
			f.Studio = s
			note("studio")
		}
		for _, board := range []string{"ESRB", "PEGI", "CERO", "USK", "ACB"} {
			if r := m.AgeRatings[board]; r != "" {
				f.ContentRating = r
				note("contentRating")
				break
			}
		}
		if m.OriginalTitle != "" {
			f.Titles = &wittymodule.ItemTitles{Native: m.OriginalTitle}
			note("titles")
		}
	}
	for role, field := range map[string]*string{"boxfront": &f.Poster, "fanart": &f.Backdrop} {
		name := g.Artwork[role]
		if b, ok := d.Files[name]; ok && name != "" {
			kind := map[string]string{"boxfront": "poster", "fanart": "backdrop"}[role]
			if u, err := a.Host.PutImage(ctx, it.ID, kind, bytes.NewReader(b)); err == nil {
				*field = u
				note(kind)
			}
		}
	}
	if pl := g.Play; pl != nil && pl.Core != "" && a.gameCoreChoice(ctx, it.ID) == "" {
		if c := wittyplay.ByID(console); c != nil && a.coreFitsConsole(c, pl.Core) {
			_, _ = a.DB.ExecContext(ctx, `INSERT INTO game_core_choice(item_id, core_id) VALUES(?,?) ON CONFLICT(item_id) DO NOTHING`, it.ID, pl.Core)
		}
	}
}

func firstOf(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// gameSidecarControls are the controls a game's own .wittyplay gives it on its console, with the
// server's own .wittyplay's under them. Nil when neither gives any.
func (a *App) gameSidecarControls(ctx context.Context, itemID int64, console string) *wittyplayfile.ControlSet {
	var set wittyplayfile.ControlSet
	found := false
	if d := a.serverPictures(ctx); d != nil && d.Controls != nil {
		set, found = d.Controls.For(console), true
	}
	if itemID > 0 {
		if r, err := a.gameRow(ctx, itemID); err == nil && r.Path != "" {
			if st := a.sidecarStateOf(ctx, itemID); st == nil || st.Verdict != string(wittyplayfile.Other) {
				if p := findSidecar(r.Path); p != "" {
					if d, err := readSidecar(p); err == nil && d.Controls != nil {
						set, found = d.Controls.For(console).Over(set), true
					}
				}
			}
		}
	}
	if !found {
		return nil
	}
	return &set
}
