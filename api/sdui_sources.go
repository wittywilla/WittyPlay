// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// WittyPlay Online's data sources (sdui.json): its home row, classics by era, friends, rooms, save
// states, memories, controllers, and the two pages of choices about friends. Moved from the server
// (wittyplay_online.go), which answered them itself while the games were its own. The server asks
// the module for them with the person the page is for (Viewer.User), and cleans what comes back
// as it does any module's items: only WittyPlay's own commands, routes of the app and pictures
// under /api/ stay.
//
// What a person may see comes from the server (Host.ItemsFull and Host.Item on their behalf);
// what they played, their saves, screenshots, friends and the games being played now are
// WittyPlay's own.

const (
	srcPlayConsoles    = "wittyplay.consoles"
	srcPlayRecent      = "wittyplay.recent"
	srcPlayCollections = "wittyplay.collections"
	srcPlayFriends     = "wittyplay.friends"
	srcPlayRooms       = "wittyplay.rooms"
	srcPlayRoom        = "wittyplay.room"
	srcPlaySaveStates  = "wittyplay.saveStates"
	srcPlayMemories    = "wittyplay.memories"
	srcPlayControllers = "wittyplay.controllers"
	// The profile's own two choices as switches, and an administrator's choice of a Kids
	// profile's friends (user: the profile's ID), each a row with a control.
	srcPlayPrivacy     = "wittyplay.privacy"
	srcPlayKidsFriends = "wittyplay.kidsFriends"
)

// onlineSources are the sources answered here; wittyplay.consoles is package rpc's.
var onlineSources = []string{srcPlayRecent, srcPlayCollections, srcPlayFriends, srcPlayRooms, srcPlayRoom, srcPlaySaveStates, srcPlayMemories, srcPlayControllers, srcPlayPrivacy, srcPlayKidsFriends}

// maxVisibleGames is as many games as the classics are made from.
const maxVisibleGames = 20000

// Eras as WittyPlay Online shows them, in the order its Classics tab lists them.
var eraOrder = []string{eraFirst, era8Bit, era16Bit, eraEarly3D, eraDisc, eraPocket}

var eraLabels = map[string]struct{ badge, title string }{
	eraFirst:   {"First era", "First-era classics"},
	era8Bit:    {"8-bit", "8-bit classics"},
	era16Bit:   {"16-bit", "16-bit classics"},
	eraEarly3D: {"3D era", "3D-era classics"},
	eraDisc:    {"Disc era", "Disc-era classics"},
	eraPocket:  {"Handheld", "Handheld classics"},
}

var presenceWords = map[string]string{"playing": "Playing", "online": "Online", "away": "Away", "offline": "Offline"}

