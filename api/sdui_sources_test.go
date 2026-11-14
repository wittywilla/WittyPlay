// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/service"
)

// Ported from the server's wittyplay_online_test.go, which answered these sources itself.

// onlineGame is a game on the fake server, and who may see it.
type onlineGame struct {
	id      int64
	title   string
	console string
	hidden  func(user int64) bool
}

// onlineHost is a server's host API for WittyPlay Online: the accounts of personHost, and games 7
// (Star Tide, snes, with a poster; alex has played it 3 hr 12 min), 12 (snes, which the Kids
// profiles may not see) and 40 (nes). Item 99 is nobody's.
func onlineHost(t *testing.T) *host.Client {
	t.Helper()
	now := time.Now().Unix()
	users := []host.User{
		{ID: 1, Name: "owner", Kind: "adult", Admin: true},
		{ID: 2, Name: "alex", Kind: "adult", LastSeen: now},
		{ID: 3, Name: "sam", Kind: "adult", LastSeen: now},
		{ID: 4, Name: "kit", Kind: "kids"},
		{ID: 5, Name: "kira", Kind: "kids"},
		{ID: 6, Name: "gus", Kind: "guest"},
	}
	kidsOnly := func(u int64) bool { return u == 4 || u == 5 }
	games := []onlineGame{
		{7, "Star Tide", "snes", nil},
		{12, "Grown-up game", "snes", kidsOnly},
		{40, "Moth Racer", "nes", nil},
	}
	sees := func(g onlineGame, user int64) bool { return g.hidden == nil || !g.hidden(user) }
	full := func(g onlineGame, user int64) map[string]any {
		m := map[string]any{"id": g.id, "kind": "game", "title": g.title, "console": g.console, "poster": ""}
		if g.id == 7 {
			m["poster"], m["year"] = "posters/7.jpg", 1993
			if user == 2 {
				m["progress"] = map[string]any{"positionSec": 3*3600 + 12*60}
			}
		}
		return m
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		on, _ := strconv.ParseInt(r.Header.Get(host.HeaderOnUser), 10, 64)
		switch {
		case p == "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
		case p == "/items":
			q := r.URL.Query()
			if q.Get("full") != "1" || q.Get("kind") != "game" || on == 0 {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"not as asked"}`))
				return
			}
			only := map[int64]bool{}
			for _, v := range strings.Split(q.Get("ids"), ",") {
				if id, err := strconv.ParseInt(v, 10, 64); err == nil {
					only[id] = true
				}
			}
			out := []map[string]any{}
			for _, g := range games {
				if sees(g, on) && (len(only) == 0 || only[g.id]) {
					out = append(out, full(g, on))
				}
			}
			off, _ := strconv.Atoi(q.Get("offset"))
			out = out[min(off, len(out)):]
			_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
		case strings.HasPrefix(p, "/items/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/items/"), 10, 64)
			for _, g := range games {
				if g.id == id && sees(g, on) {
					_ = json.NewEncoder(w).Encode(service.Item{ID: g.id, Kind: "game", Title: g.title, Facets: map[string]string{"console": g.console}})
					return
				}
			}
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such item"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such thing"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

// sourceYields is what WittyPlay's sdui.json says each source gives, and the commands the
// server's catalogue knows.
func sourceYields(t *testing.T) (map[string][]string, map[string]bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "sdui.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Sources []struct {
			Type   string   `json:"type"`
			Yields []string `json:"yields"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	yields := map[string][]string{}
	for _, s := range file.Sources {
		yields[s.Type] = s.Yields
	}
	var cat struct {
		Commands []struct {
			Op string `json:"op"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(sdui.CatalogJSON, &cat); err != nil {
		t.Fatal(err)
	}
	ops := map[string]bool{}
	for _, c := range cat.Commands {
		ops[c.Op] = true
	}
	return yields, ops
}

// onlineItems asks for a source as a profile's page would, and checks each item is one the server
// keeps: a kind the source yields, and only commands the catalogue has.
func onlineItems(t *testing.T, who service.Identity, src string, params ...any) []sdui.Item {
	t.Helper()
	source := sdui.Source{"type": src}
	for i := 0; i+1 < len(params); i += 2 {
		source[params[i].(string)] = params[i+1]
	}
	ans, err := SDUIItems(sdui.ItemsRequest{Source: source, Limit: 50, Viewer: sdui.Viewer{User: who.User, ProfileKind: who.Kind, Admin: who.Admin}})
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	yields, ops := sourceYields(t)
	if _, ok := yields[src]; !ok {
		t.Fatalf("%s isn't in sdui.json", src)
	}
	for _, it := range ans.Items {
		if !slices.Contains(yields[src], it.Kind) || it.ID == "" || it.Title == "" {
			t.Errorf("%s gave an item the server drops: %+v", src, it)
		}
		acts := []*sdui.Action{it.Action}
		for _, a := range it.Actions {
			acts = append(acts, a.Action)
		}
		for _, a := range acts {
			if a != nil && a.Type == "command" && !ops[a.Op] {
				t.Errorf("%s: %s isn't a command the catalogue has", src, a.Op)
			}
		}
		for k, v := range it.Images {
			if !strings.HasPrefix(v, "/api/") {
				t.Errorf("%s: picture %s at %s isn't on the server", src, k, v)
			}
		}
	}
	return ans.Items
}

// startTable starts a game in the seat book for a test, and forgets it after.
func startTable(t *testing.T, sid string, hostID int64, hostName, device string, g seatGame) {
	t.Helper()
	seats.started(sid, hostID, hostName, device, g)
	t.Cleanup(func() { seats.ended(sid) })
}

// The sources, as a profile is given them: friends playing first, with the game only when the
// friend shares it and the profile may open it; no rooms for a Kids profile; a room with its seats,
// its code for its host only; a player for each seat.
func TestPlayOnlineSources(t *testing.T) {
	a, _ := testApp(t, onlineHost(t))
	if err := a.setKidsFriends(t.Context(), kit.User, []int64{alex.User}); err != nil {
		t.Fatal(err)
	}
	startTable(t, "s1", sam.User, "sam", "Living room", seatGame{Item: 99, Title: "Lost Tide", Console: "snes", Players: 2})

	friends := onlineItems(t, alex, srcPlayFriends)
	if len(friends) != 3 || friends[0].Kind != "person" || friends[0].Title != "sam" || friends[0].Badges[0] != "Playing" || friends[0].Route != "" {
		t.Fatalf("alex's friends: %+v", friends)
	}
	// Alex may not open item 99, so the game isn't named.
	if friends[0].Subtitle != "Playing" {
		t.Errorf("a game alex can't open was named: %q", friends[0].Subtitle)
	}
	if got := onlineItems(t, kit, srcPlayFriends); len(got) != 1 || got[0].Title != "alex" {
		t.Errorf("kit's friends: %+v", got)
	}
	if got := onlineItems(t, gus, srcPlayFriends); len(got) != 0 {
		t.Errorf("a guest's friends: %+v", got)
	}

	// Not open yet: no rooms. Open, but of a game alex can't open: still none.
	if got := onlineItems(t, alex, srcPlayRooms); len(got) != 0 {
		t.Errorf("a closed room offered: %+v", got)
	}
	seats.setOpen("s1", sam.User, true)
	if got := onlineItems(t, alex, srcPlayRooms); len(got) != 0 {
		t.Errorf("a room of a game alex may not play: %+v", got)
	}
	if got := onlineItems(t, kit, srcPlayRooms); len(got) != 0 {
		t.Errorf("a Kids profile was offered a room: %+v", got)
	}

	// The host's room: the room, then a card for each player.
	code, _, _ := seats.invite("s1", sam.User)
	room := onlineItems(t, sam, srcPlayRoom)
	if len(room) != 3 || room[0].Kind != "view" || room[0].Title != "Lost Tide" || room[0].Tagline != code || room[0].Link != "/games?join="+code {
		t.Fatalf("sam's room: %+v", room)
	}
	ids := []string{}
	for _, a := range room[0].Actions {
		ids = append(ids, a.ID+"/"+a.Glyph+"/"+a.Action.Op)
	}
	// Opened earlier in this test, so the third button closes it.
	if want := []string{"play/a/", "invite/x/wittyplay.room.invite", "close/y/wittyplay.room.close"}; !slices.Equal(ids, want) {
		t.Errorf("the host's buttons: %v, want %v", ids, want)
	}
	if room[0].Actions[0].Action.Type != "play" || room[0].Actions[0].Action.Item != "99" {
		t.Errorf("the host's play button: %+v", room[0].Actions[0].Action)
	}
	if p := room[1]; p.Kind != "person" || p.Title != "sam" || p.Subtitle != "Host" || p.Badges[0] != "P1" || p.Route != "" {
		t.Errorf("the host: %+v", p)
	}
	if p := room[2]; p.Kind != "person" || p.Title != "Waiting…" || p.Subtitle != "Free" || p.Badges[0] != "Free" {
		t.Errorf("a free seat: %+v", p)
	}
	if _, err := seats.joinOpen("s1", alex.User, "alex"); err != nil {
		t.Fatal(err)
	}
	room = onlineItems(t, alex, srcPlayRoom)
	if len(room) != 3 || room[0].Tagline != "" || room[0].Link != "" || room[2].Title != "alex" || len(room[0].Actions) != 1 ||
		room[0].Actions[0].Action.Op != "wittyplay.room.leave" || room[0].Actions[0].Glyph != "b" || room[0].Actions[0].Action.Params["user"] != "2" {
		t.Errorf("alex's view of the room, with no code: %+v", room)
	}
	pads := onlineItems(t, alex, srcPlayControllers)
	if len(pads) != 2 || pads[0].Subtitle != "sam · Living room" || pads[1].Subtitle != "alex" {
		t.Errorf("controllers: %+v", pads)
	}
	if got := onlineItems(t, kira, srcPlayRoom); len(got) != 0 {
		t.Errorf("a room kira isn't in: %+v", got)
	}
	if got := onlineItems(t, kira, srcPlayControllers); len(got) != 0 {
		t.Errorf("controllers of a game kira isn't in: %+v", got)
	}
}

// Memories are a profile's own saves and kept screenshots, newest first, of games it may see.
func TestMemoriesListOnlyGamesTheProfileMaySee(t *testing.T) {
	a, _ := testApp(t, onlineHost(t))
	keep := func(item int64, name string) {
		dir := a.shotsDir(alex.User, item)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep(99, "1794460000000000000.png")
	// Item 99 isn't a game alex may open, so it gives nothing.
	if got := onlineItems(t, alex, srcPlayMemories); len(got) != 0 {
		t.Errorf("%+v", got)
	}
	keep(7, "1794460000000000000.png")
	got := onlineItems(t, alex, srcPlayMemories)
	if len(got) != 1 || got[0].Kind != "photo" || got[0].Title != "Star Tide" || got[0].Badges[0] != "Shot" ||
		got[0].Images["backdrop"] != "/api/modules/wittyplay/games/7/shots/1794460000000000000.png" ||
		got[0].Action == nil || got[0].Action.As != "layer" || got[0].Action.To != "/page/wittyplay-memory?item=7&name=1794460000000000000.png" {
		t.Errorf("a kept screenshot: %+v", got)
	}
	if got := onlineItems(t, sam, srcPlayMemories); len(got) != 0 {
		t.Errorf("alex's memories shown to sam: %+v", got)
	}
}

// With games the profiles may play: a game played is in the recent row with its era, players,
// time played and latest save; before anything is played the row is the classics; the eras count
// what each profile may see; its saves are listed; and an open room of it is offered to a friend,
// though not to its host or a Kids profile.
func TestPlayOnlineWithAGame(t *testing.T) {
	a, _ := testApp(t, onlineHost(t))
	ctx := t.Context()
	if err := a.noteGamePlayed(ctx, alex.User, 7); err != nil {
		t.Fatal(err)
	}
	dir := a.saveDir(alex.User, 7)
	name := slotFileNames()[3]
	if name == "" {
		t.Fatal("no name for slot 3")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{name + ".state", name + ".png"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	recent := onlineItems(t, alex, srcPlayRecent)
	if len(recent) != 1 || recent[0].Kind != "game" || recent[0].Badges[0] != "16-bit" || recent[0].RuntimeSec != 3*3600+12*60 ||
		recent[0].Route != "/game/7" || recent[0].Console != "SNES" || recent[0].Images["poster"] != "/api/items/7/poster" {
		t.Fatalf("recent: %+v", recent)
	}
	if want := "16-bit classics · 1 to 2 players · Played 3 hr 12 min · Save state 3 ready"; recent[0].Subtitle != want {
		t.Errorf("recent's line: %q, want %q", recent[0].Subtitle, want)
	}
	if acts := recent[0].Actions; len(acts) != 2 || acts[0].Label != "Resume" || acts[0].Action.To != "/game/7" || acts[1].Action.To != "/item/7" {
		t.Errorf("recent's buttons: %+v", acts)
	}
	// Nothing played: the classics, oldest era first, then by title.
	classics := onlineItems(t, kit, srcPlayRecent)
	if len(classics) != 2 || classics[0].Title != "Moth Racer" || classics[1].Title != "Star Tide" || classics[0].Actions[0].Label != "Play" ||
		classics[0].Subtitle != "8-bit classics · 1 to 2 players" {
		t.Errorf("kit's classics: %+v", classics)
	}

	eras := onlineItems(t, alex, srcPlayCollections)
	if len(eras) != 2 || eras[0].ID != "era-8-bit" || eras[1].ID != "era-16-bit" || eras[1].Count != 2 || eras[1].Kind != "collection" ||
		eras[1].Subtitle != "2 games" || eras[1].Route != "/games?era=16-bit" {
		t.Errorf("collections: %+v", eras)
	}
	if got := onlineItems(t, kit, srcPlayCollections); len(got) != 2 || got[1].Count != 1 || got[1].Subtitle != "1 game" {
		t.Errorf("kit's collections: %+v", got)
	}
	if got := onlineItems(t, alex, srcPlayCollections, "era", "16-bit"); len(got) != 2 || got[0].Kind != "game" || got[0].Title != "Grown-up game" || got[0].Badges[0] != "16-bit" {
		t.Errorf("the 16-bit games: %+v", got)
	}
	if got := onlineItems(t, alex, srcPlayCollections, "era", "pocket"); len(got) != 0 {
		t.Errorf("handheld games: %+v", got)
	}

	saves := onlineItems(t, alex, srcPlaySaveStates, "item", "7")
	if len(saves) != 1 || saves[0].ID != "slot-3" || saves[0].Title != "Save state 3" || saves[0].Images["backdrop"] != "/api/modules/wittyplay/games/7/saves/3/thumb" {
		t.Errorf("saves: %+v", saves)
	}
	if got := onlineItems(t, alex, srcPlaySaveStates, "item", float64(7)); len(got) != 1 {
		t.Errorf("saves, the item as a number: %+v", got)
	}
	if got := onlineItems(t, kit, srcPlaySaveStates, "item", "12"); len(got) != 0 {
		t.Errorf("saves of a game kit may not see: %+v", got)
	}
	if got := onlineItems(t, alex, srcPlayMemories); len(got) != 1 || got[0].Badges[0] != "Save" || got[0].Title != "Star Tide" {
		t.Errorf("memories: %+v", got)
	}

	// An open room of it, for the other adult to join with no code.
	startTable(t, "room1", alex.User, "alex", "TV", seatGame{Item: 7, Title: "Star Tide", Console: "snes"})
	seats.setOpen("room1", alex.User, true)
	rooms := onlineItems(t, sam, srcPlayRooms)
	if len(rooms) != 1 || rooms[0].ID != "room1" || rooms[0].Title != "Star Tide · 1 of 2" || rooms[0].Subtitle != "Shared · alex" {
		t.Fatalf("rooms: %+v", rooms)
	}
	if a := rooms[0].Actions; len(a) != 1 || a[0].Glyph != "a" || a[0].Action.Op != "wittyplay.room.join" || a[0].Action.Item != "room1" {
		t.Errorf("an open room's button: %+v", a)
	}
	if got := onlineItems(t, alex, srcPlayRooms); len(got) != 0 {
		t.Errorf("the host was offered its own room: %+v", got)
	}
	if got := onlineItems(t, kit, srcPlayRooms); len(got) != 0 {
		t.Errorf("a Kids profile was offered a room: %+v", got)
	}
	// A friend who may see the game is told its name.
	if f := onlineItems(t, sam, srcPlayFriends); len(f) == 0 || f[0].Title != "alex" || f[0].Subtitle != "Playing Star Tide" {
		t.Errorf("sam's friends: %+v", f)
	}
}

// The two pages of choices: a profile's own switches (off and explained for a guest), and an
// administrator's list of a Kids profile's possible friends.
func TestPrivacyAndKidsFriendsSources(t *testing.T) {
	a, _ := testApp(t, onlineHost(t))
	ctx := t.Context()
	if err := a.setPresence(ctx, alex.User, Presence{Playing: false, Online: true}); err != nil {
		t.Fatal(err)
	}
	rows := onlineItems(t, alex, srcPlayPrivacy)
	if len(rows) != 2 || rows[0].Control.Name != "sharePlaying" || rows[0].Control.Value != false || rows[1].Control.Value != true || rows[0].Control.Disabled {
		t.Errorf("alex's choices: %+v", rows)
	}
	rows = onlineItems(t, gus, srcPlayPrivacy)
	if len(rows) != 2 || !rows[0].Control.Disabled || rows[0].Control.Value != false || rows[0].Control.Why == "" {
		t.Errorf("a guest's choices: %+v", rows)
	}

	if err := a.setKidsFriends(ctx, kit.User, []int64{alex.User}); err != nil {
		t.Fatal(err)
	}
	rows = onlineItems(t, owner, srcPlayKidsFriends, "user", "4")
	byName := map[string]sdui.Item{}
	for _, r := range rows {
		byName[r.Title] = r
	}
	// Every profile but kit itself and the guest.
	if len(rows) != 4 || byName["alex"].Trailing != "Approved" || byName["alex"].Control.Value != true || byName["sam"].Trailing != "Not approved" {
		t.Fatalf("kit's possible friends: %+v", rows)
	}
	if act := byName["sam"].Action; act.Op != "wittyplay.kids.friends" || act.Item != "4" || !slices.Equal(act.Params["friends"].([]int64), []int64{2, 3}) {
		t.Errorf("approving sam: %+v", act)
	}
	if act := byName["alex"].Action; len(act.Params["friends"].([]int64)) != 0 {
		t.Errorf("removing alex: %+v", act)
	}
	if got := onlineItems(t, alex, srcPlayKidsFriends, "user", "4"); len(got) != 0 {
		t.Errorf("not an administrator: %+v", got)
	}
	if got := onlineItems(t, owner, srcPlayKidsFriends, "user", "2"); len(got) != 0 {
		t.Errorf("an adult's chosen friends: %+v", got)
	}
}

// The answer to the server: paged as asked and held to the source's limit, nothing for a profile
// that may not play games or for nobody, and the consoles from package rpc.
func TestSDUIItemsPagesAndDispatches(t *testing.T) {
	testApp(t, onlineHost(t))
	ask := func(req sdui.ItemsRequest) sdui.ItemsAnswer {
		t.Helper()
		ans, err := SDUIItems(req)
		if err != nil {
			t.Fatal(err)
		}
		return ans
	}
	viewer := sdui.Viewer{User: alex.User, ProfileKind: "adult"}
	ans := ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayCollections, "era": "16-bit"}, Limit: 1, Viewer: viewer})
	if len(ans.Items) != 1 || !ans.More || ans.Total != 2 {
		t.Errorf("one of two: %+v", ans)
	}
	ans = ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayCollections, "era": "16-bit", "limit": float64(1)}, Limit: 20, Viewer: viewer})
	if len(ans.Items) != 1 || !ans.More {
		t.Errorf("the source's own limit: %+v", ans)
	}
	ans = ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayCollections, "era": "16-bit"}, Offset: 1, Limit: 20, Viewer: viewer})
	if len(ans.Items) != 1 || ans.More || ans.Items[0].Title != "Star Tide" {
		t.Errorf("the second page: %+v", ans)
	}
	if ans := ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayCollections}, Limit: 20, Viewer: sdui.Viewer{User: alex.User, Cannot: []string{"games"}}}); len(ans.Items) != 0 {
		t.Errorf("a profile that may not play games: %+v", ans)
	}
	if ans := ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayCollections}, Limit: 20}); len(ans.Items) != 0 {
		t.Errorf("nobody: %+v", ans)
	}
	if ans := ask(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayConsoles, "only": "all"}, Limit: 5, Viewer: viewer}); len(ans.Items) != 5 || ans.Items[0].Kind != "view" {
		t.Errorf("the consoles: %+v", ans)
	}
}
