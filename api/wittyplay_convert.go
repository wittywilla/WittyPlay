// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamefiles"
	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// Converting a library both ways, for the server's first-run setup and for administrators:
//
//   - gather: for each game without a .wittyplay, what the standard files beside it say (a folder's
//     gamelist.xml, its .nfo, pictures in media/ folders or named after it, .rmp and .cfg files)
//     and, if asked, what WittyPlay already knows of it, into Game (USA).wittyplay beside it;
//   - writeout: each game's .wittyplay out to those standard files.
//
// One conversion runs at a time, game by game, so a small machine keeps up with everything else.
// Only an administrator can start one: it writes in the owner's games folders, which a scan never
// does. A game whose folder can't be written to is counted as failed and the rest carry on.
//
//	GET    /games/wittyplay/convert          counts, and the conversion running or last run
//	POST   /games/wittyplay/convert          start: {direction, source, overwrite, library}
//	GET    /games/wittyplay/convert/status   the conversion's progress
//	DELETE /games/wittyplay/convert          stop it after the game it is on
func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/wittyplay/convert", w(adminRoute(handleConvertCounts)))
		mux.Handle("POST /games/wittyplay/convert", w(adminRoute(handleConvertStart)))
		mux.Handle("GET /games/wittyplay/convert/status", w(adminRoute(handleConvertStatus)))
		mux.Handle("DELETE /games/wittyplay/convert", w(adminRoute(handleConvertStop)))
	})
}

// Directions and sources of a conversion.
const (
	convertGather   = "gather"
	convertWriteOut = "writeout"
	sourceFiles     = "files"   // only the standard files beside a game
	sourceLibrary   = "library" // and what WittyPlay knows of it
)

// convertStatus is a conversion's progress.
type convertStatus struct {
	State     string         `json:"state"` // idle, running, done, stopped, failed
	Direction string         `json:"direction,omitempty"`
	Source    string         `json:"source,omitempty"`
	Total     int            `json:"total"`
	Done      int            `json:"done"`
	Written   int            `json:"written"`
	Skipped   int            `json:"skipped"`
	Failed    int            `json:"failed"`
	Errors    []convertError `json:"errors,omitempty"` // the first 20
	Started   int64          `json:"started,omitempty"`
	Finished  int64          `json:"finished,omitempty"`
}

type convertError struct {
	Item  int64  `json:"item"`
	Title string `json:"title,omitempty"`
	Error string `json:"error"`
}

type converter struct {
	mu     sync.Mutex
	status convertStatus
	cancel context.CancelFunc
}

var conversions = &converter{status: convertStatus{State: "idle"}}

func (c *converter) snapshot() convertStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Errors = append([]convertError(nil), c.status.Errors...)
	return s
}

func (c *converter) update(f func(s *convertStatus)) {
	c.mu.Lock()
	f(&c.status)
	c.mu.Unlock()
}

// convertGame is one game a conversion may go through.
type convertGame struct {
	ID      int64
	Library int64
	Console string
	Title   string
	Path    string
}