// SDUIItems answers the server's sdui.items call for every WittyPlay source: the library and the
// store (sdui_store.go), WittyPlay Online's here, and the consoles (rpc.ConsoleItems) for the rest.
func SDUIItems(req sdui.ItemsRequest) (sdui.ItemsAnswer, error) {
	typ := req.Source.Type()
	if ans, ok, err := tvItems(req); ok {
		return ans, err
	}
	if !slices.Contains(onlineSources, typ) {
		return rpc.ConsoleItems(req)
	}
	// A page for nobody, or a profile that may not play games, has none of these.
	if req.Viewer.User <= 0 || slices.Contains(req.Viewer.Cannot, "games") {
		return sdui.ItemsAnswer{}, nil
	}
	a, err := Get()
	if err != nil {
		return sdui.ItemsAnswer{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return a.onlineItems(ctx, req)
}

// sduiAsk is one source asked for, and who for.
type sduiAsk struct {
	a   *App
	ctx context.Context
	req sdui.ItemsRequest
	id  service.Identity
	on  host.On
}

func (a *App) onlineItems(ctx context.Context, req sdui.ItemsRequest) (sdui.ItemsAnswer, error) {
	q := &sduiAsk{a: a, ctx: ctx, req: req,
		id: service.Identity{User: req.Viewer.User, Kind: req.Viewer.ProfileKind, Admin: req.Viewer.Admin},
		on: host.On{User: req.Viewer.User}}
	var all []sdui.Item
	var err error
	switch req.Source.Type() {
	case srcPlayRecent:
		all, err = q.recent()
	case srcPlayCollections:
		all, err = q.collections()
	case srcPlayFriends:
		all, err = q.friends()
	case srcPlayRooms:
		all, err = q.rooms()
	case srcPlayRoom:
		all = q.room()
	case srcPlaySaveStates:
		all = q.saveStates()
	case srcPlayMemories:
		all, err = q.memories()
	case srcPlayControllers:
		all = q.controllers()
	case srcPlayPrivacy:
		all = q.privacy()
	case srcPlayKidsFriends:
		all, err = q.kidsFriendRows()
	}
	if err != nil {
		return sdui.ItemsAnswer{}, err
	}
	limit := req.Limit
	if lim := srcInt(req.Source, "limit"); lim > 0 && (limit <= 0 || lim < limit) {
		limit = lim
	}
	return pageItems(all, req.Offset, limit), nil
}

// pageItems is one page of a source's items: as many as limit (any, with none) from offset.
func pageItems(all []sdui.Item, offset, limit int) sdui.ItemsAnswer {
	total := len(all)
	if offset >= total {
		return sdui.ItemsAnswer{Total: total}
	}
	all = all[max(offset, 0):]
	more := limit > 0 && len(all) > limit
	if more {
		all = all[:limit]
	}
	return sdui.ItemsAnswer{Items: all, More: more, Total: total}
}

// srcStr and srcInt read a source's parameter, which the page may have written as a string or a number.
func srcStr(s sdui.Source, k string) string {
	switch v := s[k].(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func srcInt(s sdui.Source, k string) int {
	n, _ := strconv.Atoi(srcStr(s, k))
	return n
}

// ---------------------------------------------------------------- games as the server describes them

// fullGame is what these sources read of the server's own item JSON (Host.ItemsFull).
type fullGame struct {
	ID            int64    `json:"id"`
	Kind          string   `json:"kind"`
	Title         string   `json:"title"`
	Year          *int     `json:"year"`
	Overview      string   `json:"overview"`
	Tagline       string   `json:"tagline"`
	Genres        []string `json:"genres"`
	Rating        *float64 `json:"rating"`
	ContentRating string   `json:"contentRating"`
	Studio        string   `json:"studio"`
	RuntimeSec    float64  `json:"runtimeSec"`
	Poster        string   `json:"poster"`
	Backdrop      string   `json:"backdrop"`
	Console       string   `json:"console"`
	Titles        *struct {
		Native    string `json:"native"`
		English   string `json:"english"`
		Romanized string `json:"romanized"`
	} `json:"titles"`
	// Progress is the person's: a game's position is its time played.
	Progress *struct {
		PositionSec float64 `json:"positionSec"`
	} `json:"progress"`
}

// games asks the server for games the person may see, as its item JSON.
func (q *sduiAsk) games(hq host.Query) ([]fullGame, error) {
	hq.Kind = "game"
	raws, err := q.a.Host.ItemsFull(q.ctx, q.on, hq)
	if err != nil {
		return nil, err
	}
	out := make([]fullGame, 0, len(raws))
	for _, raw := range raws {
		var g fullGame
		if json.Unmarshal(raw, &g) == nil && g.ID > 0 && (g.Kind == "" || g.Kind == "game") {
			out = append(out, g)
		}
	}
	return out, nil
}

// gamesByID is these games as far as the person may see them, by id.
func (q *sduiAsk) gamesByID(ids []int64) (map[int64]fullGame, error) {
	out := map[int64]fullGame{}
	for len(ids) > 0 {
		n := min(len(ids), 500)
		got, err := q.games(host.Query{IDs: ids[:n], Limit: n})
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			out[g.ID] = g
		}
		ids = ids[n:]
	}
	return out, nil
}

// visibleGames is every game the person may see in the games libraries.
func (q *sduiAsk) visibleGames() ([]fullGame, error) {
	var out []fullGame
	for len(out) < maxVisibleGames {
		got, err := q.games(host.Query{LibraryKind: "games", Offset: len(out), Limit: 500})
		if err != nil {
			return nil, err
		}
		out = append(out, got...)
		if len(got) < 500 {
			break
		}
	}
	return out, nil
}

// mayOpen says whether the person may open a game, from the server.
func (q *sduiAsk) mayOpen(id int64) bool {
	if id <= 0 {
		return false
	}
	it, err := q.a.Host.Item(q.ctx, q.on, id)
	return err == nil && it.Kind == "game"
}

// item is one game as a page shows it (the server's rv.item for a game).
func gameItem(g fullGame) sdui.Item {
	id := strconv.FormatInt(g.ID, 10)
	out := sdui.Item{ID: id, Kind: "game", Title: g.Title, Overview: g.Overview, Tagline: g.Tagline, ContentRating: g.ContentRating, RuntimeSec: g.RuntimeSec,
		Genres: g.Genres, Studio: g.Studio, Route: "/game/" + id, Images: map[string]string{}}
	if g.Year != nil && *g.Year > 0 {
		out.Year = *g.Year
		out.Subtitle = strconv.Itoa(*g.Year)
	}
	if g.Rating != nil {
		out.Rating = *g.Rating
	}
	if g.Poster != "" {
		out.Images["poster"] = "/api/items/" + id + "/poster"
	}
	if g.Backdrop != "" {
		out.Images["backdrop"] = "/api/items/" + id + "/backdrop"
	}
	if g.Console != "" {
		out.Console = consoleShort(g.Console)
	}
	if t := g.Titles; t != nil {
		out.Titles = &sdui.Titles{Native: t.Native, English: t.English, Romanized: t.Romanized}
	}
	return out
}

// consoleShort is a console's short name (SNES, Mega Drive), for the home row's console bar; its id when it has none.
func consoleShort(id string) string {
	if c := wittyplay.ByID(id); c != nil {
		if c.Short != "" {
			return c.Short
		}
		return c.Name
	}
	return id
}

// consoleEra is a console's era, "" when it has none (a custom console, a test one).
func consoleEra(console string) string { return consoleTraitsTable[console].Era }

// playersWords says how many may play a console's games at once.
func playersWords(ports int) string {
	if ports <= 1 {
		return "1 player"
	}
	return fmt.Sprintf("1 to %d players", ports)
}

// playedWords is time played as a person says it: "Played 3 hr 12 min".
func playedWords(sec float64) string {
	if sec < 60 {
		return ""
	}
	d := time.Duration(sec) * time.Second
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("Played %d min", m)
	case m == 0:
		return fmt.Sprintf("Played %d hr", h)
	}
	return fmt.Sprintf("Played %d hr %d min", h, m)
}

// eraLine starts a game's line with its era and how many may play, and gives it its era's badge.
func eraLine(it *sdui.Item, console string) []string {
	line := []string{}
	if l, ok := eraLabels[consoleEra(console)]; ok {
		it.Badges = []string{l.badge}
		line = append(line, l.title)
	}
	if ports := consoleTraitsTable[console].Caps.Input.Ports; ports > 0 {
		line = append(line, playersWords(ports))
	}
	return line
}

// gameActions are a game's buttons in the home row's detail panel: A plays it (Resume when it has
// a save state), Y opens its page.
func gameActions(id int64, saved bool) []sdui.ItemAction {
	sid := strconv.FormatInt(id, 10)
	play := "Play"
	if saved {
		play = "Resume"
	}
	return []sdui.ItemAction{
		{ID: "play", Label: play, Glyph: "a", Action: &sdui.Action{Type: core.ActNavigate, To: "/game/" + sid}},
		{ID: "details", Label: "Details", Glyph: "y", Action: &sdui.Action{Type: core.ActNavigate, To: "/item/" + sid}},
	}
}

// ---------------------------------------------------------------- saves

// latestOwnSave is the slot of a person's own save made last (1 to MaxSlot), or 0 for none.
func latestOwnSave(saves []saveSlot) int {
	best, at := 0, int64(0)
	for _, sv := range saves {
		if sv.Slot >= 1 && sv.Slot <= stream.MaxSlot && sv.UpdatedAt > at {
			best, at = sv.Slot, sv.UpdatedAt
		}
	}
	return best
}

func slotTitle(slot int) string {
	switch slot {
	case 0:
		return "Automatic save"
	case stream.SlotUndo:
		return "Before the last load"
	case stream.SlotPrevAuto:
		return "Automatic save before starting over"
	}
	return fmt.Sprintf("Save state %d", slot)
}

// savedWords is when something was saved: "Today 19:42", "Yesterday 08:10", "3 November".
func savedWords(unix int64) string {
	t := time.Unix(unix, 0)
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "Today " + t.Format("15:04")
	case now.Sub(t) < 48*time.Hour && now.AddDate(0, 0, -1).Day() == d2:
		return "Yesterday " + t.Format("15:04")
	case y1 == y2:
		return t.Format("2 January")
	}
	return t.Format("2 January 2006")
}

// saveThumb and shotPicture are where the server serves a save's picture and a kept screenshot:
// the module's own routes.
func saveThumb(item int64, slot int) string {
	return fmt.Sprintf("/api/modules/wittyplay/games/%d/saves/%d/thumb", item, slot)
}

func shotPicture(item int64, name string) string {
	return fmt.Sprintf("/api/modules/wittyplay/games/%d/shots/%s", item, name)
}

// ---------------------------------------------------------------- the sources

// recent: the games this profile played, newest first, with their era, how many may play, how
// long it has played and its latest save.
func (q *sduiAsk) recent() ([]sdui.Item, error) {
	limit := q.req.Limit
	if limit <= 0 {
		limit = 200
	}
	rows, err := q.a.gameList(q.ctx, q.id.User, false, false, min(2*(q.req.Offset+limit), 400))
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(rows))
	for _, e := range rows {
		ids = append(ids, e.ItemID)
	}
	seen, err := q.gamesByID(ids)
	if err != nil {
		return nil, err
	}
	var out []sdui.Item
	for _, e := range rows {
		g, ok := seen[e.ItemID]
		if !ok {
			continue
		}
		it := gameItem(g)
		line := eraLine(&it, g.Console)
		if g.Progress != nil {
			if w := playedWords(g.Progress.PositionSec); w != "" {
				line = append(line, w)
				it.RuntimeSec = g.Progress.PositionSec
			}
		}
		slot := latestOwnSave(listSaves(q.a.saveDir(q.id.User, g.ID)))
		if slot > 0 {
			line = append(line, fmt.Sprintf("Save state %d ready", slot))
		}
		it.Subtitle = strings.Join(line, " · ")
		it.Actions = gameActions(g.ID, slot > 0)
		out = append(out, it)
	}
	// Nothing played yet: the home row is never empty while there are games. It shows the
	// classics, by title, in the same large tiles, so the first game is one press away.
	if len(out) == 0 && q.req.Offset == 0 {
		return q.classicsRow()
	}
	return out, nil
}

