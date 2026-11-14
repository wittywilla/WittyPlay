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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/wittywilla/wittyplay"
)

// A person's controller layouts, per console: GET and PUT /consoles/{console}/controls (the
// server's /api/consoles/{console}/controls). Moved from the server (api_games.go, game_layouts.go,
// library/games.go) as they were.

func controlRoutes(mux *http.ServeMux, with func(func(*App, http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("GET /consoles/{console}/controls", with(handleGameControls))
	mux.Handle("PUT /consoles/{console}/controls", with(handlePutGameControl))
}

// A client's name for itself, chosen by the client and kept on the device. It only labels saved
// layouts, so it proves nothing and needs to be nothing more than tidy.
var gameDeviceRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func touchKey(device, orientation string) string { return device + "/" + orientation }

// handleGameControls gives a client everything it needs to draw and map a console's controller:
// what the console has, the defaults, and what this person saved. Asking with ?device= includes
// the touch layouts saved for that device.
func handleGameControls(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("console")
	if console(id) == nil {
		writeErr(w, 404, "not found")
		return
	}
	ctx, userID := r.Context(), who(r).User
	saved, err := a.gameControls(ctx, userID, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	profiles, err := a.gameProfiles(ctx, r, userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	background, err := a.getGameBackground(ctx, userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var game int64
	if g, err := strconv.ParseInt(r.URL.Query().Get("game"), 10, 64); err == nil && g > 0 {
		if _, err := a.visibleGame(ctx, r, g); err == nil {
			game = g
		}
	}
	gamepads := saved[wittyplay.KindGamepad]
	if gamepads == nil {
		gamepads = map[string]json.RawMessage{}
	}
	var keyboard json.RawMessage
	if k, ok := saved[wittyplay.KindKeyboard][""]; ok {
		keyboard = k
	}
	layouts := map[string]json.RawMessage{}
	for shape, l := range saved[kindLayout] {
		if layoutShapes[shape] {
			layouts[shape] = l
		}
	}
	touch := map[string]json.RawMessage{}
	if device := r.URL.Query().Get("device"); gameDeviceRe.MatchString(device) {
		for _, o := range []string{wittyplay.Landscape, wittyplay.Portrait} {
			if l, ok := saved[wittyplay.KindTouch][touchKey(device, o)]; ok {
				touch[o] = l
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"console":  id,
		"pad":      wittyplay.Pad(id),
		"controls": wittyplay.Controls(id),
		"targets":  wittyplay.Targets(id),
		"defaults": map[string]any{
			"gamepad": wittyplay.DefaultGamepadFor(id), "keyboard": wittyplay.DefaultKeyboardFor(id), "remote": wittyplay.DefaultRemote(), "options": wittyplay.DefaultOptions(id),
			"touch": map[string]any{wittyplay.Landscape: wittyplay.DefaultTouch(id, wittyplay.Landscape), wittyplay.Portrait: wittyplay.DefaultTouch(id, wittyplay.Portrait)},
		},
		"saved":      map[string]any{"gamepad": gamepads, "keyboard": keyboard, "touch": touch, "layouts": layouts},
		"game":       game,
		"profiles":   activeProfilesFor(profiles, id, game),
		"background": background,
	})
}

// handlePutGameControl saves one layout for the person asking, or removes it when data is null.
// Everything is checked: a layout comes from a client.
func handlePutGameControl(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("console")
	if console(id) == nil {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Kind        string          `json:"kind"`
		Key         string          `json:"key"` // gamepad: its model; touch: this device
		Orientation string          `json:"orientation"`
		Data        json.RawMessage `json:"data"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, wittyplay.MaxControlSize+1024)
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, "this layout can't be read")
		return
	}
	key := ""
	switch b.Kind {
	case wittyplay.KindGamepad:
		key = b.Key
		if key == "" || len(key) > 120 {
			writeErr(w, 400, "a gamepad's model name is needed")
			return
		}
	case wittyplay.KindKeyboard:
	case wittyplay.KindTouch:
		if !gameDeviceRe.MatchString(b.Key) || (b.Orientation != wittyplay.Landscape && b.Orientation != wittyplay.Portrait) {
			writeErr(w, 400, "a touch layout needs a device and an orientation")
			return
		}
		key = touchKey(b.Key, b.Orientation)
	case kindLayout:
		if !layoutShapes[b.Key] {
			writeErr(w, 400, "a layout is for a shape of device: phone-portrait, phone-landscape, tablet-portrait, tablet-landscape, desktop or tv")
			return
		}
		key = b.Key
	default:
		writeErr(w, 400, "unknown kind of layout")
		return
	}
	var clean json.RawMessage
	if len(b.Data) > 0 && string(b.Data) != "null" {
		var err error
		if b.Kind == kindLayout {
			clean, err = cleanLayout(controlKeys(id), b.Data)
		} else {
			clean, err = wittyplay.CleanControl(id, b.Kind, b.Data)
		}
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if err := a.setGameControl(r.Context(), who(r).User, id, b.Kind, key, clean); err != nil {
		if errors.Is(err, errTooManyControls) {
			writeErr(w, 409, err.Error())
			return
		}
		a.Log.WarnContext(r.Context(), "could not save a control layout", "cat", "games", "kind", b.Kind, "console", id, "err", err)
		writeErr(w, 500, err.Error())
		return
	}
	a.Log.DebugContext(r.Context(), "control layout saved", "cat", "games", "kind", b.Kind, "console", id, "removed", clean == nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (a *App) gameControls(ctx context.Context, userID int64, console string) (map[string]map[string]json.RawMessage, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT kind, key, data FROM wittyplay_controls WHERE user_id = ? AND console = ?`, userID, console)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]json.RawMessage{}
	for rows.Next() {
		var kind, key, data string
		if err := rows.Scan(&kind, &key, &data); err != nil {
			return nil, err
		}
		if out[kind] == nil {
			out[kind] = map[string]json.RawMessage{}
		}
		out[kind][key] = json.RawMessage(data)
	}
	return out, rows.Err()
}

// maxGameControls bounds what one account can store per console.
const maxGameControls = 64

var errTooManyControls = errors.New("too many saved layouts for this console; reset one first")

func (a *App) setGameControl(ctx context.Context, userID int64, console, kind, key string, data json.RawMessage) error {
	if data == nil {
		_, err := a.DB.ExecContext(ctx, `DELETE FROM wittyplay_controls WHERE user_id = ? AND console = ? AND kind = ? AND key = ?`, userID, console, kind, key)
		return err
	}
	var n int
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wittyplay_controls WHERE user_id = ? AND console = ? AND NOT (kind = ? AND key = ?)`, userID, console, kind, key).Scan(&n); err != nil {
		return err
	}
	if n >= maxGameControls {
		return errTooManyControls
	}
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_controls(user_id, console, kind, key, data, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id, console, kind, key) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		userID, console, kind, key, string(data), time.Now().Unix())
	return err
}

// ---- Edit layout: where everything sits while playing, per console and shape of device.

const kindLayout = "layout"

var layoutShapes = map[string]bool{
	"phone-portrait": true, "phone-landscape": true,
	"tablet-portrait": true, "tablet-landscape": true,
	"desktop": true, "tv": true,
}

type layoutSpot struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type layoutScreen struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Size float64 `json:"size"`
}

type savedLayout struct {
	Controls        map[string]wittyplay.Placement `json:"controls,omitempty"`
	Menu            *layoutSpot                    `json:"menu,omitempty"`
	Touchbar        *layoutSpot                    `json:"touchbar,omitempty"`
	Screens         []layoutScreen                 `json:"screens,omitempty"`
	FaceTogether    *bool                          `json:"faceTogether,omitempty"`
	ScreensTogether *bool                          `json:"screensTogether,omitempty"`
}

// cleanLayout checks a layout someone wants to keep, given the console's controls by key, and
// returns it in the one form stored. It comes from a client, so nothing in it is taken on trust.
func cleanLayout(controls map[string]bool, data json.RawMessage) (json.RawMessage, error) {
	if len(data) > wittyplay.MaxControlSize {
		return nil, errors.New("this layout is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var l savedLayout
	if err := dec.Decode(&l); err != nil {
		return nil, errors.New("this layout can't be read")
	}
	in := func(v, lo, hi float64) bool { return v >= lo && v <= hi }
	for k, p := range l.Controls {
		if !controls[k] {
			return nil, fmt.Errorf("this console has no control %q", k)
		}
		if !in(p.X, 0, 1) || !in(p.Y, 0, 1) || !in(p.Size, 0.05, 0.6) || !in(p.Opacity, 0.1, 1) {
			return nil, fmt.Errorf("%s is out of range", k)
		}
	}
	for name, s := range map[string]*layoutSpot{"the menu button": l.Menu, "the shortcuts bar": l.Touchbar} {
		if s != nil && (!in(s.X, 0, 1) || !in(s.Y, 0, 1)) {
			return nil, fmt.Errorf("%s is off the screen", name)
		}
	}
	if len(l.Screens) > 2 {
		return nil, errors.New("a layout places one picture, or two screens")
	}
	for _, s := range l.Screens {
		if !in(s.X, 0, 1) || !in(s.Y, 0, 1) || !in(s.Size, 0.1, 1) {
			return nil, errors.New("a screen is out of range")
		}
	}
	return json.Marshal(l)
}

func controlKeys(console string) map[string]bool {
	known := map[string]bool{}
	for _, c := range wittyplay.Controls(console) {
		known[c.Key] = true
	}
	return known
}

var _ = slog.Default