func (a *App) convertGames(ctx context.Context, library int64) ([]convertGame, error) {
	q := `SELECT item_id, library_id, console, title, path FROM wittyplay_game_details WHERE path <> '' AND console <> ?`
	args := []any{wittyplay.TestConsole}
	if library > 0 {
		q += ` AND library_id = ?`
		args = append(args, library)
	}
	rows, err := a.DB.QueryContext(ctx, q+` ORDER BY item_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []convertGame
	for rows.Next() {
		var g convertGame
		if err := rows.Scan(&g.ID, &g.Library, &g.Console, &g.Title, &g.Path); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GET /games/wittyplay/convert[?library=]: how many games there are, how many have a .wittyplay,
// how many have standard files beside them, what each direction would go through, and the
// conversion running or last run.
func handleConvertCounts(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	lib, _ := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
	games, err := a.convertGames(ctx, lib)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var with, standard, gather, flagged int
	for i, g := range games {
		if i%200 == 0 && ctx.Err() != nil {
			return
		}
		has := findSidecar(g.Path) != ""
		std := gamefiles.Has(g.Path)
		if has {
			with++
		}
		if std {
			standard++
		}
		if std && !has {
			gather++
		}
	}
	_ = a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wittyplay_sidecars WHERE verdict = ?`, string(wittyplayfile.Other)).Scan(&flagged)
	writeJSON(w, 200, map[string]any{
		"games":         len(games),
		"withWittyplay": with,
		"withStandard":  standard,
		// What each direction goes through: gather from files only, gather with the library's
		// own knowledge too (every game without one), and write out.
		"toGather":        gather,
		"toGatherLibrary": len(games) - with,
		"toWriteOut":      with,
		"flagged":         flagged,
		"status":          conversions.snapshot(),
	})
}

// POST /games/wittyplay/convert {direction: gather|writeout, source: files|library, overwrite,
// library}: starts a conversion, and answers 202 with its status. 409 when one is running.
func handleConvertStart(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Direction string `json:"direction"`
		Source    string `json:"source"`
		Overwrite bool   `json:"overwrite"`
		Library   int64  `json:"library"`
	}
	if err := readJSON(capBody(w, r, 4<<10), &b); err != nil {
		writeErr(w, 400, "this request can't be read")
		return
	}
	if b.Direction != convertGather && b.Direction != convertWriteOut {
		writeErr(w, 400, `direction is "gather" or "writeout"`)
		return
	}
	if b.Source == "" {
		b.Source = sourceFiles
	}
	if b.Source != sourceFiles && b.Source != sourceLibrary {
		writeErr(w, 400, `source is "files" or "library"`)
		return
	}
	games, err := a.convertGames(r.Context(), b.Library)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	c := conversions
	c.mu.Lock()
	if c.status.State == "running" {
		c.mu.Unlock()
		writeErr(w, 409, "a conversion is running already")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.status = convertStatus{State: "running", Direction: b.Direction, Source: b.Source, Total: len(games), Started: time.Now().Unix()}
	c.mu.Unlock()
	go a.runConversion(ctx, games, b.Direction, b.Source, b.Overwrite)
	writeJSON(w, 202, conversions.snapshot())
}

func handleConvertStatus(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, conversions.snapshot())
}

func handleConvertStop(a *App, w http.ResponseWriter, r *http.Request) {
	c := conversions
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	writeJSON(w, 200, conversions.snapshot())
}

func (a *App) runConversion(ctx context.Context, games []convertGame, direction, source string, overwrite bool) {
	defer func() {
		if p := recover(); p != nil {
			conversions.update(func(s *convertStatus) { s.State, s.Finished = "failed", time.Now().Unix() })
			a.Log.Error("wittyplay: the conversion stopped", "cat", "games", "err", fmt.Sprint(p))
		}
	}()
	for _, g := range games {
		if ctx.Err() != nil {
			conversions.update(func(s *convertStatus) { s.State, s.Finished = "stopped", time.Now().Unix() })
			return
		}
		var wrote bool
		var err error
		if direction == convertGather {
			wrote, err = a.gatherGame(ctx, g, source, overwrite)
		} else {
			wrote, err = a.writeOutGame(ctx, g, overwrite)
		}
		conversions.update(func(s *convertStatus) {
			s.Done++
			switch {
			case err != nil:
				s.Failed++
				if len(s.Errors) < 20 {
					s.Errors = append(s.Errors, convertError{Item: g.ID, Title: g.Title, Error: err.Error()})
				}
			case wrote:
				s.Written++
			default:
				s.Skipped++
			}
		})
	}
	conversions.update(func(s *convertStatus) { s.State, s.Finished = "done", time.Now().Unix() })
	a.Log.Info("wittyplay: a conversion finished", "cat", "games", "direction", direction, "status", fmt.Sprintf("%+v", conversions.snapshot()))
}