// classicsRow is every game this profile may see, by era then title, each with its era's badge,
// who may play and its buttons: what the home row shows before anything was played.
func (q *sduiAsk) classicsRow() ([]sdui.Item, error) {
	games, err := q.visibleGames()
	if err != nil {
		return nil, err
	}
	rank := map[string]int{}
	for i, e := range eraOrder {
		rank[e] = i + 1
	}
	sort.SliceStable(games, func(i, j int) bool {
		a, b := rank[consoleEra(games[i].Console)], rank[consoleEra(games[j].Console)]
		if a != b {
			return a != 0 && (b == 0 || a < b)
		}
		return strings.ToLower(games[i].Title) < strings.ToLower(games[j].Title)
	})
	out := make([]sdui.Item, 0, len(games))
	for _, g := range games {
		it := gameItem(g)
		it.Subtitle = strings.Join(eraLine(&it, g.Console), " · ")
		it.Actions = gameActions(g.ID, false)
		out = append(out, it)
	}
	return out, nil
}

// collections: the games this profile may see, by era, with how many each has. With era, the
// games of that era, by title.
func (q *sduiAsk) collections() ([]sdui.Item, error) {
	games, err := q.visibleGames()
	if err != nil {
		return nil, err
	}
	if era := srcStr(q.req.Source, "era"); era != "" {
		var out []sdui.Item
		for _, g := range games {
			if consoleEra(g.Console) == era {
				it := gameItem(g)
				it.Badges = []string{eraLabels[era].badge}
				out = append(out, it)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title) })
		return out, nil
	}
	count := map[string]int{}
	for _, g := range games {
		count[consoleEra(g.Console)]++
	}
	var out []sdui.Item
	for _, era := range eraOrder {
		if count[era] == 0 {
			continue
		}
		l := eraLabels[era]
		out = append(out, sdui.Item{ID: "era-" + era, Kind: "collection", Title: l.title, Count: count[era], Badges: []string{l.badge}, Icon: "game",
			Subtitle: fmt.Sprintf("%d %s", count[era], plural(count[era], "game", "games")), Route: "/games?era=" + era})
	}
	return out, nil
}

