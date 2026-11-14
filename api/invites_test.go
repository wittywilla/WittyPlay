// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/service"
)

// The book: one invite per friend and game, sent again in place; a cap on how many one profile has
// out; ten minutes each; taken only by the friend it was for; dropped by either side; gone with its game.
func TestInviteBook(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b := inviteBook{now: func() time.Time { return now }}
	first := b.send(gameInvite{From: 3, To: 2, Session: "s1", Item: 7, Title: "Star Tide"})
	if first.ID == "" || !first.Expires.Equal(now.Add(inviteLife)) {
		t.Fatalf("sent: %+v", first)
	}
	now = now.Add(time.Second)
	again := b.send(gameInvite{From: 3, To: 2, Session: "s1", Item: 7, Title: "Star Tide"})
	if got := b.to(2); len(got) != 1 || got[0].ID != again.ID || again.ID == first.ID {
		t.Fatalf("sent again, in place: %+v", got)
	}
	if _, ok := b.take(again.ID, 4); ok {
		t.Error("someone else took the invite")
	}
	if b.drop(again.ID, 4) {
		t.Error("someone else dropped the invite")
	}

	// Too many out: the oldest goes.
	for i := range invitesPerOne + 2 {
		now = now.Add(time.Second)
		b.send(gameInvite{From: 5, To: int64(100 + i), Session: "s2"})
	}
	out := b.from(5)
	if len(out) != invitesPerOne || out[0].To != 100+invitesPerOne+1 || slices.ContainsFunc(out, func(in gameInvite) bool { return in.To == 100 || in.To == 101 }) {
		t.Fatalf("a profile's invites out: %d, newest to %d", len(out), out[0].To)
	}

	// The game ends: its invites go. Ten minutes pass: the rest go.
	b.ended("s2")
	if len(b.from(5)) != 0 {
		t.Error("invites to a game that ended")
	}
	if in, ok := b.take(again.ID, 2); !ok || in.Title != "Star Tide" {
		t.Fatalf("the friend took it: %+v %v", in, ok)
	}
	if _, ok := b.take(again.ID, 2); ok {
		t.Error("an invite was taken twice")
	}
	left := b.send(gameInvite{From: 3, To: 2, Session: "s1"})
	if !b.drop(left.ID, 3) {
		t.Error("the one who sent it couldn't take it back")
	}
	b.send(gameInvite{From: 3, To: 2, Session: "s1"})
	now = now.Add(inviteLife)
	if len(b.to(2)) != 0 {
		t.Error("an invite outlived its ten minutes")
	}
}