// gatherGame writes a game's .wittyplay from what is beside it (and, for source library, what
// WittyPlay knows). False and no error: nothing to write, or one is there already.
func (a *App) gatherGame(ctx context.Context, g convertGame, source string, overwrite bool) (bool, error) {
	if !overwrite && findSidecar(g.Path) != "" {
		return false, nil
	}
	if _, err := os.Stat(g.Path); err != nil {
		return false, errors.New("the game's file can't be found")
	}
	d, _, err := gamefiles.Gather(g.Path)
	if err != nil {
		return false, err
	}
	if source == sourceLibrary {
		d = a.libraryDoc(ctx, g, d)
	}
	if d == nil {
		return false, nil
	}
	if d.Game == nil {
		d.Game = &wittyplayfile.About{}
	}
	if d.Game.Details == nil {
		d.Game.Details = &wittyplayfile.Details{}
	}
	if d.Game.Details.Console == "" {
		d.Game.Details.Console = g.Console
	}
	if id := d.Game.Identity; id == nil || id.Size == 0 {
		d.Game.Identity = a.identityFor(ctx, g, id)
	}
	d.Stamp("", "WittyPlay", moduleVersion(), time.Now())
	if d.Authorship.Source == "" {
		d.Authorship.Source = "gathered from the files beside the game"
		if source == sourceLibrary {
			d.Authorship.Source = "gathered from the files beside the game and WittyPlay's game details"
		}
	}
	b, err := d.EncodeAny()
	if err != nil {
		return false, err
	}
	if err := writeSidecarFile(gamefiles.SidecarPath(g.Path), b); err != nil {
		return false, err
	}
	return true, nil
}

// identityFor is a game's identity: its name and size, and checksums, worked out for a file small
// enough or taken from the scan's CRC32.
func (a *App) identityFor(ctx context.Context, g convertGame, have *wittyplayfile.Identity) *wittyplayfile.Identity {
	id := &wittyplayfile.Identity{FileName: filepath.Base(g.Path)}
	if have != nil {
		*id = *have
		id.FileName = filepath.Base(g.Path)
	}
	if st, err := os.Stat(g.Path); err == nil {
		id.Size = st.Size()
		if st.Size() <= 64<<20 {
			if h, err := wittyplayfile.Hash(g.Path, "crc32", "md5", "sha1"); err == nil {
				id.CRC32, id.MD5, id.SHA1 = h.CRC32, h.MD5, h.SHA1
				return id
			}
		}
	}
	if r, err := a.gameRow(ctx, g.ID); err == nil && len(r.CRCs) > 0 && id.CRC32 == "" && r.CRCSize == id.Size {
		id.CRC32 = fmt.Sprintf("%08x", r.CRCs[0])
	}
	return id
}

