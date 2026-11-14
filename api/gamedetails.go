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
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// What's known about games, and where it comes from (moved from the server's api_games_meta.go).
// The work happens in the background (gamedetails_worker.go); these routes show it and steer it.

// gmBase is where WittyPlay's routes are, for addresses it gives out.
const gmBase = "/api/modules/wittyplay"

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/facets", w(handleGameFacets))
		mux.Handle("GET /games/{id}/metadata", w(handleGameMetadata))
		mux.Handle("GET /games/{id}/art/{n}", w(handleGameArt))
		mux.Handle("GET /games/{id}/metadata/search", w(adminRoute(handleGameSearch)))
		mux.Handle("PUT /games/{id}/metadata/match", w(adminRoute(handleGameMatch)))
		mux.Handle("DELETE /games/{id}/metadata/match", w(adminRoute(handleGameUnmatch)))
		mux.Handle("POST /games/{id}/metadata/refresh", w(adminRoute(handleGameRefresh)))
		mux.Handle("GET /gamemeta/status", w(adminRoute(handleGameMetaStatus)))
		mux.Handle("POST /gamemeta/refetch", w(adminRoute(handleGameMetaRefetch)))
	})
}

// gameDetailsExtra fills in what identification knows of a game (how its console was worked
// out, its discs, a problem playing it, the entry chosen in its archive), set by the part that
// identifies games, in an init. Unset, those stay empty.
var gameDetailsExtra func(ctx context.Context, a *App, id int64, d *gameDetails)

// gameArt is one of a game's pictures.
type gameArt struct {
	Kind string `json:"kind"` // screenshot | title | artwork
	URL  string `json:"url"`  // on this server
}

// gameDetails is everything known about a game beyond what the server's item holds.
type gameDetails struct {
	ItemID int64 `json:"itemId"`
	// Status: pending (still being looked up) or done.
	Status string `json:"status"`
	// Locked: the match stays as it is until someone changes it.
	Locked bool `json:"locked"`
	// MatchSource is the source most sure of this match, MatchMethod how it knows (checksum,
	// serial, name or manual), and Confidence how sure, out of 100. 0: no match.
	MatchSource string `json:"matchSource"`
	MatchMethod string `json:"matchMethod"`
	Confidence  int    `json:"confidence"`
	// Tags are what the file's name says beyond the title: regions, revisions.
	Tags      string               `json:"tags"`
	Released  string               `json:"released"` // YYYY, YYYY-MM or YYYY-MM-DD
	Developer string               `json:"developer"`
	Publisher string               `json:"publisher"`
	Franchise string               `json:"franchise"`
	Players   int                  `json:"players"` // 0: unknown
	Scores    []gamemeta.Score     `json:"scores"`
	Ages      []gamemeta.AgeRating `json:"ages"`
	Art       []gameArt            `json:"art"`
	Credits   []gamemeta.Credit    `json:"credits"`
	// Checksums and serial, for administrators, to help fix a wrong match.
	CRCs   string `json:"crcs,omitempty"`
	Serial string `json:"serial,omitempty"`
	// How its console was worked out: bytes, extension, folder or chosen; how sure, out of 100;
	// and why, in words (gameDetailsExtra).
	DetectedBy string `json:"detectedBy,omitempty"`
	DetectSure int    `json:"detectSure,omitempty"`
	DetectWhy  string `json:"detectWhy,omitempty"`
	// Problem says why it can't play as it is: "encrypted", or empty. ProblemText says that in
	// words, and says what would.
	Problem     string `json:"problem,omitempty"`
	ProblemText string `json:"problemText,omitempty"`
	// Discs are its discs, when it has more than one.
	Discs any `json:"discs,omitempty"`
	// Entry is the file to play, when someone chose one from an archive holding several.
	Entry string `json:"entry,omitempty"`
}

