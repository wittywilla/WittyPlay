// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"errors"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gameid"
	"github.com/wittywilla/wittyplay/gamescan"
)

// Everything about putting a games library right: the list of files a scan wasn't sure
// about, how an import is going, and fixing one game. Files aren't touched by any of it:
// each choice is kept in WittyPlay's own database, and the next scan goes by it (the server
// is asked for one straight away). Moved from the server's /api/libraries/{id}/games/… and
// /api/games/{id}/… routes, which the server answers here now.

func init() { addRoutes(gameReviewRoutes) }

func gameReviewRoutes(mux *http.ServeMux, with with) {
	admin := func(f func(*App, http.ResponseWriter, *http.Request)) http.Handler { return service.Admins(with(f)) }
	mux.Handle("GET /libraries/{id}/games/review", admin(handleGameReview))
	mux.Handle("POST /libraries/{id}/games/review/apply", admin(handleGameReviewApply))
	mux.Handle("GET /libraries/{id}/games/import", admin(handleGameImport))
	mux.Handle("GET /games/{id}/versions", with(handleGameVersions))
	mux.Handle("PUT /games/{id}/version", admin(handleGameVersionPick))
	mux.Handle("POST /games/{id}/split", admin(handleGameSplit))
	mux.Handle("PUT /games/{id}/console", admin(handleGameConsoleFix))
	mux.Handle("POST /games/{id}/not-a-game", admin(handleGameNotAGame))
}

// gameLookupStatus says how finding out about a library's games is going (the import page's
// lookup), when the part of WittyPlay that does that has set it; null until then.
var gameLookupStatus func(ctx context.Context, a *App) any

// onEntryPinned runs when someone chose which file of a game's archive to play: that's a
// different file, so what's known about it is worked out again.
var onEntryPinned func(ctx context.Context, a *App, it *service.Item)

// gamesLibrary is the games library a route names, as the server has it.
func (a *App) gamesLibrary(w http.ResponseWriter, r *http.Request) *host.Library {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad id")
		return nil
	}
	libs, err := a.Host.Libraries(r.Context())
	if err != nil {
		writeErr(w, 502, "the server can't be reached right now")
		return nil
	}
	for i := range libs {
		if libs[i].ID == id && libs[i].Kind == "games" {
			return &libs[i]
		}
	}
	writeErr(w, 404, "library not found")
	return nil
}

func reviewInt(r *http.Request, name string, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil {
		return n
	}
	return def
}

// GET /libraries/{id}/games/review?kind=needs|blocked|left|ignored|duplicate&limit=&offset=
func handleGameReview(a *App, w http.ResponseWriter, r *http.Request) {
	lib := a.gamesLibrary(w, r)
	if lib == nil {
		return
	}
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "", "needs", "blocked", "left", "ignored", "duplicate":
	default:
		writeErr(w, 400, "no such list")
		return
	}
	out, err := gamescan.ReviewList(r.Context(), a.DB.DB, lib.ID, lib.Paths, kind, reviewInt(r, "limit", 200), reviewInt(r, "offset", 0))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// rescan asks the server to scan a library again, after someone said what a file is.
func (a *App) rescan(ctx context.Context, library int64) {
	if err := a.Host.Rescan(ctx, library); err != nil {
		a.Log.Warn("asking the server to scan a games library", "cat", "games", "library", library, "err", err)
	}
}