// libraryDoc adds what WittyPlay knows of a game under what the files beside it said.
func (a *App) libraryDoc(ctx context.Context, g convertGame, d *wittyplayfile.Doc) *wittyplayfile.Doc {
	if d == nil {
		d = &wittyplayfile.Doc{Version: wittyplayfile.Version, Game: &wittyplayfile.About{}}
	}
	if d.Game == nil {
		d.Game = &wittyplayfile.About{}
	}
	if d.Game.Details == nil {
		d.Game.Details = &wittyplayfile.Details{}
	}
	m := d.Game.Details
	gw := a.gameWorker()
	set := gw.sources(ctx)
	if answers, _, err := a.gameAnswers(ctx, g.ID); err == nil {
		if f, err := gw.mergeAnswers(ctx, g.ID, answersInOrder(answers, set.on)); err == nil {
			fill(&m.Title, f.Title)
			fill(&m.SortTitle, f.SortTitle)
			fill(&m.Overview, f.Overview)
			if m.Year == nil && f.Year >= 1950 && f.Year <= 3000 {
				y := f.Year
				m.Year = &y
			}
			if len(m.Genres) == 0 && len(f.Genres) > 0 {
				m.Genres = capList(f.Genres)
			}
			if m.Rating == nil && f.Rating != nil && *f.Rating >= 0 && *f.Rating <= 10 {
				r := *f.Rating
				m.Rating = &r
			}
			if f.Titles != nil {
				fill(&m.OriginalTitle, f.Titles.Native)
			}
		}
	}
	if det, err := a.gameDetailsOf(ctx, g.ID); err == nil {
		fill(&m.Developer, det.Developer)
		fill(&m.Publisher, det.Publisher)
		fill(&m.Series, det.Franchise)
		if m.ReleaseDate == "" && det.Released != "" {
			m.ReleaseDate = det.Released
		}
		if m.Players == nil && det.Players > 0 && det.Players <= 64 {
			m.Players = &wittyplayfile.Players{Min: 1, Max: det.Players}
		}
		for _, age := range det.Ages {
			if m.AgeRatings == nil {
				m.AgeRatings = map[string]string{}
			}
			if _, ok := m.AgeRatings[age.System]; !ok && age.System != "" && age.Value != "" && len(m.AgeRatings) < 20 {
				m.AgeRatings[age.System] = age.Value
			}
		}
	}
	fill(&m.Title, g.Title)
	// The box art WittyPlay has already downloaded, when the files beside the game had none. Nothing
	// is fetched for this.
	if d.Game.Artwork["boxfront"] == "" {
		if r, err := a.gameRow(ctx, g.ID); err == nil && r.PosterRef != "" {
			for _, ext := range []string{".jpg", ".png", ".webp"} {
				p := filepath.Join(a.gameImagesDir(), gameImageName(r.PosterRef, ext))
				if b, err := os.ReadFile(p); err == nil && len(b) <= wittyplayfile.MaxImage {
					_, _ = d.SetImage("boxfront", b)
					break
				}
			}
		}
	}
	if core := a.gameCoreChoice(ctx, g.ID); core != "" {
		if d.Game.Play == nil {
			d.Game.Play = &wittyplayfile.Play{}
		}
		fill(&d.Game.Play.Core, core)
	}
	return d
}

func fill(dst *string, v string) {
	if *dst == "" {
		*dst = strings.TrimSpace(v)
	}
}

func capList(l []string) []string {
	if len(l) > wittyplayfile.MaxListItems {
		return l[:wittyplayfile.MaxListItems]
	}
	return l
}

// writeOutGame writes a game's .wittyplay out to the standard files beside it.
func (a *App) writeOutGame(ctx context.Context, g convertGame, overwrite bool) (bool, error) {
	p := findSidecar(g.Path)
	if p == "" {
		return false, nil
	}
	d, err := readSidecar(p)
	if err != nil {
		return false, err
	}
	if d.Game != nil {
		if v := wittyplayfile.Compare(d.Game.Identity, a.gameFacts(ctx, g.ID, g.Path, d.Game.Identity)); v == wittyplayfile.Other {
			return false, errors.New("its .wittyplay was made for another dump of this game: not written out")
		}
	}
	if d.Game == nil && d.Controls == nil {
		return false, nil
	}
	out, err := gamefiles.WriteOut(g.Path, d, g.Console, gamefiles.Options{Overwrite: overwrite})
	if err != nil {
		return false, err
	}
	return len(out.Files) > 0, nil
}

// writeSidecarFile writes a .wittyplay through a temporary file beside it.
func writeSidecarFile(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".wittyplay-*.tmp")
	if err != nil {
		return fmt.Errorf("the game's folder can't be written to: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// moduleVersion is this module's version, from its manifest.
func moduleVersion() string {
	var m struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(wittyplay.ManifestJSON, &m)
	return m.Version
}
