// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyPlay - Console games streamed to a browser or an app, with every controller and a touch layout editor.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay"
)

// Controller profiles: named mappings for a controller, a console or one game, kept per
// person. Moved from the server (api_game_profiles.go, library/game_profiles.go) as they were.
// No route takes a person's id: there is nothing to change to reach someone else's.

func profileRoutes(mux *http.ServeMux, with func(func(*App, http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("GET /game-controllers", with(handleGameControllers))
	mux.Handle("GET /game-profiles", with(handleGameProfiles))
	mux.Handle("POST /game-profiles", with(handleGameProfileCreate))
	mux.Handle("GET /game-profiles/resolve", with(handleGameProfileResolve))
	mux.Handle("GET /game-profiles/export", with(handleGameProfileExport))
	mux.Handle("POST /game-profiles/import", with(handleGameProfileImport))
	mux.Handle("GET /game-profiles/{id}", with(handleGameProfileGet))
	mux.Handle("PUT /game-profiles/{id}", with(handleGameProfilePut))
	mux.Handle("DELETE /game-profiles/{id}", with(handleGameProfileDelete))
	mux.Handle("POST /game-profiles/{id}/copy", with(handleGameProfileCopy))
	mux.Handle("POST /game-profiles/{id}/active", with(handleGameProfileActive))
}

var (
	errTooManyProfiles = errors.New("you have as many controller profiles as an account can keep; delete one first")
	errNoProfile       = errors.New("no such profile")
)

// GET /game-controllers: the controllers this server knows by vendor and product.
func handleGameControllers(a *App, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, max-age=3600")
	b, _ := json.Marshal(wittyplay.KnownControllers())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(append(b, '\n'))
}

// cleanGameProfile checks a profile from a client, and that its game is one this person can see,
// on the console it says.
func (a *App) cleanGameProfile(r *http.Request, p wittyplay.Profile) (wittyplay.Profile, error) {
	clean, err := wittyplay.CleanProfile(p, consoleKnown)
	if err != nil {
		return p, err
	}
	if clean.Game != 0 {
		it, err := a.visibleGame(r.Context(), r, clean.Game)
		if err != nil || it.Facets["console"] != clean.Console {
			return p, errors.New("that game isn't in your libraries")
		}
	}
	return clean, nil
}

func (a *App) profileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNoProfile):
		writeErr(w, 404, "not found")
	case errors.Is(err, errTooManyProfiles):
		writeErr(w, 409, err.Error())
	default:
		a.Log.WarnContext(r.Context(), "could not save or read a game profile", "cat", "games", "err", err)
		writeErr(w, 500, err.Error())
	}
}

func readProfile(w http.ResponseWriter, r *http.Request) (wittyplay.Profile, bool) {
	var p wittyplay.Profile
	r.Body = http.MaxBytesReader(w, r.Body, wittyplay.MaxProfileSize)
	if err := readJSON(r, &p); err != nil {
		writeErr(w, 400, "this profile can't be read")
		return p, false
	}
	return p, true
}

func handleGameProfiles(a *App, w http.ResponseWriter, r *http.Request) {
	list, err := a.gameProfiles(r.Context(), r, who(r).User)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	q := r.URL.Query()
	out := list[:0]
	for _, p := range list {
		if (q.Get("kind") == "" || q.Get("kind") == p.Kind) && (q.Get("console") == "" || p.Console == "" || q.Get("console") == p.Console) {
			out = append(out, p)
		}
	}
	writeJSON(w, 200, out)
}

func handleGameProfileCreate(a *App, w http.ResponseWriter, r *http.Request) {
	p, ok := readProfile(w, r)
	if !ok {
		return
	}
	p.ID = 0
	clean, err := a.cleanGameProfile(r, p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	saved, err := a.saveGameProfile(r.Context(), r, who(r).User, clean)
	if err != nil {
		a.profileError(w, r, err)
		return
	}
	writeJSON(w, 201, saved)
}

func (a *App) ownProfile(w http.ResponseWriter, r *http.Request) (wittyplay.Profile, bool) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 404, "not found")
		return wittyplay.Profile{}, false
	}
	p, err := a.gameProfile(r.Context(), r, who(r).User, id)
	if err != nil {
		a.profileError(w, r, err)
		return p, false
	}
	return p, true
}

