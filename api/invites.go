// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/rpc"
)

// WittyPlay Online's invites (docs/GAMING-API.md, Playing together): someone playing a game asks one
// friend by name to join it. The friend accepts, which seats them as a code would, or declines; the
// one who sent it may take it back. An invite lasts ten minutes, or until the game ends, and is kept
// in memory only, as the seats are.
//
// Who may invite whom is the friends list's rule (online.go): only a profile this one may see as a
// friend, so a Kids profile invites and is invited by its approved friends alone, and a guest by
// nobody. The friend must also be able to open the game themselves, which is checked again when they
// accept.

const (
	inviteLife    = 10 * time.Minute
	invitesPerOne = 16 // invites one profile may have out at once; the oldest goes first
)

// gameInvite is one invite to a game being played.
type gameInvite struct {
	ID       string
	From     int64
	FromName string
	To       int64
	Session  string
	Item     int64
	Title    string
	Console  string
	Sent     time.Time
	Expires  time.Time
}

// inviteBook keeps the invites out now.
type inviteBook struct {
	mu   sync.Mutex
	byID map[string]*gameInvite
	now  func() time.Time
}

func (b *inviteBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// dropOldLocked forgets invites that ran out.
func (b *inviteBook) dropOldLocked() {
	now := b.clock()
	for id, in := range b.byID {
		if !now.Before(in.Expires) {
			delete(b.byID, id)
		}
	}
}

// send keeps an invite, in place of one the same person already had out to the same friend for the
// same game, and answers it as kept. Someone with too many out loses their oldest.
func (b *inviteBook) send(in gameInvite) gameInvite {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byID == nil {
		b.byID = map[string]*gameInvite{}
	}
	b.dropOldLocked()
	var mine []*gameInvite
	for id, old := range b.byID {
		if old.From != in.From {
			continue
		}
		if old.To == in.To && old.Session == in.Session {
			delete(b.byID, id)
			continue
		}
		mine = append(mine, old)
	}
	slices.SortFunc(mine, func(x, y *gameInvite) int { return x.Sent.Compare(y.Sent) })
	for len(mine) >= invitesPerOne {
		delete(b.byID, mine[0].ID)
		mine = mine[1:]
	}
	in.ID = newInviteID()
	in.Sent = b.clock()
	in.Expires = in.Sent.Add(inviteLife)
	b.byID[in.ID] = &in
	return in
}

// to is the invites a profile has been sent and may still answer, newest first.
func (b *inviteBook) to(user int64) []gameInvite {
	return b.list(func(in *gameInvite) bool { return in.To == user })
}

// from is the invites a profile has out, newest first.
func (b *inviteBook) from(user int64) []gameInvite {
	return b.list(func(in *gameInvite) bool { return in.From == user })
}

func (b *inviteBook) list(keep func(*gameInvite) bool) []gameInvite {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropOldLocked()
	out := []gameInvite{}
	for _, in := range b.byID {
		if keep(in) {
			out = append(out, *in)
		}
	}
	slices.SortFunc(out, func(x, y gameInvite) int {
		if c := y.Sent.Compare(x.Sent); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out
}

// take hands an invite to the friend it was sent to, and forgets it: accepting uses it up.
func (b *inviteBook) take(id string, user int64) (gameInvite, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dropOldLocked()
	in := b.byID[id]
	if in == nil || in.To != user {
		return gameInvite{}, false
	}
	delete(b.byID, id)
	return *in, true
}

// drop forgets an invite: declined by the friend, or taken back by the one who sent it.
func (b *inviteBook) drop(id string, user int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	in := b.byID[id]
	if in == nil || (in.To != user && in.From != user) {
		return false
	}
	delete(b.byID, id)
	return true
}

// ended forgets every invite to a game that ended.
func (b *inviteBook) ended(sid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, in := range b.byID {
		if in.Session == sid {
			delete(b.byID, id)
		}
	}
}

func newInviteID() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand doesn't fail on any system this runs on
	}
	return hex.EncodeToString(b[:])
}

// invites is every invite out on this server now.
var invites inviteBook

// inviteView is an invite as the API answers it.
type inviteView struct {
	ID        string `json:"id"`
	From      int64  `json:"from"`
	FromName  string `json:"fromName"`
	To        int64  `json:"to"`
	Session   string `json:"sid"`
	Item      int64  `json:"itemId"`
	Title     string `json:"title"`
	Console   string `json:"console,omitempty"`
	SentAt    int64  `json:"sentAt"`
	ExpiresAt int64  `json:"expiresAt"`
}

func viewInvite(in gameInvite) inviteView {
	return inviteView{ID: in.ID, From: in.From, FromName: in.FromName, To: in.To, Session: in.Session, Item: in.Item, Title: in.Title,
		Console: in.Console, SentAt: in.Sent.Unix(), ExpiresAt: in.Expires.Unix()}
}

// liveInvite says whether an invite's game is still being played.
func liveInvite(in gameInvite) bool {
	for _, t := range seats.tablesNow() {
		if t.Session == in.Session {
			return true
		}
	}
	return false
}

// hostedTable is the game a profile is playing now as its host.
func hostedTable(user int64) (seatTableView, bool) {
	for _, t := range seats.tablesNow() {
		if t.Host == user {
			return t, true
		}
	}
	return seatTableView{}, false
}

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/online/invites", w(handleInvites))
		mux.Handle("POST /games/online/friends/{user}/invite", w(handleInviteSend))
		mux.Handle("POST /games/online/invites/{id}/accept", w(handleInviteAccept))
		mux.Handle("DELETE /games/online/invites/{id}", w(handleInviteDrop))
	})
}