// POST /libraries/{id}/games/review/apply {ids: [...], action: "add"|"ignore"|"forget", console}
//
// add says these are games for console, ignore says they aren't games, and forget takes back
// whatever was said. A scan follows, and it only has to read what changed.
func handleGameReviewApply(a *App, w http.ResponseWriter, r *http.Request) {
	lib := a.gamesLibrary(w, r)
	if lib == nil {
		return
	}
	var b struct {
		IDs     []int64 `json:"ids"`
		Action  string  `json:"action"`
		Console string  `json:"console"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if len(b.IDs) == 0 || len(b.IDs) > 5000 {
		writeErr(w, 400, "say which files, up to 5,000 at a time")
		return
	}
	switch b.Action {
	case "add":
		if !consoleKnown(b.Console) {
			writeErr(w, 400, "no such console")
			return
		}
	case "ignore", "forget":
		b.Console = ""
	default:
		writeErr(w, 400, "the action is add, ignore or forget")
		return
	}
	done := 0
	for _, id := range b.IDs {
		if _, err := gamescan.DecideReviewed(r.Context(), a.DB.DB, lib.ID, id, b.Action, b.Console); err != nil {
			if errors.Is(err, gamescan.ErrNotFound) {
				continue
			}
			writeErr(w, 500, err.Error())
			return
		}
		done++
	}
	if done > 0 {
		a.rescan(r.Context(), lib.ID)
	}
	writeJSON(w, 200, map[string]any{"applied": done})
}

// scanProgress is how far a scan has got, as the import page shows it.
type scanProgress struct {
	Phase   string `json:"phase"`
	Current string `json:"current"`
	Found   int    `json:"found"`
	Done    int    `json:"done"`
}

// GET /libraries/{id}/games/import: how far a library's import has got, in one answer.
func handleGameImport(a *App, w http.ResponseWriter, r *http.Request) {
	lib := a.gamesLibrary(w, r)
	if lib == nil {
		return
	}
	review, err := gamescan.ReviewCounts(r.Context(), a.DB.DB, lib.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	counts, err := a.Host.CountsIn(r.Context(), host.For(r), lib.ID, "console")
	if err != nil {
		writeErr(w, 502, "the server can't be reached right now")
		return
	}
	if counts == nil {
		counts = map[string]int{}
	}
	var scan *scanProgress
	if s := scanning(lib.ID); s != nil {
		done, total, current := s.Progress()
		scan = &scanProgress{Phase: "discovering", Current: current, Found: total, Done: done}
	}
	var lookup any
	if gameLookupStatus != nil {
		lookup = gameLookupStatus(r.Context(), a)
	}
	writeJSON(w, 200, map[string]any{
		"scanning": scan != nil,
		"scan":     scan,
		"lookup":   lookup,
		"review":   review,
		"games":    counts,
	})
}

// gameVersion is one way to play a game, for choosing between them.
type gameVersion struct {
	// ID is the version's own item, when it's a file of its own.
	ID int64 `json:"id,omitempty"`
	// Entry is its file in the game's archive, when it's one of the versions in there.
	Entry string `json:"entry,omitempty"`
	Label string `json:"label"`
	// Current: what Play starts. Altered: a bad dump, a hack or a translation.
	Current bool `json:"current,omitempty"`
	Altered bool `json:"altered,omitempty"`
}

// gameAt is the game a /games/{id}/… route names, if the person asking may see it, with its file.
func (a *App) gameAt(w http.ResponseWriter, r *http.Request) (*service.Item, string) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 400, "bad id")
		return nil, ""
	}
	it, err := a.visibleGame(r.Context(), r, id)
	if err != nil || gamePath(it) == "" {
		writeErr(w, 404, "not found")
		return nil, ""
	}
	p := gamePath(it)
	_ = gamescan.NoteItem(r.Context(), a.DB.DB, it.Library, p, it.ID)
	return it, p
}

// GET /games/{id}/versions: its other versions, in files of their own and inside its
// archive, and its discs.
func handleGameVersions(a *App, w http.ResponseWriter, r *http.Request) {
	it, p := a.gameAt(w, r)
	if it == nil {
		return
	}
	ctx := r.Context()
	out := map[string]any{}
	listed, err := gamescan.ListedGame(ctx, a.DB.DB, it.Library, p)
	if err != nil {
		listed = &gamescan.Listed{Library: it.Library, Path: p}
	}
	head := p
	if listed.VersionOf != "" {
		head = listed.VersionOf
	}
	kids, _ := gamescan.VersionsOf(ctx, a.DB.DB, it.Library, head)
	// Which of them the person may see: the server says, for the ones whose ids are known.
	var ids []int64
	headID := it.ID
	if head != p {
		headID = gamescan.ItemAt(ctx, a.DB.DB, it.Library, head)
	}
	if headID > 0 {
		ids = append(ids, headID)
	}
	for _, k := range kids {
		if k.Item > 0 {
			ids = append(ids, k.Item)
		}
	}
	visible := map[int64]bool{it.ID: true}
	if len(ids) > 1 {
		if list, err := a.Host.Items(ctx, host.For(r), host.Query{Library: it.Library, IDs: ids, Limit: len(ids)}); err == nil {
			for _, v := range list {
				visible[v.ID] = true
			}
		}
	}
	var versions []gameVersion
	if head != p {
		if headID > 0 && visible[headID] {
			label := ""
			if h, err := gamescan.ListedGame(ctx, a.DB.DB, it.Library, head); err == nil {
				label = h.Tagline
			}
			versions = append(versions, gameVersion{ID: headID, Label: label})
		}
	} else {
		versions = append(versions, gameVersion{ID: it.ID, Label: listed.Tagline, Current: true})
	}
	for _, k := range kids {
		if k.Item <= 0 || !visible[k.Item] {
			continue
		}
		label := k.Label
		if label == "" {
			label = k.Tagline
		}
		versions = append(versions, gameVersion{ID: k.Item, Label: label, Current: k.Item == it.ID})
	}
	out["versions"] = versions
	if len(listed.Discs) > 0 {
		out["discs"] = listed.Discs
	}
	// What's inside a set, which is only a choice if there's a choice to make. Only an
	// administrator can make it, and answering means opening the archive in a worker.
	if who(r).Admin && gamearchive.IsArchive(p) {
		if inner := a.archiveVersions(ctx, it, p); len(inner) > 1 {
			out["inside"] = inner
		}
	}
	writeJSON(w, 200, out)
}

func (a *App) archiveVersions(ctx context.Context, it *service.Item, p string) []gameVersion {
	console := wittyplay.ByID(it.Facets["console"])
	if console == nil {
		return nil
	}
	l, err := gamearchive.List(p)
	if err != nil {
		return nil
	}
	current := gamescan.Entry(ctx, a.DB.DB, it.Library, p)
	if current == "" {
		if plan, err := gamearchive.Choose(l, console, filepath.Base(p)); err == nil && len(plan.Want) > 0 {
			current = plan.Want[0].Name
		}
	}
	var out []gameVersion
	for _, e := range l.Entries {
		x := strings.ToLower(path.Ext(e.Name))
		if !console.Accepts(x) {
			continue
		}
		switch x {
		case ".cue", ".gdi", ".m3u", ".ccd", ".toc":
			return nil // a disc, with its tracks: nothing to choose between
		}
		base := path.Base(e.Name)
		q := gameid.QualityOf(base)
		out = append(out, gameVersion{Entry: e.Name, Label: strings.TrimSuffix(base, path.Ext(base)), Current: e.Name == current, Altered: q >= gameid.QualityTranslated})
	}
	return out
}

// PUT /games/{id}/version {entry}: which file in an archive to play. Empty goes back
// to choosing by itself.
func handleGameVersionPick(a *App, w http.ResponseWriter, r *http.Request) {
	it, p := a.gameAt(w, r)
	if it == nil {
		return
	}
	var b struct {
		Entry string `json:"entry"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if b.Entry != "" {
		// Only something this archive holds, and that this console can run.
		ok := false
		for _, v := range a.archiveVersions(r.Context(), it, p) {
			ok = ok || v.Entry == b.Entry
		}
		if !ok {
			writeErr(w, 400, "this game's archive has no such file")
			return
		}
	}
	if err := gamescan.PinEntry(r.Context(), a.DB.DB, it.Library, p, b.Entry); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if onEntryPinned != nil {
		onEntryPinned(r.Context(), a, it)
	}
	writeJSON(w, 200, map[string]any{"entry": b.Entry})
}

// POST /games/{id}/split: a version becomes a game of its own, and stays one.
func handleGameSplit(a *App, w http.ResponseWriter, r *http.Request) {
	it, p := a.gameAt(w, r)
	if it == nil {
		return
	}
	if err := gamescan.Decide(r.Context(), a.DB.DB, it.Library, p, "own", ""); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.rescan(r.Context(), it.Library)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// PUT /games/{id}/console {console}: this file is really for another console.
func handleGameConsoleFix(a *App, w http.ResponseWriter, r *http.Request) {
	it, p := a.gameAt(w, r)
	if it == nil {
		return
	}
	var b struct {
		Console string `json:"console"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !consoleKnown(b.Console) {
		writeErr(w, 400, "no such console")
		return
	}
	if err := gamescan.Decide(r.Context(), a.DB.DB, it.Library, p, "add", b.Console); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.rescan(r.Context(), it.Library)
	writeJSON(w, 200, map[string]any{"console": b.Console})
}

// POST /games/{id}/not-a-game: this isn't a game. Its file stays where it is, and it
// goes to the list of what's been left out, from where it can be brought back.
func handleGameNotAGame(a *App, w http.ResponseWriter, r *http.Request) {
	it, p := a.gameAt(w, r)
	if it == nil {
		return
	}
	if err := gamescan.Decide(r.Context(), a.DB.DB, it.Library, p, "ignore", ""); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.rescan(r.Context(), it.Library)
	writeJSON(w, 200, map[string]any{"ok": true})
}
