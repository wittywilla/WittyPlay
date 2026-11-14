// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"fmt"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WittyPlay Online's rooms and screenshots, moved from the server's wittyplay_online.go: a host
// opening its game to friends, moving who plays which player, and keeping a screenshot of a game
// in its memories. Friends and presence are online.go's; what it needs from here is who is playing
// what (playingNow), and whether someone may join an open room is its mayJoinRoom.

// shotsKept is how many screenshots of one game a profile keeps; the oldest go first.
const shotsKept = 200

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("POST /play/{sid}/room", w(handlePlayRoom))
		mux.Handle("PUT /play/{sid}/seats/{user}", w(handlePlaySeatMove))
		mux.Handle("POST /play/{sid}/screenshot", w(handlePlayScreenshotKeep))
		mux.Handle("GET /games/{id}/shots/{name}", w(handleGameShot))
		mux.Handle("DELETE /games/{id}/shots/{name}", w(handleGameShotDelete))
		// From a running game (the server's api_game_options.go): how it's running, and a picture of it.
		mux.Handle("GET /play/{sid}/stats", w(handlePlayStats))
		mux.Handle("GET /play/{sid}/screenshot", w(handlePlayScreenshot))
	})
	// Who is playing what: the host of every game being played, and everyone who joined one.
	playingNow = func(user int64) (int64, string, bool) {
		for _, t := range seats.tablesNow() {
			if t.Host == user || slicesHas(t.Holders, user) {
				return t.Game.Item, t.Game.Title, true
			}
		}
		return 0, "", false
	}
}

func slicesHas(hs []seatHolder, user int64) bool {
	for _, h := range hs {
		if h.UserID == user {
			return true
		}
	}
	return false
}

// shotName is a kept screenshot's file name: the moment it was taken, and its extension.
var shotName = regexp.MustCompile(`^[0-9]{10,19}\.png$`)

type keptShot struct {
	Name  string
	Taken int64
}

// listShots is a game's kept screenshots, newest first.
func listShots(dir string) []keptShot {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []keptShot
	for _, e := range entries {
		if e.IsDir() || !shotName.MatchString(e.Name()) {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".png"), 10, 64)
		out = append(out, keptShot{Name: e.Name(), Taken: n / int64(time.Second)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out
}

// POST /play/{sid}/room: the host opens its game to the friends who may see it, or closes it.
// It answers the code that lets anyone it's given to join, which a phone's QR code carries.
func handlePlayRoom(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Open *bool `json:"open"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	u := who(r)
	open := b.Open == nil || *b.Open
	if open && (u.Kind == "kids" || u.Kind == "guest") {
		writeErr(w, 403, "This profile’s games can’t be opened to others. Give a code to the people you want instead.")
		return
	}
	if !seats.setOpen(sess.ID(), u.User, open) {
		writeErr(w, 404, "not found")
		return
	}
	out := map[string]any{"sid": sess.ID(), "open": open}
	if v, ok := seats.view(sess.ID(), true); ok && v.Code != "" {
		out["code"], out["expiresAt"] = v.Code, v.ExpiresAt
	} else if code, expires, ok := seats.invite(sess.ID(), u.User); ok {
		out["code"], out["expiresAt"] = code, expires.Unix()
	}
	a.Log.InfoContext(r.Context(), "wittyplay: a game room opened or closed", "cat", "games", "session", sess.ID(), "open", open)
	writeJSON(w, 200, out)
}

// PUT /play/{sid}/seats/{user}: the host moves someone who joined to another player, or to
// watching (seat -1). A seat someone else holds is swapped.
func handlePlaySeatMove(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Seat int `json:"seat"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	sess := session(r)
	user, ok := pathInt(r, "user")
	if sess == nil || !ok {
		writeErr(w, 404, "not found")
		return
	}
	if err := seats.assign(sess.ID(), who(r).User, user, b.Seat); err != nil {
		code := 400
		if err == errSeatNobody || err == errSeatNotHost {
			code = 404
		}
		writeErr(w, code, err.Error())
		return
	}
	v, _ := seats.view(sess.ID(), true)
	writeJSON(w, 200, v)
}

// POST /play/{sid}/screenshot: keeps a screenshot of the host's game in its memories.
func handlePlayScreenshotKeep(a *App, w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	b, err := sess.Screenshot()
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if _, err := png.DecodeConfig(bytes.NewReader(b)); err != nil {
		writeErr(w, 409, "The game gave no picture to keep.")
		return
	}
	item := sess.Info().ItemID
	dir := a.shotsDir(who(r).User, item)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, "The screenshot couldn’t be kept.")
		return
	}
	name := strconv.FormatInt(time.Now().UnixNano(), 10) + ".png"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		writeErr(w, 500, "The screenshot couldn’t be kept.")
		return
	}
	shots := listShots(dir)
	for _, old := range shots[min(len(shots), shotsKept):] {
		_ = os.Remove(filepath.Join(dir, old.Name))
	}
	// The address a client asks for it at, under the module's routes, as the server's was under its own.
	writeJSON(w, 201, map[string]any{"name": name, "itemId": item, "url": fmt.Sprintf("/api/v1/modules/wittyplay/games/%d/shots/%s", item, name)})
}

// shotPath is one of the profile's kept screenshots of a game it may see, or "".
func (a *App) shotPath(r *http.Request) string {
	it, ok := a.personalGame(r)
	name := r.PathValue("name")
	if !ok || !shotName.MatchString(name) {
		return ""
	}
	p := filepath.Join(a.shotsDir(who(r).User, it.ID), name)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func handleGameShot(a *App, w http.ResponseWriter, r *http.Request) {
	p := a.shotPath(r)
	if p == "" {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, p)
}

func handleGameShotDelete(a *App, w http.ResponseWriter, r *http.Request) {
	p := a.shotPath(r)
	if p == "" {
		writeErr(w, 404, "not found")
		return
	}
	if err := os.Remove(p); err != nil {
		writeErr(w, 500, "The screenshot couldn’t be deleted.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handlePlayStats(a *App, w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, sess.Stats())
}

func handlePlayScreenshot(a *App, w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	b, err := sess.Screenshot()
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.png"`, downloadName(sess.Info().Title, "screenshot")))
	_, _ = w.Write(b)
}