// gameDetailsOf reads what's known about a game.
func (a *App) gameDetailsOf(ctx context.Context, id int64) (*gameDetails, error) {
	d := &gameDetails{ItemID: id, Status: "pending", Scores: []gamemeta.Score{}, Ages: []gamemeta.AgeRating{}, Art: []gameArt{}, Credits: []gamemeta.Credit{}}
	var locked int
	var scores, ages, art, credits string
	err := a.DB.QueryRowContext(ctx, `SELECT status, locked, match_source, match_method, confidence, tags, released, developer, publisher, franchise, players, scores, ages, art, credits, crcs, serial, chosen_entry
		FROM wittyplay_game_details WHERE item_id = ?`, id).Scan(&d.Status, &locked, &d.MatchSource, &d.MatchMethod, &d.Confidence, &d.Tags, &d.Released, &d.Developer, &d.Publisher,
		&d.Franchise, &d.Players, &scores, &ages, &art, &credits, &d.CRCs, &d.Serial, &d.Entry)
	if err != nil && !isNoRows(err) {
		return nil, err
	}
	if err == nil {
		d.Locked = locked != 0
		_ = json.Unmarshal([]byte(scores), &d.Scores)
		_ = json.Unmarshal([]byte(ages), &d.Ages)
		_ = json.Unmarshal([]byte(credits), &d.Credits)
		var refs []gamemeta.ArtRef
		_ = json.Unmarshal([]byte(art), &refs)
		for i, r := range refs {
			d.Art = append(d.Art, gameArt{Kind: r.Kind, URL: fmt.Sprintf(gmBase+"/games/%d/art/%d", id, i)})
		}
	}
	if gameDetailsExtra != nil {
		gameDetailsExtra(ctx, a, id, d)
	}
	if d.Scores == nil {
		d.Scores = []gamemeta.Score{}
	}
	if d.Ages == nil {
		d.Ages = []gamemeta.AgeRating{}
	}
	if d.Credits == nil {
		d.Credits = []gamemeta.Credit{}
	}
	return d, nil
}

// gameArtFile gets one of a game's extra pictures, fetching it the first time anyone looks.
func (g *gameMeta) gameArtFile(ctx context.Context, id int64, n int) (string, error) {
	var art string
	if err := g.a.DB.QueryRowContext(ctx, `SELECT art FROM wittyplay_game_details WHERE item_id = ?`, id).Scan(&art); err != nil {
		return "", errNoPicture
	}
	var refs []gamemeta.ArtRef
	_ = json.Unmarshal([]byte(art), &refs)
	if n < 0 || n >= len(refs) {
		return "", errNoPicture
	}
	return g.fetchPicture(ctx, refs[n].Ref)
}

// gameOf is the game a /games/{id}/... route names, if the person may see it, with its files.
func (a *App) gameOf(w http.ResponseWriter, r *http.Request) (metaGame, bool) {
	it, ok := a.personalGame(r)
	if !ok || it == nil {
		writeErr(w, 404, "game not found")
		return metaGame{}, false
	}
	return metaGameOf(it), true
}

func metaGameOf(it *service.Item) metaGame {
	g := metaGame{ID: it.ID, Library: it.Library, Console: itemConsole(it), Title: it.Title}
	for _, f := range it.Files {
		g.Files = append(g.Files, f.Path)
	}
	return g
}