// friends: the profiles this one may see, each with what it's doing, playing first.
func (q *sduiAsk) friends() ([]sdui.Item, error) {
	friends, err := q.a.playFriends(q.ctx, q.id)
	if err != nil {
		return nil, err
	}
	pres := q.a.playPresence(q.ctx, friends)
	rank := map[string]int{"playing": 0, "online": 1, "away": 2, "offline": 3}
	sort.SliceStable(friends, func(i, j int) bool { return rank[pres[friends[i].ID].State] < rank[pres[friends[j].ID].State] })
	out := make([]sdui.Item, 0, len(friends))
	for _, f := range friends {
		p := pres[f.ID]
		sub := presenceWords[p.State]
		// A game this profile couldn't open itself is "Playing", with no title.
		if p.State == "playing" && q.mayOpen(p.Item) {
			sub = "Playing " + p.Game
		}
		// A person card that opens nothing: there's no profile page to open.
		out = append(out, sdui.Item{ID: "profile-" + strconv.FormatInt(f.ID, 10), Kind: "person", Title: profileName(&f), Subtitle: sub,
			Badges: []string{presenceWords[p.State]}, Icon: "user"})
	}
	return out, nil
}

// roomWords is a room's line: "Shared · Sam".
func roomWords(hostName string) string { return "Shared · " + hostName }

