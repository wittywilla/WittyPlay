// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
)

// WittyPlay Online's people (docs/GAMING-API.md, Playing together): friends, what each lets the
// others know, and the friends an administrator lets a Kids profile have. Moved from the server
// (wittyplay_online.go, library/game_online.go); the choices are in wittyplay_presence and
// wittyplay_kids_friends, the accounts and when each was last seen come from the server
// (a.Host.Users). Rooms and screenshots of a running game are the starting-and-playing group's.
//
// Friends are the other profiles of this server. A profile chooses whether they see what it
// plays, and whether it appears online at all. A Kids profile sees, and is seen by, only the
// profiles an administrator chose for it. A guest has no friends.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/online/me", w(handlePlayOnlineMe))
		mux.Handle("PUT /games/online/me", w(handlePlayOnlineMePut))
		mux.Handle("GET /games/online/friends", w(handlePlayFriends))
		mux.Handle("GET /games/online/kids/{user}", w(handlePlayKidsFriends))
		mux.Handle("PUT /games/online/kids/{user}", w(handlePlayKidsFriendsPut))
	})
}

// playingNow says what a profile is playing on this server right now, as the game's item and
// title (the starting-and-playing group sets it). Until it's set nobody is playing.
var playingNow func(user int64) (item int64, title string, ok bool)

// How recently a profile was seen, from its sign-ins.
const (
	presenceOnline = 5 * time.Minute
	presenceAway   = time.Hour
)

// Presence is what a profile lets its friends know: what it plays (Playing), and whether it's here
// at all (Online). Someone who isn't online is offline to everyone, playing or not; someone who
// doesn't share what they play is shown online, with no game.
type Presence struct {
	Playing bool `json:"sharePlaying"`
	Online  bool `json:"appearOnline"`
}

// presenceOf is a profile's choices. With none of its own the answer is yes to both, except for a
// guest.
func (a *App) presenceOf(ctx context.Context, userID int64, kind string) Presence {
	var share, online int
	err := a.DB.QueryRowContext(ctx, `SELECT share, online FROM wittyplay_presence WHERE user_id = ?`, userID).Scan(&share, &online)
	if err != nil {
		yes := kind != "guest"
		return Presence{Playing: yes, Online: yes}
	}
	return Presence{Playing: share != 0, Online: online != 0}
}