func callAs(t *testing.T, h http.Handler, method, path string, who service.Identity) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader("{}"))
	r.Header.Set(service.HeaderUser, strconv.FormatInt(who.User, 10))
	if who.Kind != "" {
		r.Header.Set(service.HeaderKind, who.Kind)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Inviting, over the API and on the Together page: only to the game the profile hosts, only a friend,
// only one who may open the game; the friends list offers Invite, then Invited and Cancel; the friend
// sees it with Accept (into the game) and Decline; declining takes it away.
func TestInvitesToAFriend(t *testing.T) {
	a, h := testApp(t, onlineHost(t))
	t.Cleanup(func() { invites = inviteBook{} })
	if err := a.setKidsFriends(t.Context(), kit.User, []int64{sam.User}); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, h, "POST", "/games/online/friends/2/invite", sam); w.Code != 409 {
		t.Fatalf("an invite with no game: %d %s", w.Code, w.Body)
	}
	// Nobody hosts: the friends list has no Invite.
	for _, f := range onlineItems(t, sam, srcPlayFriends) {
		if len(f.Actions) != 0 {
			t.Errorf("a button with no game: %+v", f.Actions)
		}
	}

	startTable(t, "s1", sam.User, "sam", "Living room", seatGame{Item: 7, Title: "Star Tide", Console: "snes", Players: 2})
	t.Cleanup(func() { invites.ended("s1") })
	if w := callAs(t, h, "POST", "/games/online/friends/6/invite", sam); w.Code != 404 {
		t.Errorf("a guest is nobody's friend: %d", w.Code)
	}
	if w := callAs(t, h, "POST", "/games/online/friends/5/invite", sam); w.Code != 404 {
		t.Errorf("a Kids profile that didn't approve sam: %d", w.Code)
	}

	friends := onlineItems(t, sam, srcPlayFriends)
	at := slices.IndexFunc(friends, func(f sdui.Item) bool { return f.Title == "alex" })
	if at < 0 || len(friends[at].Actions) != 1 || friends[at].Actions[0].ID != "invite" || friends[at].Actions[0].Glyph != "x" ||
		friends[at].Actions[0].Action.Op != "wittyplay.friend.invite" || friends[at].Actions[0].Action.Item != "2" || friends[at].Actions[0].Label != "Invite to Star Tide" {
		t.Fatalf("sam's friend alex: %+v", friends)
	}

	w := callAs(t, h, "POST", "/games/online/friends/2/invite", sam)
	var sent inviteView
	_ = json.Unmarshal(w.Body.Bytes(), &sent)
	if w.Code != 201 || sent.To != 2 || sent.Session != "s1" || sent.Title != "Star Tide" || sent.FromName != "sam" {
		t.Fatalf("invited alex: %d %s", w.Code, w.Body)
	}
	friends = onlineItems(t, sam, srcPlayFriends)
	at = slices.IndexFunc(friends, func(f sdui.Item) bool { return f.Title == "alex" })
	if !slices.Contains(friends[at].Badges, "Invited") || friends[at].Actions[0].Action.Op != "wittyplay.invite.cancel" || friends[at].Actions[0].Action.Item != sent.ID {
		t.Errorf("alex once invited: %+v", friends[at])
	}

	// Alex sees it, with Accept into the game and Decline.
	got := onlineItems(t, alex, srcPlayInvites)
	if len(got) != 1 || got[0].Title != "sam invites you to play" || got[0].Subtitle != "Star Tide" || len(got[0].Actions) != 2 {
		t.Fatalf("alex's invites: %+v", got)
	}
	if acc := got[0].Actions[0]; acc.Glyph != "a" || acc.Action.Type != "navigate" || acc.Action.To != "/game/7?invite="+sent.ID {
		t.Errorf("accept: %+v", acc.Action)
	}
	if dec := got[0].Actions[1]; dec.Glyph != "b" || dec.Action.Op != "wittyplay.invite.decline" || dec.Action.Item != sent.ID {
		t.Errorf("decline: %+v", dec.Action)
	}
	var list map[string][]inviteView
	w = callAs(t, h, "GET", "/games/online/invites", alex)
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list["received"]) != 1 || len(list["sent"]) != 0 {
		t.Errorf("alex's invites over the API: %s", w.Body)
	}

	// The invite's game is gone from the server: accepting answers that it has ended, and uses it up.
	if w := callAs(t, h, "POST", "/games/online/invites/"+sent.ID+"/accept", alex); w.Code != 404 {
		t.Errorf("accepting with no game running: %d", w.Code)
	}
	w = callAs(t, h, "POST", "/games/online/friends/2/invite", sam)
	_ = json.Unmarshal(w.Body.Bytes(), &sent)
	if w := callAs(t, h, "DELETE", "/games/online/invites/"+sent.ID, sam); w.Code != 204 {
		t.Errorf("sam took it back: %d", w.Code)
	}
	w = callAs(t, h, "POST", "/games/online/friends/2/invite", sam)
	_ = json.Unmarshal(w.Body.Bytes(), &sent)
	if w := callAs(t, h, "DELETE", "/games/online/invites/"+sent.ID, kit); w.Code != 404 {
		t.Errorf("someone else declined it: %d", w.Code)
	}
	if w := callAs(t, h, "DELETE", "/games/online/invites/"+sent.ID, alex); w.Code != 204 {
		t.Errorf("alex declined: %d", w.Code)
	}
	if got := onlineItems(t, alex, srcPlayInvites); len(got) != 0 {
		t.Errorf("a declined invite stayed: %+v", got)
	}

	// Kit, a Kids profile that approved sam, can't be asked to a game it may not open.
	startTable(t, "s2", alex.User, "alex", "Den", seatGame{Item: 12, Title: "Grown-up game", Console: "snes", Players: 2})
	t.Cleanup(func() { invites.ended("s2") })
	if err := a.setKidsFriends(t.Context(), kit.User, []int64{sam.User, alex.User}); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, h, "POST", "/games/online/friends/4/invite", alex); w.Code != 403 {
		t.Errorf("kit invited to a game it can't open: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, h, "POST", "/games/online/friends/4/invite", sam); w.Code != 201 {
		t.Errorf("kit invited to Star Tide: %d %s", w.Code, w.Body)
	}
	if got := onlineItems(t, kit, srcPlayInvites); len(got) != 1 {
		t.Errorf("kit's invite: %+v", got)
	}

	// Someone already in the game isn't offered Invite; the seat an invite gives is a code's.
	if j, err := seats.joinInvited("s1", alex.User, "alex"); err != nil || j.Seat != 1 {
		t.Fatalf("seated by an invite: %+v %v", j, err)
	}
	for _, f := range onlineItems(t, sam, srcPlayFriends) {
		if f.Title == "alex" && len(f.Actions) != 0 {
			t.Errorf("alex is in the game, and still offered: %+v", f.Actions)
		}
	}
	if w := callAs(t, h, "POST", "/games/online/friends/2/invite", sam); w.Code != 409 {
		t.Errorf("inviting someone already in: %d", w.Code)
	}
	// Alex hosts s2 too; open, it's a Join on alex's card for sam (who is in s1 as its host).
	seats.setOpen("s2", alex.User, true)
	for _, f := range onlineItems(t, sam, srcPlayFriends) {
		if f.Title == "alex" && (len(f.Actions) != 1 || f.Actions[0].ID != "join" || f.Actions[0].Action.Item != "s2") {
			t.Errorf("alex's open room on sam's list: %+v", f.Actions)
		}
	}
	if _, err := seats.joinInvited("nosuch", alex.User, "alex"); err == nil {
		t.Error("seated in a game that isn't running")
	}
}