// seatsTaken is how many players a room has: its host and everyone in a seat.
func seatsTaken(t seatTableView) int {
	n := 1 // the host
	for _, h := range t.Holders {
		if h.Seat > 0 {
			n++
		}
	}
	return n
}

// rooms: the open rooms this profile may join: friends' games with a seat free, of games it may
// play. None for a Kids profile or a guest.
func (q *sduiAsk) rooms() ([]sdui.Item, error) {
	if q.id.Kind == "kids" || q.id.Kind == "guest" {
		return nil, nil
	}
	tables := seats.tablesNow()
	if len(tables) == 0 {
		return nil, nil
	}
	friends, err := q.a.playFriends(q.ctx, q.id)
	if err != nil {
		return nil, err
	}
	var out []sdui.Item
	for _, t := range tables {
		if !t.Open || t.Host == q.id.User || !slices.ContainsFunc(friends, func(u host.User) bool { return u.ID == t.Host }) {
			continue
		}
		if slicesHas(t.Holders, q.id.User) {
			continue // already in it: it's this profile's room
		}
		taken, total := seatsTaken(t), max(t.Game.Players, 1)
		if taken >= total || !q.mayOpen(t.Game.Item) {
			continue
		}
		join := &sdui.Action{Type: core.ActCommand, Op: "wittyplay.room.join", Item: t.Session}
		out = append(out, sdui.Item{ID: t.Session, Kind: "view", Title: fmt.Sprintf("%s · %d of %d", t.Game.Title, taken, total), Subtitle: roomWords(t.HostName),
			Count: taken, Icon: "users", Badges: []string{"Shared"}, Action: join,
			Actions: []sdui.ItemAction{{ID: "join", Label: "Join", Glyph: "a", Action: join}}})
	}
	return out, nil
}

