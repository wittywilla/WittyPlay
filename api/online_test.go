// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
)

func friendNames(t *testing.T, a *App, id service.Identity) []string {
	t.Helper()
	list, err := a.playFriends(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range list {
		out = append(out, f.Name)
	}
	return out
}

// Friends are the server's other profiles: a Kids profile sees, and is seen by, only those an
// administrator chose; a guest has none and is nobody's.
func TestPlayFriendsFollowTheRules(t *testing.T) {
	a, _ := testApp(t, personHost(t, nil))
	if err := a.setKidsFriends(t.Context(), kit.User, []int64{alex.User, kit.User}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		who  service.Identity
		want []string
	}{
		{"owner", owner, []string{"alex", "sam"}},
		{"alex", alex, []string{"kit", "owner", "sam"}},
		{"kit", kit, []string{"alex"}}, // never itself, though it was listed
		{"kira", kira, nil},
		{"gus", gus, nil},
	} {
		if got := friendNames(t, a, c.who); !slices.Equal(got, c.want) {
			t.Errorf("%s sees %v, want %v", c.name, got, c.want)
		}
	}
	if !a.mayJoinRoom(t.Context(), alex, sam.User) || a.mayJoinRoom(t.Context(), kit, alex.User) || a.mayJoinRoom(t.Context(), gus, alex.User) {
		t.Error("who may join a room")
	}
}

// What a friend is doing shows only as far as it shares: someone who doesn't appear online is
// offline to everyone.
func TestPresenceSharesOnlyWhatAProfileAllows(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	ctx := t.Context()
	users := []host.User{{ID: 2, Name: "alex", Kind: "adult", LastSeen: 1}, {ID: 3, Name: "sam", Kind: "adult"}}
	if p := a.playPresence(ctx, users); p[2].State != "offline" || p[3].State != "offline" {
		t.Fatalf("not seen for ages: %+v", p)
	}
	old := playingNow
	t.Cleanup(func() { playingNow = old })
	playingNow = func(user int64) (int64, string, bool) {
		switch user {
		case alex.User:
			return 7, "Star Tide", true
		case sam.User:
			return 12, "Moth Racer", true
		}
		return 0, "", false
	}
	if err := a.setPresence(ctx, sam.User, Presence{Playing: false, Online: false}); err != nil {
		t.Fatal(err)
	}
	p := a.playPresence(ctx, users)
	if p[2].State != "playing" || p[2].Game != "Star Tide" {
		t.Errorf("alex: %+v", p[2])
	}
	if p[3].State != "offline" || p[3].Game != "" {
		t.Errorf("sam doesn't appear online, and showed %+v", p[3])
	}
	if err := a.setPresence(ctx, sam.User, Presence{Playing: false, Online: true}); err != nil {
		t.Fatal(err)
	}
	if p := a.playPresence(ctx, users); p[3].State != "online" || p[3].Game != "" {
		t.Errorf("sam appears online without its game, and showed %+v", p[3])
	}

	// The friends list: playing first, and a game the asking profile may not see has no title.
	if err := a.setPresence(ctx, sam.User, Presence{Playing: true, Online: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.setKidsFriends(ctx, kit.User, []int64{alex.User, sam.User}); err != nil {
		t.Fatal(err)
	}
	var list []friendView
	decode(t, as(t, h, "GET", "/games/online/friends", kit, nil), &list)
	if len(list) != 2 || list[0].State != "playing" || list[1].State != "playing" {
		t.Fatalf("%+v", list)
	}
	for _, f := range list {
		if f.Name == "sam" && (f.Game != "" || f.Item != 0) {
			t.Errorf("a game kit may not see was named: %+v", f)
		}
		if f.Name == "alex" && f.Game != "Star Tide" {
			t.Errorf("alex: %+v", f)
		}
	}
	if w := as(t, h, "GET", "/games/online/friends", gus, nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("a guest's friends: %s", w.Body)
	}
}

func TestPlayOnlineRoutes(t *testing.T) {
	var audits atomic.Int32
	a, h := testApp(t, personHost(t, &audits))
	var me struct {
		SharePlaying bool `json:"sharePlaying"`
		AppearOnline bool `json:"appearOnline"`
		CanShare     bool `json:"canShare"`
	}
	decode(t, as(t, h, "GET", "/games/online/me", alex, nil), &me)
	if !me.SharePlaying || !me.AppearOnline || !me.CanShare {
		t.Fatalf("%+v", me)
	}
	decode(t, as(t, h, "PUT", "/games/online/me", alex, map[string]any{"sharePlaying": false}), &me)
	if me.SharePlaying || !me.AppearOnline {
		t.Fatalf("one choice changes only that one: %+v", me)
	}
	decode(t, as(t, h, "PUT", "/games/online/me", alex, map[string]any{"appearOnline": false}), &me)
	if me.SharePlaying || me.AppearOnline {
		t.Fatalf("%+v", me)
	}
	if w := as(t, h, "PUT", "/games/online/me", gus, map[string]any{"sharePlaying": true}); w.Code != 403 {
		t.Errorf("a guest shared: %d", w.Code)
	}
	decode(t, as(t, h, "GET", "/games/online/me", gus, nil), &me)
	if me.SharePlaying || me.AppearOnline || me.CanShare {
		t.Errorf("a guest: %+v", me)
	}
	if w := as(t, h, "PUT", "/games/online/me", alex, map[string]any{}); w.Code != 400 {
		t.Errorf("no choice: %d", w.Code)
	}

	kids := "/games/online/kids/" + strconv.FormatInt(kit.User, 10)
	if w := as(t, h, "GET", kids, alex, nil); w.Code != 403 {
		t.Errorf("not an administrator: %d", w.Code)
	}
	if w := as(t, h, "PUT", kids, alex, map[string]any{"friends": []int64{alex.User}}); w.Code != 403 {
		t.Errorf("not an administrator, changing: %d", w.Code)
	}
	w := as(t, h, "PUT", kids, owner, map[string]any{"friends": []int64{sam.User, alex.User}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"friends":[2,3]`) || audits.Load() != 1 {
		t.Fatalf("%d %s (audit rows %d)", w.Code, w.Body, audits.Load())
	}
	if w := as(t, h, "PUT", kids, owner, map[string]any{"friends": []int64{gus.User}}); w.Code != 400 {
		t.Errorf("a guest as a friend: %d", w.Code)
	}
	if w := as(t, h, "GET", "/games/online/kids/"+strconv.FormatInt(alex.User, 10), owner, nil); w.Code != 400 {
		t.Errorf("an adult's chosen friends: %d", w.Code)
	}
	if w := as(t, h, "GET", "/games/online/kids/99", owner, nil); w.Code != 404 {
		t.Errorf("nobody: %d", w.Code)
	}
	var page struct {
		Candidates []kidsCandidate `json:"candidates"`
	}
	decode(t, as(t, h, "GET", kids, owner, nil), &page)
	approved := map[string]bool{}
	for _, c := range page.Candidates {
		approved[c.Name] = c.Approved
	}
	// Every profile but kit itself and the guest, approved or not.
	if len(page.Candidates) != 4 || !approved["alex"] || !approved["sam"] || approved["owner"] || approved["kira"] {
		t.Errorf("candidates: %+v", page.Candidates)
	}
	if got := friendNames(t, a, kit); !slices.Equal(got, []string{"alex", "sam"}) {
		t.Errorf("kit's friends now: %v", got)
	}
	// An account that goes takes its choices and its place in Kids profiles' friends.
	a.forget(alex.User)
	if ids, _ := a.kidsFriends(t.Context(), kit.User); slices.Contains(ids, alex.User) {
		t.Errorf("a gone account is still a friend: %v", ids)
	}
	if p := a.presenceOf(t.Context(), alex.User, "adult"); !p.Online {
		t.Error("a gone account's choices stayed")
	}
}