func handleGameProfileGet(a *App, w http.ResponseWriter, r *http.Request) {
	if p, ok := a.ownProfile(w, r); ok {
		writeJSON(w, 200, p)
	}
}

func handleGameProfilePut(a *App, w http.ResponseWriter, r *http.Request) {
	cur, ok := a.ownProfile(w, r)
	if !ok {
		return
	}
	p, ok := readProfile(w, r)
	if !ok {
		return
	}
	p.ID = cur.ID
	clean, err := a.cleanGameProfile(r, p)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	saved, err := a.saveGameProfile(r.Context(), r, who(r).User, clean)
	if err != nil {
		a.profileError(w, r, err)
		return
	}
	writeJSON(w, 200, saved)
}

func handleGameProfileDelete(a *App, w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	res, err := a.DB.ExecContext(r.Context(), `DELETE FROM wittyplay_profiles WHERE user_id = ? AND id = ?`, who(r).User, id)
	if err != nil {
		a.profileError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		a.profileError(w, r, errNoProfile)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func copyName(name string) string {
	const suffix = " copy"
	r := []rune(name)
	if max := 60 - len(suffix); len(r) > max {
		r = r[:max]
	}
	return string(r) + suffix
}

func handleGameProfileCopy(a *App, w http.ResponseWriter, r *http.Request) {
	p, ok := a.ownProfile(w, r)
	if !ok {
		return
	}
	p.ID, p.Active, p.Name = 0, false, copyName(p.Name)
	saved, err := a.saveGameProfile(r.Context(), r, who(r).User, p)
	if err != nil {
		a.profileError(w, r, err)
		return
	}
	writeJSON(w, 201, saved)
}

func handleGameProfileActive(a *App, w http.ResponseWriter, r *http.Request) {
	p, ok := a.ownProfile(w, r)
	if !ok {
		return
	}
	var b struct {
		Active bool `json:"active"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	p.Active = b.Active
	saved, err := a.saveGameProfile(r.Context(), r, who(r).User, p)
	if err != nil {
		a.profileError(w, r, err)
		return
	}
	writeJSON(w, 200, saved)
}

// GET /game-profiles/resolve?kind=gamepad&controller=&console=&game=&raw=1: what a client
// should use for this controller here.
func handleGameProfileResolve(a *App, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := q.Get("console")
	if console(c) == nil {
		writeErr(w, 404, "not found")
		return
	}
	kind := q.Get("kind")
	if kind == "" {
		kind = wittyplay.ProfileGamepad
	}
	if kind != wittyplay.ProfileGamepad && kind != wittyplay.ProfileKeyboard {
		writeErr(w, 400, "a profile is for a gamepad or a keyboard")
		return
	}
	game, _ := strconv.ParseInt(q.Get("game"), 10, 64)
	if game != 0 {
		if _, err := a.visibleGame(r.Context(), r, game); err != nil {
			game = 0 // a game this person can't see counts as no game
		}
	}
	list, err := a.gameProfiles(r.Context(), r, who(r).User)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	controller := q.Get("controller")
	if kind == wittyplay.ProfileKeyboard {
		controller = wittyplay.ControllerKeyboard
	}
	p := wittyplay.ResolveProfile(list, kind, controller, c, game, q.Get("raw") == "1")
	out := map[string]any{"profile": p, "options": wittyplay.EffectiveOptions(c, p)}
	if kind == wittyplay.ProfileKeyboard {
		out["keys"] = wittyplay.EffectiveKeys(c, p)
	} else {
		out["map"] = wittyplay.EffectiveMap(c, p)
	}
	writeJSON(w, 200, out)
}

func handleGameProfileExport(a *App, w http.ResponseWriter, r *http.Request) {
	list, err := a.gameProfiles(r.Context(), r, who(r).User)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	want := map[string]bool{}
	for _, id := range r.URL.Query()["id"] {
		want[id] = true
	}
	f := wittyplay.ProfileFile{Format: wittyplay.ProfileFormat, Version: 1, Profiles: []wittyplay.Profile{}}
	for _, p := range list {
		if len(want) > 0 && !want[strconv.FormatInt(p.ID, 10)] {
			continue
		}
		p.ID, p.UpdatedAt, p.Active = 0, 0, false
		f.Profiles = append(f.Profiles, p)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="wittyplay-controller-profiles.json"`)
	writeJSON(w, 200, f)
}

type importRejected struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

// POST /game-profiles/import: a file from export, entirely untrusted. Good profiles are kept and
// each bad one is said why; a profile takes effect only if nothing else with its scope is in use.
func handleGameProfileImport(a *App, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wittyplay.MaxImportSize))
	if err != nil {
		writeErr(w, 413, "this file is too large")
		return
	}
	entries, err := wittyplay.ParseProfileFile(body)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, userID := r.Context(), who(r).User
	existing, err := a.gameProfiles(ctx, r, userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	scope := func(p wittyplay.Profile) string {
		return fmt.Sprint(p.Kind, "\x00", p.Controller, "\x00", p.Console, "\x00", p.Game)
	}
	inUse := map[string]bool{}
	for _, p := range existing {
		if p.Active {
			inUse[scope(p)] = true
		}
	}
	imported, rejected, notes := []wittyplay.Profile{}, []importRejected{}, []string{}
	for i, e := range entries {
		e.ID = 0
		if e.Game != 0 { // another server's numbers: only the same title is the same game
			if it, err := a.visibleGame(ctx, r, e.Game); err != nil || it.Facets["console"] != e.Console || it.Title != e.GameTitle {
				e.Game = 0
				notes = append(notes, fmt.Sprintf("“%s” was for a game that isn't here, so it now applies to the whole console.", wittyplay.ShortText(e.Name)))
			}
		}
		clean, err := a.cleanGameProfile(r, e)
		if err != nil {
			rejected = append(rejected, importRejected{Index: i, Name: wittyplay.ShortText(e.Name), Error: err.Error()})
			continue
		}
		clean.Active = !inUse[scope(clean)]
		saved, err := a.saveGameProfile(ctx, r, userID, clean)
		if err != nil {
			if errors.Is(err, errTooManyProfiles) {
				rejected = append(rejected, importRejected{Index: i, Name: clean.Name, Error: err.Error()})
				continue
			}
			writeErr(w, 500, err.Error())
			return
		}
		if saved.Active {
			inUse[scope(saved)] = true
		}
		imported = append(imported, saved)
	}
	writeJSON(w, 200, map[string]any{"imported": imported, "rejected": rejected, "notes": notes})
}

// activeProfilesFor is what the controls route sends: a person's profiles in use for a console
// and game, whichever controller they're for.
func activeProfilesFor(list []wittyplay.Profile, console string, game int64) []wittyplay.Profile {
	out := []wittyplay.Profile{}
	for _, p := range list {
		if p.Active && (p.Console == "" || p.Console == console) && (p.Game == 0 || p.Game == game) {
			out = append(out, p)
		}
	}
	return out
}

// ---- the table

type profileBody struct {
	ControllerName string                     `json:"controllerName,omitempty"`
	Map            map[int]*wittyplay.Binding `json:"map,omitempty"`
	Keys           map[int]string             `json:"keys,omitempty"`
	Options        wittyplay.ProfileOptions   `json:"options"`
}

const profileCols = `id, name, kind, controller, console, game_id, raw, active, data, updated_at`

func scanProfile(r interface{ Scan(...any) error }) (wittyplay.Profile, error) {
	var p wittyplay.Profile
	var data string
	var raw, active int
	if err := r.Scan(&p.ID, &p.Name, &p.Kind, &p.Controller, &p.Console, &p.Game, &raw, &active, &data, &p.UpdatedAt); err != nil {
		return p, err
	}
	p.Raw, p.Active = raw != 0, active != 0
	var b profileBody
	if err := json.Unmarshal([]byte(data), &b); err != nil {
		return p, err
	}
	p.ControllerName, p.Map, p.Keys, p.Options = b.ControllerName, b.Map, b.Keys, b.Options
	return p, nil
}

// titles fills in the titles of the games profiles are for, from the server: as its own copy
// did, from the item whatever the person may see now (the title is theirs to know: they chose it).
func (a *App) titles(ctx context.Context, list []wittyplay.Profile) {
	seen := map[int64]string{}
	for i := range list {
		g := list[i].Game
		if g == 0 {
			continue
		}
		t, ok := seen[g]
		if !ok {
			if it, err := a.Host.Item(ctx, host.On{}, g); err == nil {
				t = it.Title
			}
			seen[g] = t
		}
		list[i].GameTitle = t
	}
}

func (a *App) gameProfiles(ctx context.Context, _ *http.Request, userID int64) ([]wittyplay.Profile, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT `+profileCols+` FROM wittyplay_profiles WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []wittyplay.Profile{}
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	a.titles(ctx, out)
	return out, nil
}

func (a *App) gameProfile(ctx context.Context, _ *http.Request, userID, id int64) (wittyplay.Profile, error) {
	p, err := scanProfile(a.DB.QueryRowContext(ctx, `SELECT `+profileCols+` FROM wittyplay_profiles WHERE user_id = ? AND id = ?`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, errNoProfile
	}
	if err == nil {
		l := []wittyplay.Profile{p}
		a.titles(ctx, l)
		p = l[0]
	}
	return p, err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// saveGameProfile adds a profile, or replaces one of this person's when p.ID is set. A profile
// made active takes over from any other with the same scope.
func (a *App) saveGameProfile(ctx context.Context, r *http.Request, userID int64, p wittyplay.Profile) (wittyplay.Profile, error) {
	data, err := json.Marshal(profileBody{ControllerName: p.ControllerName, Map: p.Map, Keys: p.Keys, Options: p.Options})
	if err != nil {
		return p, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if p.ID == 0 {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wittyplay_profiles WHERE user_id = ?`, userID).Scan(&n); err != nil {
			return p, err
		}
		if n >= wittyplay.MaxProfiles {
			return p, errTooManyProfiles
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO wittyplay_profiles(user_id, name, kind, controller, console, game_id, raw, active, data, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			userID, p.Name, p.Kind, p.Controller, p.Console, p.Game, boolInt(p.Raw), boolInt(p.Active), string(data), now)
		if err != nil {
			return p, err
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			return p, err
		}
	} else {
		res, err := tx.ExecContext(ctx, `UPDATE wittyplay_profiles SET name = ?, kind = ?, controller = ?, console = ?, game_id = ?, raw = ?, active = ?, data = ?, updated_at = ? WHERE user_id = ? AND id = ?`,
			p.Name, p.Kind, p.Controller, p.Console, p.Game, boolInt(p.Raw), boolInt(p.Active), string(data), now, userID, p.ID)
		if err != nil {
			return p, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return p, errNoProfile
		}
	}
	if p.Active {
		if _, err := tx.ExecContext(ctx, `UPDATE wittyplay_profiles SET active = 0 WHERE user_id = ? AND kind = ? AND controller = ? AND console = ? AND game_id = ? AND id != ?`,
			userID, p.Kind, p.Controller, p.Console, p.Game, p.ID); err != nil {
			return p, err
		}
	}
	if err := tx.Commit(); err != nil {
		return p, err
	}
	return a.gameProfile(ctx, r, userID, p.ID)
}