// ownTable is the game this profile is playing now, as its host or in a seat.
func (q *sduiAsk) ownTable() (seatTableView, bool) {
	for _, t := range seats.tablesNow() {
		if t.Host == q.id.User || slicesHas(t.Holders, q.id.User) {
			return t, true
		}
	}
	return seatTableView{}, false
}

// room: this profile's room (a view), then each player as a person: the host, "Player n", or
// "Waiting…" for a free seat. The room's code, which lets anyone join, is the host's alone to see.
func (q *sduiAsk) room() []sdui.Item {
	t, ok := q.ownTable()
	if !ok {
		return nil
	}
	room := sdui.Item{ID: t.Session, Kind: "view", Title: t.Game.Title, Subtitle: roomWords(t.HostName), Icon: "users", Badges: []string{"Shared"}, Count: seatsTaken(t)}
	if t.Host == q.id.User {
		if v, ok := seats.view(t.Session, true); ok && v.Code != "" {
			room.Tagline = v.Code
			room.Route = "/games?join=" + v.Code
			room.Link = room.Route
		}
		if t.Open {
			room.Badges = append(room.Badges, "Open")
		}
		room.Actions = q.roomActions(t)
	} else {
		// Someone who joined may leave; getting back into the game is their player's own.
		room.Actions = []sdui.ItemAction{
			{ID: "leave", Label: "Leave", Glyph: "b", Action: &sdui.Action{Type: core.ActCommand, Op: "wittyplay.room.leave", Item: t.Session,
				Params: map[string]any{"user": strconv.FormatInt(q.id.User, 10)}, Confirm: "Leave this game?"}},
		}
	}
	out := []sdui.Item{room}
	players := max(t.Game.Players, 1)
	for n := 0; n < players; n++ {
		it := sdui.Item{ID: fmt.Sprintf("%s-p%d", t.Session, n+1), Kind: "person", Subtitle: fmt.Sprintf("Player %d", n+1), Badges: []string{fmt.Sprintf("P%d", n+1)}, Icon: "user"}
		switch {
		case n == 0:
			it.Title, it.Subtitle = t.HostName, "Host"
		default:
			if at := slices.IndexFunc(t.Holders, func(h seatHolder) bool { return h.Seat == n }); at >= 0 {
				it.Title = t.Holders[at].Name
			} else {
				it.Title, it.Subtitle, it.Badges = "Waiting…", "Free", []string{"Free"}
			}
		}
		out = append(out, it)
	}
	return out
}

// roomActions are the host's buttons on its room: back into the game (playing it again rejoins the
// session it has running), a code to invite someone with, and opening the room to friends or closing
// it. A Kids or guest profile's room can't be opened, so it has no such button.
func (q *sduiAsk) roomActions(t seatTableView) []sdui.ItemAction {
	out := []sdui.ItemAction{
		{ID: "play", Label: "Play", Glyph: "a", Action: &sdui.Action{Type: core.ActPlay, Item: strconv.FormatInt(t.Game.Item, 10)}},
		{ID: "invite", Label: "Invite", Glyph: "x", Action: &sdui.Action{Type: core.ActCommand, Op: "wittyplay.room.invite", Item: t.Session}},
	}
	switch {
	case q.id.Kind == "kids" || q.id.Kind == "guest":
	case t.Open:
		out = append(out, sdui.ItemAction{ID: "close", Label: "Close the room", Glyph: "y", Action: &sdui.Action{Type: core.ActCommand, Op: "wittyplay.room.close", Item: t.Session}})
	default:
		out = append(out, sdui.ItemAction{ID: "open", Label: "Open to friends", Glyph: "y", Action: &sdui.Action{Type: core.ActCommand, Op: "wittyplay.room.create", Item: t.Session}})
	}
	return out
}

