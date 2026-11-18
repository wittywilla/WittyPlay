// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/gamemeta"
	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// How WittyPlay pictures games (WittyPlay Settings › Game pictures; moved from the server's
// game_pictures.go and api_game_pictures.go): box art fitted or filled, or drawn in a cartridge or
// case. A person's own choices live in their profile (/me/ui, wittyplayPictures); WittyPlay's are
// here: its default and per-console looks (setting "pictures"), and per-game looks an
// administrator chose or a .wittyplay file beside the game gives. The file format is
// docs/specs/wittyplay-file.md.

// Where a game's look came from.
const (
	pictureByAdmin = "admin"
	pictureByFile  = "file"
)

// cfgPictures holds WittyPlay's own .wittyplay document: its default and per-console looks.
const cfgPictures = "pictures"

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/pictures", w(handleGamePictures))
		mux.Handle("PUT /games/pictures", w(adminRoute(handleGamePicturesPut)))
		mux.Handle("GET /games/pictures/file", w(adminRoute(handleGamePicturesFile)))
		mux.Handle("POST /games/pictures/check", w(handleGamePicturesCheck))
		mux.Handle("PUT /games/{id}/picture", w(adminRoute(handleGamePicturePut)))
	})
}

// setGamePicture keeps one game's look from one source; an empty look takes it away.
func (a *App) setGamePicture(ctx context.Context, itemID int64, source string, l wittyplayfile.Look) error {
	l.Extra = nil
	if l.Empty() {
		_, err := a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_pictures WHERE item_id = ? AND source = ?`, itemID, source)
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = a.DB.ExecContext(ctx, `INSERT INTO wittyplay_game_pictures(item_id, source, look, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(item_id, source) DO UPDATE SET look = excluded.look, updated_at = excluded.updated_at`, itemID, source, string(b), time.Now().Unix())
	return err
}

// gamePictures is every game's look, by item: an administrator's choice over a file's, field by field.
func (a *App) gamePictures(ctx context.Context) (map[int64]wittyplayfile.Look, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT item_id, source, look FROM wittyplay_game_pictures ORDER BY item_id, source = 'admin' DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]wittyplayfile.Look{}
	for rows.Next() {
		var id int64
		var source, raw string
		if err := rows.Scan(&id, &source, &raw); err != nil {
			return nil, err
		}
		var l wittyplayfile.Look
		if json.Unmarshal([]byte(raw), &l) != nil {
			continue
		}
		l.Extra = nil
		out[id] = out[id].Over(l)
	}
	return out, rows.Err()
}

// gamePictureFrom is one game's look from one source, if there is one.
func (a *App) gamePictureFrom(ctx context.Context, itemID int64, source string) (wittyplayfile.Look, error) {
	var raw string
	err := a.DB.QueryRowContext(ctx, `SELECT look FROM wittyplay_game_pictures WHERE item_id = ? AND source = ?`, itemID, source).Scan(&raw)
	if isNoRows(err) {
		return wittyplayfile.Look{}, nil
	}
	if err != nil {
		return wittyplayfile.Look{}, err
	}
	var l wittyplayfile.Look
	err = json.Unmarshal([]byte(raw), &l)
	l.Extra = nil
	return l, err
}

// serverPictures is WittyPlay's own .wittyplay document, or an empty one.
func (a *App) serverPictures(ctx context.Context) *wittyplayfile.Doc {
	var raw json.RawMessage
	if ok, err := a.Config(ctx, cfgPictures, &raw); ok && err == nil {
		if d, err := wittyplayfile.Parse(raw); err == nil {
			return d
		}
	}
	return &wittyplayfile.Doc{Version: wittyplayfile.Version}
}

// setServerPictures keeps WittyPlay's own document: its default and consoles. Its games are kept
// per game instead (setGamePicture), so they're dropped here.
func (a *App) setServerPictures(ctx context.Context, d *wittyplayfile.Doc) error {
	keep := *d
	keep.Games = nil
	keep.Version = wittyplayfile.Version
	if err := keep.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(keep)
	if err != nil {
		return err
	}
	return a.SetConfig(ctx, cfgPictures, json.RawMessage(b))
}

// gameMatch finds the games a .wittyplay entry is for, among the server's games: by its id, or by
// its file name and title (and console, when it says one). At most limit. The server is asked as
// WittyPlay; the caller checks who may see them.
func (a *App) gameMatch(ctx context.Context, m wittyplayfile.Match, limit int) ([]int64, error) {
	check := func(id int64) (bool, error) {
		it, err := a.Host.Item(ctx, hostOn(), id)
		if host.IsNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		file := ""
		if len(it.Files) > 0 {
			file = pathBase(it.Files[0].Path)
		}
		return it.Kind == "game" && m.Matches(it.ID, itemConsole(&it), file, it.Title), nil
	}
	if m.Item > 0 {
		ok, err := check(m.Item)
		if !ok || err != nil {
			return nil, err
		}
		return []int64{m.Item}, nil
	}
	search := strings.TrimSpace(m.Title)
	if search == "" && m.File != "" {
		search, _ = gamemeta.CleanName(gameBaseName(m.File))
	}
	if search == "" {
		return nil, nil
	}
	q := host.Query{LibraryKind: "games", Kind: "game", Search: search, Limit: 500}
	if m.Console != "" {
		q.Facets = map[string]string{"console": m.Console}
	}
	items, err := a.Host.Items(ctx, hostOn(), q)
	if err != nil {
		return nil, err
	}
	var out []int64
	asked := 0
	for _, it := range items {
		if len(out) >= limit {
			break
		}
		if m.Title != "" && !strings.EqualFold(strings.TrimSpace(it.Title), strings.TrimSpace(m.Title)) {
			continue
		}
		if m.Console != "" && itemConsole(&it) != m.Console {
			continue
		}
		if m.File == "" {
			if m.Matches(it.ID, itemConsole(&it), "", it.Title) {
				out = append(out, it.ID)
			}
			continue
		}
		// The file name is only on the item itself.
		if asked++; asked > 100 {
			break
		}
		ok, err := check(it.ID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, it.ID)
		}
	}
	return out, nil
}

// pathBase is a path's last part, whichever separator it was written with.
func pathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

// visibleIDs keeps the games of ids the person asking may see.
func (a *App) visibleIDs(ctx context.Context, r *http.Request, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		items, err := a.Host.Items(ctx, host.For(r), hostQueryIDs(ids[:n]))
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Kind == "game" {
				out[it.ID] = true
			}
		}
		ids = ids[n:]
	}
	return out, nil
}

// readWittyplay reads a .wittyplay file sent as a request's body, refusing anything over the limit.
func readWittyplay(r *http.Request) (*wittyplayfile.Doc, error) {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, wittyplayfile.MaxSize+1))
	if err != nil {
		return nil, err
	}
	return wittyplayfile.Parse(b)
}

func wittyplayErr(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, wittyplayfile.ErrTooBig) {
		code = http.StatusRequestEntityTooLarge
	}
	writeErr(w, code, err.Error())
}

// GET /games/pictures: WittyPlay's own document (default and consoles), and the games it has a look
// for that this account may see, by id. The case styles WittyPlay draws come too.
func handleGamePictures(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	all, err := a.gamePictures(ctx)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	ids := make([]int64, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	seen, err := a.visibleIDs(ctx, r, ids)
	if err != nil {
		writeErr(w, 502, "the server couldn't say which games you can see")
		return
	}
	games := map[string]wittyplayfile.Look{}
	for id, l := range all {
		if seen[id] {
			games[strconv.FormatInt(id, 10)] = l
		}
	}
	writeJSON(w, 200, map[string]any{"server": a.serverPictures(ctx), "games": games, "caseStyles": wittyplayfile.CaseStyles})
}

// PUT /games/pictures: a .wittyplay document for the whole server. Its default and consoles become
// WittyPlay's; each of its games is applied to the games here it matches, as an administrator's choice.
func handleGamePicturesPut(a *App, w http.ResponseWriter, r *http.Request) {
	d, err := readWittyplay(r)
	if err != nil {
		wittyplayErr(w, err)
		return
	}
	ctx := r.Context()
	if err := a.setServerPictures(ctx, d); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	applied, unmatched := 0, 0
	for _, g := range d.Games {
		ids, err := a.gameMatch(ctx, g.Match, 50)
		if err != nil {
			writeErr(w, 502, "the server couldn't say which games these are")
			return
		}
		if len(ids) == 0 {
			unmatched++
		}
		for _, id := range ids {
			was, _ := a.gamePictureFrom(ctx, id, pictureByAdmin)
			if err := a.setGamePicture(ctx, id, pictureByAdmin, g.Look.Over(was)); err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			applied++
		}
	}
	a.Log.InfoContext(ctx, "wittyplay: game pictures saved", "cat", "games", "by", who(r).User, "games", applied, "unmatched", unmatched)
	writeJSON(w, 200, map[string]any{"server": a.serverPictures(ctx), "applied": applied, "unmatched": unmatched})
}

// GET /games/pictures/file: WittyPlay's choices as a .wittyplay file to download: its default, its
// consoles, and every game an administrator chose for, matched by file name, title and console
// (and this server's id) so another server can find them.
func handleGamePicturesFile(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d := a.serverPictures(ctx)
	d.Generator = "WittyPlay"
	rows, err := a.DB.QueryContext(ctx, `SELECT item_id, look FROM wittyplay_game_pictures WHERE source = ? ORDER BY item_id LIMIT ?`, pictureByAdmin, wittyplayfile.MaxGames)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	type picked struct {
		id  int64
		raw string
	}
	var list []picked
	for rows.Next() {
		var p picked
		if err := rows.Scan(&p.id, &p.raw); err != nil {
			rows.Close()
			writeErr(w, 500, err.Error())
			return
		}
		list = append(list, p)
	}
	rows.Close()
	type named struct {
		g    wittyplayfile.Game
		sort string
	}
	var games []named
	for _, p := range list {
		it, err := a.Host.Item(ctx, hostOn(), p.id)
		if err != nil {
			continue // gone, or the server can't say: left out
		}
		g := wittyplayfile.Game{Match: wittyplayfile.Match{Item: p.id, Title: it.Title, Console: itemConsole(&it)}}
		if len(it.Files) > 0 {
			g.Match.File = pathBase(it.Files[0].Path)
		}
		if err := g.Look.UnmarshalJSON([]byte(p.raw)); err != nil {
			continue
		}
		if len(g.Match.File) > wittyplayfile.MaxName {
			g.Match.File = ""
		}
		if len(g.Match.Title) > wittyplayfile.MaxName {
			g.Match.Title = ""
		}
		games = append(games, named{g, strings.ToLower(it.Title)})
	}
	sort.SliceStable(games, func(i, j int) bool { return games[i].sort < games[j].sort })
	for _, n := range games {
		d.Games = append(d.Games, n.g)
	}
	b, err := d.Encode()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", wittyplayfile.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="server-game-pictures`+wittyplayfile.Ext+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// POST /games/pictures/check: reads a .wittyplay file for someone about to import it into their own
// choices. It says whether the file is valid and, for each of its games, which of this account's
// games it matches.
func handleGamePicturesCheck(a *App, w http.ResponseWriter, r *http.Request) {
	d, err := readWittyplay(r)
	if err != nil {
		wittyplayErr(w, err)
		return
	}
	ctx := r.Context()
	matches := make([][]int64, len(d.Games))
	for i, g := range d.Games {
		ids, err := a.gameMatch(ctx, g.Match, 50)
		if err != nil {
			writeErr(w, 502, "the server couldn't say which games these are")
			return
		}
		seen, err := a.visibleIDs(ctx, r, ids)
		if err != nil {
			writeErr(w, 502, "the server couldn't say which games you can see")
			return
		}
		matches[i] = []int64{}
		for _, id := range ids {
			if seen[id] {
				matches[i] = append(matches[i], id)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"doc": d, "matches": matches})
}

// PUT /games/{id}/picture {picture, case, label}: an administrator's look for one game, for
// everyone on this server; an empty one takes it away.
func handleGamePicturePut(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	var l wittyplayfile.Look
	if err := readJSON(r, &l); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	probe := wittyplayfile.Doc{Version: wittyplayfile.Version, Default: &l}
	if err := probe.Validate(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	if err := a.setGamePicture(ctx, it.ID, pictureByAdmin, l); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	got, err := a.gamePictureFrom(ctx, it.ID, pictureByAdmin)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, got)
}