// GET /games/online/invites: the invites this profile was sent (received) and has out (sent),
// newest first, of games still being played.
func handleInvites(a *App, w http.ResponseWriter, r *http.Request) {
	u := who(r)
	out := map[string][]inviteView{"received": {}, "sent": {}}
	for _, in := range invites.to(u.User) {
		if liveInvite(in) {
			out["received"] = append(out["received"], viewInvite(in))
		}
	}
	for _, in := range invites.from(u.User) {
		if liveInvite(in) {
			out["sent"] = append(out["sent"], viewInvite(in))
		}
	}
	writeJSON(w, 200, out)
}

// POST /games/online/friends/{user}/invite: invites a friend to the game this profile is playing as
// its host. Sending it again sends a new one in its place.
func handleInviteSend(a *App, w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), who(r)
	to, ok := pathInt(r, "user")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	t, ok := hostedTable(u.User)
	if !ok {
		writeErr(w, 409, "Start a game first, then invite a friend to it.")
		return
	}
	friends, err := a.playFriends(ctx, u)
	if err != nil {
		writeErr(w, 502, "the server's profiles can't be read right now")
		return
	}
	at := slices.IndexFunc(friends, func(f host.User) bool { return f.ID == to })
	if at < 0 {
		writeErr(w, 404, "That profile isn’t one of your friends.")
		return
	}
	if slicesHas(t.Holders, to) {
		writeErr(w, 409, profileName(&friends[at])+" is already in this game.")
		return
	}
	// A friend who couldn't open the game themselves can't be asked to join it.
	if it, err := a.Host.Item(ctx, host.On{User: to}, t.Game.Item); err != nil || it.Kind != "game" {
		writeErr(w, 403, profileName(&friends[at])+" can’t play this game.")
		return
	}
	in := invites.send(gameInvite{From: u.User, FromName: t.HostName, To: to, Session: t.Session, Item: t.Game.Item, Title: t.Game.Title, Console: t.Game.Console})
	if in.FromName == "" {
		in.FromName = a.personName(ctx, u.User)
	}
	a.Log.InfoContext(ctx, "wittyplay: a friend was invited to a game", "cat", "games", "session", t.Session, "to", to)
	writeJSON(w, 201, viewInvite(in))
}

// POST /games/online/invites/{id}/accept: takes a seat in the game an invite is for, as a code
// would: the lowest player free, or watching when every one is taken. It answers what joining
// answers, and where the game is played.
func handleInviteAccept(a *App, w http.ResponseWriter, r *http.Request) {
	ctx, u := r.Context(), who(r)
	in, ok := invites.take(r.PathValue("id"), u.User)
	if !ok || !liveInvite(in) {
		writeErr(w, 404, "That invite has run out, or the game has ended.")
		return
	}
	sess := rpc.Mgr.Get(in.Session)
	if sess == nil {
		writeErr(w, 404, "That invite has run out, or the game has ended.")
		return
	}
	if _, err := a.visibleGame(ctx, r, in.Item); err != nil {
		writeErr(w, 403, "This game isn’t one this profile can play.")
		return
	}
	j, err := seats.joinInvited(in.Session, u.User, a.personName(ctx, u.User))
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	info := sess.Info()
	writeJSON(w, 200, map[string]any{"sid": j.Session, "seat": j.Seat, "itemId": info.ItemID, "title": info.Title, "console": info.Console,
		"host": in.FromName, "yours": j.Host, "route": joinRoute(info.ItemID, j.Session)})
}

// DELETE /games/online/invites/{id}: the friend declines an invite, or the one who sent it takes it back.
func handleInviteDrop(a *App, w http.ResponseWriter, r *http.Request) {
	if !invites.drop(r.PathValue("id"), who(r).User) {
		writeErr(w, 404, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// joinRoute is where someone who joined a game plays it: the game's page with the session it joined,
// which every app's player opens as a seat in that game rather than a game of its own.
func joinRoute(item int64, sid string) string {
	return "/game/" + strconv.FormatInt(item, 10) + "?join=" + sid
}

// inviteRoute is where accepting an invite leads: the game's page with the invite, which every app's
// player accepts (POST /games/online/invites/{id}/accept) and then plays as joinRoute would.
func inviteRoute(item int64, id string) string {
	return "/game/" + strconv.FormatInt(item, 10) + "?invite=" + id
}