// saveStates: one game's saves, a person's own first, newest first.
func (q *sduiAsk) saveStates() []sdui.Item {
	id, err := strconv.ParseInt(srcStr(q.req.Source, "item"), 10, 64)
	if err != nil || !q.mayOpen(id) {
		return nil
	}
	saves := listSaves(q.a.saveDir(q.id.User, id))
	own := func(s saveSlot) bool { return s.Slot >= 1 && s.Slot <= stream.MaxSlot }
	sort.SliceStable(saves, func(i, j int) bool {
		if own(saves[i]) != own(saves[j]) {
			return own(saves[i])
		}
		return saves[i].UpdatedAt > saves[j].UpdatedAt
	})
	out := make([]sdui.Item, 0, len(saves))
	for _, sv := range saves {
		it := sdui.Item{ID: fmt.Sprintf("slot-%d", sv.Slot), Kind: "view", Title: slotTitle(sv.Slot), Subtitle: savedWords(sv.UpdatedAt), Icon: "history",
			Route: fmt.Sprintf("/game/%d", id), Badges: []string{"Save"}}
		if sv.HasThumb {
			it.Images = map[string]string{"backdrop": saveThumb(id, sv.Slot)}
		}
		out = append(out, it)
	}
	return out
}

// memories: this profile's own saves and kept screenshots, in every game it may still see, newest
// first.
func (q *sduiAsk) memories() ([]sdui.Item, error) {
	entries, err := os.ReadDir(filepath.Dir(q.a.saveDir(q.id.User, 0)))
	if err != nil {
		return nil, nil
	}
	var ids []int64
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "i") {
			continue
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(e.Name(), "i"), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	seen, err := q.gamesByID(ids)
	if err != nil {
		return nil, err
	}
	type memory struct {
		it sdui.Item
		at int64
	}
	var all []memory
	for _, id := range ids {
		g, ok := seen[id]
		if !ok {
			continue
		}
		for _, sv := range listSaves(q.a.saveDir(q.id.User, id)) {
			if sv.Slot < 1 || sv.Slot > stream.MaxSlot {
				continue
			}
			it := sdui.Item{ID: fmt.Sprintf("save-%d-%d", id, sv.Slot), Kind: "view", Title: g.Title, Subtitle: slotTitle(sv.Slot) + " · " + savedWords(sv.UpdatedAt),
				Badges: []string{"Save"}, Icon: "history", Route: fmt.Sprintf("/game/%d", id)}
			if sv.HasThumb {
				it.Images = map[string]string{"backdrop": saveThumb(id, sv.Slot)}
			}
			all = append(all, memory{it, sv.UpdatedAt})
		}
		for _, sh := range listShots(q.a.shotsDir(q.id.User, id)) {
			all = append(all, memory{sdui.Item{ID: fmt.Sprintf("shot-%d-%s", id, strings.TrimSuffix(sh.Name, ".png")), Kind: "photo", Title: g.Title,
				Subtitle: "Screenshot · " + savedWords(sh.Taken), Badges: []string{"Shot"},
				Action: &sdui.Action{Type: core.ActNavigate, To: fmt.Sprintf("/page/wittyplay-memory?item=%d&name=%s", id, url.QueryEscape(sh.Name)), As: core.PresentLayer},
				Images: map[string]string{"backdrop": shotPicture(id, sh.Name)}}, sh.Taken})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at > all[j].at })
	out := make([]sdui.Item, len(all))
	for i, m := range all {
		out[i] = m.it
	}
	return out, nil
}