func (a *App) setPresence(ctx context.Context, userID int64, p Presence) error {
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_presence(user_id, share, online) VALUES(?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET share = excluded.share, online = excluded.online`, userID, boolInt(p.Playing), boolInt(p.Online))
	return err
}

// kidsFriends is the profiles an administrator lets a Kids profile see and be seen by.
func (a *App) kidsFriends(ctx context.Context, kidID int64) ([]int64, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT friend_id FROM wittyplay_kids_friends WHERE kid_id = ? ORDER BY friend_id`, kidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// setKidsFriends replaces a Kids profile's friends. A profile is never its own friend.
func (a *App) setKidsFriends(ctx context.Context, kidID int64, friends []int64) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM wittyplay_kids_friends WHERE kid_id = ?`, kidID); err != nil {
		return err
	}
	seen := []int64{}
	for _, f := range friends {
		if f == kidID || slices.Contains(seen, f) {
			continue
		}
		seen = append(seen, f)
		if _, err := tx.ExecContext(ctx, `INSERT INTO wittyplay_kids_friends(kid_id, friend_id) VALUES(?, ?)`, kidID, f); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// profileName is the name a profile goes by.
func profileName(u *host.User) string {
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Name
}

// playFriends is the profiles this one may see as friends, by name. A guest has none; a Kids
// profile has the ones an administrator chose; anyone else every profile but guests and the Kids
// profiles that weren't given them.
func (a *App) playFriends(ctx context.Context, viewer service.Identity) ([]host.User, error) {
	if viewer.User <= 0 || viewer.Kind == "guest" {
		return nil, nil
	}
	all, err := a.Host.Users(ctx)
	if err != nil {
		return nil, err
	}
	var mine []int64
	if viewer.Kind == "kids" {
		if mine, err = a.kidsFriends(ctx, viewer.User); err != nil {
			return nil, err
		}
	}
	out := []host.User{}
	for _, u := range all {
		if u.ID == viewer.User || u.Kind == "guest" {
			continue
		}
		if viewer.Kind == "kids" && !slices.Contains(mine, u.ID) {
			continue
		}
		if u.Kind == "kids" {
			theirs, err := a.kidsFriends(ctx, u.ID)
			if err != nil || !slices.Contains(theirs, viewer.User) {
				continue
			}
		}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(profileName(&out[i])) < strings.ToLower(profileName(&out[j]))
	})
	return out, nil
}

// mayJoinRoom says whether a profile may join an open room (for the group that has rooms): not a
// Kids profile or a guest, and only one whose host it may see as a friend.
func (a *App) mayJoinRoom(ctx context.Context, viewer service.Identity, hostUser int64) bool {
	if viewer.Kind == "kids" || viewer.Kind == "guest" {
		return false
	}
	friends, err := a.playFriends(ctx, viewer)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(friends, func(u host.User) bool { return u.ID == hostUser })
}

// presence is what a friend is doing.
type presence struct {
	State string `json:"state"`          // playing, online, away, offline
	Game  string `json:"game,omitempty"` // with playing: the game's title
	Item  int64  `json:"item,omitempty"`
}

// playPresence is what each of these profiles is doing, as far as each lets others know.
func (a *App) playPresence(ctx context.Context, users []host.User) map[int64]presence {
	out := map[int64]presence{}
	now := time.Now()
	for _, u := range users {
		choice := a.presenceOf(ctx, u.ID, u.Kind)
		if !choice.Online {
			out[u.ID] = presence{State: "offline"}
			continue
		}
		if playingNow != nil {
			if item, title, ok := playingNow(u.ID); ok && title != "" {
				if choice.Playing {
					out[u.ID] = presence{State: "playing", Game: title, Item: item}
				} else {
					out[u.ID] = presence{State: "online"} // here, and not saying what it plays
				}
				continue
			}
		}
		ago := now.Sub(time.Unix(u.LastSeen, 0))
		switch {
		case u.LastSeen > 0 && ago <= presenceOnline:
			out[u.ID] = presence{State: "online"}
		case u.LastSeen > 0 && ago <= presenceAway:
			out[u.ID] = presence{State: "away"}
		default:
			out[u.ID] = presence{State: "offline"}
		}
	}
	return out
}

// GET /games/online/me: what this profile lets its friends know: what it plays (sharePlaying;
// off, it's shown online with no game) and whether it appears online at all (appearOnline; off,
// it's offline to everyone).
func handlePlayOnlineMe(a *App, w http.ResponseWriter, r *http.Request) {
	u := who(r)
	p := a.presenceOf(r.Context(), u.User, u.Kind)
	writeJSON(w, 200, map[string]any{"sharePlaying": p.Playing, "appearOnline": p.Online, "canShare": u.Kind != "guest"})
}

// PUT /games/online/me: either choice, or both.
func handlePlayOnlineMePut(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		SharePlaying *bool `json:"sharePlaying"`
		AppearOnline *bool `json:"appearOnline"`
	}
	if err := readJSON(capBody(w, r, 4<<10), &b); err != nil || (b.SharePlaying == nil && b.AppearOnline == nil) {
		writeErr(w, 400, "Say sharePlaying or appearOnline: true or false.")
		return
	}
	u := who(r)
	if u.Kind == "guest" {
		writeErr(w, 403, "A guest has no friends to share with.")
		return
	}
	p := a.presenceOf(r.Context(), u.User, u.Kind)
	if b.SharePlaying != nil {
		p.Playing = *b.SharePlaying
	}
	if b.AppearOnline != nil {
		p.Online = *b.AppearOnline
	}
	if err := a.setPresence(r.Context(), u.User, p); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	handlePlayOnlineMe(a, w, r)
}

// friendView is a friend as the friends list shows it.
type friendView struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	State string `json:"state"`          // playing, online, away, offline
	Game  string `json:"game,omitempty"` // only a game the asking profile may see itself
	Item  int64  `json:"item,omitempty"`
}

// GET /games/online/friends: the profiles this one may see, each with what it's doing as far as
// it shares, playing first. What the server's wittyplay.friends source showed.
func handlePlayFriends(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	friends, err := a.playFriends(ctx, who(r))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pres := a.playPresence(ctx, friends)
	rank := map[string]int{"playing": 0, "online": 1, "away": 2, "offline": 3}
	sort.SliceStable(friends, func(i, j int) bool { return rank[pres[friends[i].ID].State] < rank[pres[friends[j].ID].State] })
	out := make([]friendView, 0, len(friends))
	for _, f := range friends {
		p := pres[f.ID]
		v := friendView{ID: f.ID, Name: profileName(&f), Kind: f.Kind, State: p.State}
		// A game this profile couldn't open itself is "playing", with no title.
		if p.State == "playing" {
			if _, err := a.visibleGame(ctx, r, p.Item); err == nil {
				v.Game, v.Item = p.Game, p.Item
			}
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

// kidsProfile is the Kids profile a route names, for an administrator (the server's requireAdmin:
// the route is "admin" in integration.json, and checked again here).
func (a *App) kidsProfile(w http.ResponseWriter, r *http.Request) (*host.User, []host.User, bool) {
	if !who(r).Admin {
		writeErr(w, 403, "only an administrator can do that")
		return nil, nil, false
	}
	id, ok := pathInt(r, "user")
	if !ok {
		writeErr(w, 404, "not found")
		return nil, nil, false
	}
	all, err := a.Host.Users(r.Context())
	if err != nil {
		writeErr(w, 502, "the server's profiles can't be read right now")
		return nil, nil, false
	}
	i := slices.IndexFunc(all, func(u host.User) bool { return u.ID == id })
	if i < 0 {
		writeErr(w, 404, "not found")
		return nil, nil, false
	}
	if all[i].Kind != "kids" {
		writeErr(w, 400, "Only a Kids profile has friends chosen for it.")
		return nil, nil, false
	}
	return &all[i], all, true
}

// kidsCandidate is a profile that could be a Kids profile's friend, and whether it is.
type kidsCandidate struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Approved bool   `json:"approved"`
}

func (a *App) writeKidsFriends(w http.ResponseWriter, r *http.Request, u *host.User, all []host.User) {
	ids, err := a.kidsFriends(r.Context(), u.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	candidates := []kidsCandidate{}
	for _, o := range all {
		if o.ID == u.ID || o.Kind == "guest" {
			continue
		}
		candidates = append(candidates, kidsCandidate{ID: o.ID, Name: profileName(&o), Kind: o.Kind, Approved: slices.Contains(ids, o.ID)})
	}
	writeJSON(w, 200, map[string]any{"user": u.ID, "name": profileName(u), "friends": ids, "candidates": candidates})
}

// GET /games/online/kids/{user}: the friends a Kids profile may see and be seen by, and every
// profile that could be one (any but itself and guests), for the page that chooses them.
func handlePlayKidsFriends(a *App, w http.ResponseWriter, r *http.Request) {
	if u, all, ok := a.kidsProfile(w, r); ok {
		a.writeKidsFriends(w, r, u, all)
	}
}

func handlePlayKidsFriendsPut(a *App, w http.ResponseWriter, r *http.Request) {
	u, all, ok := a.kidsProfile(w, r)
	if !ok {
		return
	}
	var b struct {
		Friends []int64 `json:"friends"`
	}
	if err := readJSON(capBody(w, r, 64<<10), &b); err != nil {
		writeErr(w, 400, "this can't be read")
		return
	}
	if len(b.Friends) > 200 {
		writeErr(w, 400, "That’s more friends than this server has profiles.")
		return
	}
	for _, id := range b.Friends {
		if !slices.ContainsFunc(all, func(x host.User) bool { return x.ID == id && x.Kind != "guest" }) {
			writeErr(w, 400, fmt.Sprintf("Profile %d isn’t one that can be a friend.", id))
			return
		}
	}
	if err := a.setKidsFriends(r.Context(), u.ID, b.Friends); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The server keeps the audit log (it did this with logging.Audit itself).
	if err := a.Host.Audit(r.Context(), host.For(r), "a Kids profile's game friends changed", "",
		fmt.Sprintf("profile %s, %d friends", u.Name, len(b.Friends))); err != nil {
		a.Log.Warn("the audit log couldn't be written", "cat", "profiles", "err", err)
	}
	a.writeKidsFriends(w, r, u, all)
}