// GET /games/{id}/metadata
func handleGameMetadata(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	d, err := a.gameDetailsOf(r.Context(), it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !who(r).Admin {
		d.CRCs, d.Serial, d.DetectedBy, d.DetectWhy, d.Entry = "", "", "", "", ""
		d.DetectSure = 0
	}
	writeJSON(w, 200, d)
}

// GET /games/{id}/art/{n}: one of a game's screenshots, fetched by WittyPlay the first time
// anyone looks, so a browser never talks to the source.
func handleGameArt(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeErr(w, 400, "bad picture number")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	file, err := a.gameWorker().gameArtFile(ctx, it.ID, n)
	if err != nil {
		writeErr(w, 404, "no such picture")
		return
	}
	servePicture(w, file, "private, max-age=604800")
}

// servePicture sends a picture from WittyPlay's own folder, as what its bytes say it is.
func servePicture(w http.ResponseWriter, file, cache string) {
	b, err := os.ReadFile(file)
	if err != nil {
		writeErr(w, 404, "no such picture")
		return
	}
	ct := http.DetectContentType(b)
	if !strings.HasPrefix(ct, "image/") {
		writeErr(w, 404, "no such picture")
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", cache)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// GET /games/{id}/metadata/search?q=: games this might be, from every source in use.
func handleGameSearch(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		q = it.Title
	}
	if len(q) > 200 {
		q = clipText(q, 200)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	results, failed := a.gameWorker().search(ctx, it.Console, q)
	if failed == nil {
		failed = []string{}
	}
	writeJSON(w, 200, map[string]any{"query": q, "results": results, "failed": failed})
}

// PUT /games/{id}/metadata/match {source, id, lock}: this is the right game. With no source, only
// changes whether the match is locked.
func handleGameMatch(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	var b struct {
		Source string `json:"source"`
		ID     string `json:"id"`
		Lock   *bool  `json:"lock"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	lock := b.Lock == nil || *b.Lock
	g := a.gameWorker()
	var err error
	if b.Source == "" {
		if b.Lock == nil {
			writeErr(w, 400, "say which game this is, or whether to lock the match")
			return
		}
		err = g.setLocked(ctx, it, lock)
	} else {
		err = g.choose(ctx, it, b.Source, b.ID, lock)
	}
	if err != nil {
		writeErr(w, sourceStatus(err), sourceMessage(err))
		return
	}
	d, err := a.gameDetailsOf(ctx, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, d)
}

// DELETE /games/{id}/metadata/match: this match is wrong, and there's no right one.
func handleGameUnmatch(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	if err := a.gameWorker().clearMatch(r.Context(), it); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /games/{id}/metadata/refresh: ask the sources about this game again.
func handleGameRefresh(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.gameWorker().refresh(ctx, it); err != nil {
		writeErr(w, sourceStatus(err), sourceMessage(err))
		return
	}
	writeJSON(w, 202, map[string]any{"started": true})
}

// sourceStatus and sourceMessage turn what a source said into an answer. The errors here never
// hold addresses or keys: gamemeta.Client strips them.
func sourceStatus(err error) int {
	switch {
	case errors.Is(err, gamemeta.ErrNotFound):
		return 404
	case errors.Is(err, gamemeta.ErrKey), errors.Is(err, gamemeta.ErrNotSet):
		return 409
	}
	return 502
}

func sourceMessage(err error) string {
	switch {
	case errors.Is(err, gamemeta.ErrNotFound):
		return "That source doesn't know this game."
	case errors.Is(err, gamemeta.ErrKey):
		return "The source didn't accept its key. Check it in WittyPlay's settings, Game details."
	case errors.Is(err, gamemeta.ErrNotSet):
		return "That source needs a key. Add one in WittyPlay's settings, Game details."
	}
	return err.Error()
}

// gameMetaStatus is how the work is going.
type gameMetaStatus struct {
	Running bool   `json:"running"`
	Phase   string `json:"phase"` // details | pictures | ""
	Total   int    `json:"total"`
	Pending int    `json:"pending"`
	Matched int    `json:"matched"`
	// ByChecksum counts matches by exact file or disc.
	ByChecksum  int                         `json:"byChecksum"`
	Pictures    int                         `json:"pictures"` // games whose pictures are still to fetch
	WithPoster  int                         `json:"withPoster"`
	WithDetails int                         `json:"withDetails"`
	Consoles    map[string]gameConsoleTally `json:"consoles"`
	Paused      []gameSourcePause           `json:"paused"`
	// KeysSet: at least one source that takes a key has one. Pages use this to suggest one.
	KeysSet bool `json:"keysSet"`
}

// gameConsoleTally counts one console's games.
type gameConsoleTally struct {
	Total      int `json:"total"`
	Matched    int `json:"matched"`
	ByChecksum int `json:"byChecksum"`
}

// gameSourcePause is a source that's been asked to wait.
type gameSourcePause struct {
	Source string `json:"source"`
	Until  int64  `json:"until"`
	Why    string `json:"why"`
}

// status says how the work is going.
func (g *gameMeta) status(ctx context.Context) gameMetaStatus {
	out := gameMetaStatus{Consoles: map[string]gameConsoleTally{}, Paused: []gameSourcePause{}}
	g.mu.Lock()
	out.Running, out.Phase = g.running, g.phase
	for id, p := range g.paused {
		if time.Now().Before(p.Until) {
			out.Paused = append(out.Paused, gameSourcePause{Source: id, Until: p.Until.Unix(), Why: p.Why})
		}
	}
	g.mu.Unlock()
	sort.Slice(out.Paused, func(a, b int) bool { return out.Paused[a].Source < out.Paused[b].Source })
	rows, err := g.a.DB.QueryContext(ctx, `SELECT console, COUNT(*), SUM(status = 'pending'), SUM(confidence >= ?), SUM(match_method IN ('checksum', 'serial')),
		SUM(art_wanted = 1), SUM(poster_url <> '' OR (',' || edited || ',') LIKE '%,poster,%'), SUM(has_overview = 1)
		FROM wittyplay_game_details WHERE path <> '' AND console <> ? GROUP BY 1`, gamemeta.AcceptConfidence, wittyplay.TestConsole)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c string
		var total, pending, matched, exact, art, posters, details int
		if rows.Scan(&c, &total, &pending, &matched, &exact, &art, &posters, &details) != nil {
			continue
		}
		out.Total += total
		out.Pending += pending
		out.Matched += matched
		out.ByChecksum += exact
		out.Pictures += art
		out.WithPoster += posters
		out.WithDetails += details
		out.Consoles[c] = gameConsoleTally{Total: total, Matched: matched, ByChecksum: exact}
	}
	return out
}

// GET /gamemeta/status
func handleGameMetaStatus(a *App, w http.ResponseWriter, r *http.Request) {
	g := a.gameWorker()
	keys := g.readKeys(r.Context())
	st := g.status(r.Context())
	for _, k := range gameSourceKeys {
		if k != "ssDevId" && k != "ssDevPassword" && !strings.EqualFold(k, "retroAchievementsKey") && keys[k] != "" {
			st.KeysSet = true
		}
	}
	writeJSON(w, 200, st)
}

// POST /gamemeta/refetch: forget what sources said about every game and ask again.
func handleGameMetaRefetch(a *App, w http.ResponseWriter, r *http.Request) {
	if err := a.gameWorker().refetchAll(r.Context()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"started": true})
}

// ---------------------------------------------------------------- facets

// gameFacets are what a library's games can be filtered by, each with how many there are.
type gameFacets struct {
	Genres  []gameFacet `json:"genres"`
	Decades []gameFacet `json:"decades"`
	Players []gameFacet `json:"players"`
	Rated   int         `json:"rated"` // games with a score
	Series  []gameFacet `json:"series"`
	Letters []gameFacet `json:"letters"` // first letters of the titles, in title order
}

// gameFacet is one choice in a filter.
type gameFacet struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// facetItem is what facets read of the server's brief item.
type facetItem struct {
	ID        int64    `json:"id"`
	SortTitle string   `json:"sortTitle"`
	Title     string   `json:"title"`
	Year      *int     `json:"year"`
	Genres    []string `json:"genres"`
	Rating    *float64 `json:"rating"`
}

// GET /games/facets?library=&console=: what a library's games can be filtered by, as far as this
// account can see. One console narrows it. The items and what the server knows of them come from
// the server; players and series are WittyPlay's.
func handleGameFacets(a *App, w http.ResponseWriter, r *http.Request) {
	lib, err := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
	if err != nil || lib <= 0 {
		writeErr(w, 404, "library not found")
		return
	}
	console := r.URL.Query().Get("console")
	ctx := r.Context()
	q := host.Query{Library: lib, Kind: "game", Limit: 500}
	if console != "" {
		q.Facets = map[string]string{"console": console}
	}
	var items []facetItem
	for {
		page, err := a.Host.ItemsFull(ctx, host.For(r), q)
		if err != nil {
			if host.IsNotFound(err) || host.IsForbidden(err) {
				writeErr(w, 404, "library not found")
				return
			}
			writeErr(w, 502, "the server couldn't list the games")
			return
		}
		for _, raw := range page {
			var it facetItem
			if json.Unmarshal(raw, &it) == nil {
				items = append(items, it)
			}
		}
		if len(page) < q.Limit {
			break
		}
		q.Offset += len(page)
	}
	f, err := a.gameFacetsOf(ctx, items)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, f)
}

func (a *App) gameFacetsOf(ctx context.Context, items []facetItem) (*gameFacets, error) {
	out := &gameFacets{Genres: []gameFacet{}, Decades: []gameFacet{}, Players: []gameFacet{}, Series: []gameFacet{}, Letters: []gameFacet{}}
	genres, decades, letters := map[string]int{}, map[int]int{}, map[string]int{}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
		for _, g := range it.Genres {
			if g != "" {
				genres[g]++
			}
		}
		if it.Year != nil && *it.Year > 0 {
			decades[*it.Year/10*10]++
		}
		if it.Rating != nil {
			out.Rated++
		}
		t := it.SortTitle
		if t == "" {
			t = it.Title
		}
		if c, _ := utf8.DecodeRuneInString(t); c != utf8.RuneError {
			letters[string(unicode.ToUpper(c))]++
		}
	}
	for g, n := range genres {
		out.Genres = append(out.Genres, gameFacet{g, n})
	}
	sort.Slice(out.Genres, func(a, b int) bool {
		if out.Genres[a].Count != out.Genres[b].Count {
			return out.Genres[a].Count > out.Genres[b].Count
		}
		return out.Genres[a].Value < out.Genres[b].Value
	})
	for d, n := range decades {
		out.Decades = append(out.Decades, gameFacet{strconv.Itoa(d), n})
	}
	sort.Slice(out.Decades, func(a, b int) bool { return out.Decades[a].Value < out.Decades[b].Value })
	for l, n := range letters {
		out.Letters = append(out.Letters, gameFacet{l, n})
	}
	sort.Slice(out.Letters, func(a, b int) bool {
		return strings.ToLower(out.Letters[a].Value) < strings.ToLower(out.Letters[b].Value)
	})
	// Players and series are what WittyPlay found out.
	players, series := map[int]int{}, map[string]int{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		chunk := ids[:n]
		ids = ids[n:]
		args := make([]any, len(chunk))
		marks := make([]string, len(chunk))
		for i, id := range chunk {
			args[i], marks[i] = id, "?"
		}
		rows, err := a.DB.QueryContext(ctx, `SELECT players, franchise FROM wittyplay_game_details WHERE item_id IN (`+strings.Join(marks, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var p int
			var f string
			if rows.Scan(&p, &f) == nil {
				if p > 0 {
					players[p]++
				}
				if f != "" {
					series[f]++
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for p, n := range players {
		out.Players = append(out.Players, gameFacet{strconv.Itoa(p), n})
	}
	sort.Slice(out.Players, func(a, b int) bool {
		pa, _ := strconv.Atoi(out.Players[a].Value)
		pb, _ := strconv.Atoi(out.Players[b].Value)
		return pa < pb
	})
	for s, n := range series {
		out.Series = append(out.Series, gameFacet{s, n})
	}
	sort.Slice(out.Series, func(a, b int) bool {
		if out.Series[a].Count != out.Series[b].Count {
			return out.Series[a].Count > out.Series[b].Count
		}
		return strings.ToLower(out.Series[a].Value) < strings.ToLower(out.Series[b].Value)
	})
	if len(out.Series) > 200 {
		out.Series = out.Series[:200]
	}
	return out, nil
}

// ---------------------------------------------------------------- the host, for the work above

// hostOn asks the server as WittyPlay itself, on nobody's behalf.
func hostOn() host.On { return host.On{} }

func hostQueryIDs(ids []int64) host.Query { return host.Query{IDs: ids, Limit: len(ids)} }

func isHostNotFound(err error) bool { return host.IsNotFound(err) }