// controllers: each player of the game this profile is in, and who or what is playing it. A
// controller's battery and kind are the device's to say: the module doesn't see them.
func (q *sduiAsk) controllers() []sdui.Item {
	t, ok := q.ownTable()
	if !ok {
		return nil
	}
	device := ""
	if v, ok := seats.view(t.Session, false); ok {
		device = v.Host.Device
	}
	players := max(t.Game.Players, 1)
	out := make([]sdui.Item, 0, players)
	for n := 0; n < players; n++ {
		it := sdui.Item{ID: fmt.Sprintf("player-%d", n+1), Kind: "view", Title: fmt.Sprintf("Player %d", n+1), Icon: "devices", Badges: []string{fmt.Sprintf("P%d", n+1)}}
		switch at := slices.IndexFunc(t.Holders, func(h seatHolder) bool { return h.Seat == n }); {
		case n == 0:
			it.Subtitle = t.HostName
			if device != "" {
				it.Subtitle += " · " + device
			}
		case at >= 0:
			it.Subtitle = t.Holders[at].Name
		default:
			it.Subtitle, it.Badges = "No controller", append(it.Badges, "Free")
		}
		out = append(out, it)
	}
	return out
}

// privacy: the two choices a profile makes about what its friends know, as switches in a Form
// (wittyplay.online.set saves both). A guest has no friends, so both are off and say why.
func (q *sduiAsk) privacy() []sdui.Item {
	p := q.a.presenceOf(q.ctx, q.id.User, q.id.Kind)
	guest := q.id.Kind == "guest"
	why := ""
	if guest {
		why = "A guest has no friends to share with."
	}
	return []sdui.Item{
		{ID: "sharePlaying", Kind: "view", Title: "Show friends what I’m playing", Subtitle: "Friends see “Playing Star Tide”. Off shows you as Online only.", Icon: "game",
			Control: &sdui.Control{Type: core.ControlSwitch, Name: "sharePlaying", Value: p.Playing && !guest, Disabled: guest, Why: why}},
		{ID: "appearOnline", Kind: "view", Title: "Appear online", Subtitle: "Off shows you as offline to everyone, playing or not.", Icon: "users",
			Control: &sdui.Control{Type: core.ControlSwitch, Name: "appearOnline", Value: p.Online && !guest, Disabled: guest, Why: why}},
	}
}

// kidsFriendRows: for an administrator, every profile that could be a Kids profile's friend, each a
// switch whose action saves the whole list with that one changed (wittyplay.kids.friends). Nothing
// for anyone else, or when the profile isn't a Kids profile.
func (q *sduiAsk) kidsFriendRows() ([]sdui.Item, error) {
	if !q.id.Admin {
		return nil, nil
	}
	id, err := strconv.ParseInt(srcStr(q.req.Source, "user"), 10, 64)
	if err != nil {
		return nil, nil
	}
	all, err := q.a.Host.Users(q.ctx)
	if err != nil {
		return nil, err
	}
	at := slices.IndexFunc(all, func(u host.User) bool { return u.ID == id })
	if at < 0 || all[at].Kind != "kids" {
		return nil, nil
	}
	kid := all[at]
	approved, err := q.a.kidsFriends(q.ctx, kid.ID)
	if err != nil {
		return nil, err
	}
	var out []sdui.Item
	for _, o := range all {
		if o.ID == kid.ID || o.Kind == "guest" {
			continue
		}
		on := slices.Contains(approved, o.ID)
		next := []int64{}
		for _, a := range approved {
			if a != o.ID {
				next = append(next, a)
			}
		}
		if !on {
			next = append(next, o.ID)
		}
		trailing := "Not approved"
		if on {
			trailing = "Approved"
		}
		out = append(out, sdui.Item{ID: "profile-" + strconv.FormatInt(o.ID, 10), Kind: "view", Title: profileName(&o), Icon: "user", Trailing: trailing,
			Action:  &sdui.Action{Type: core.ActCommand, Op: "wittyplay.kids.friends", Item: strconv.FormatInt(kid.ID, 10), Params: map[string]any{"friends": next}},
			Control: &sdui.Control{Type: core.ControlSwitch, Value: on}})
	}
	return out, nil
}
