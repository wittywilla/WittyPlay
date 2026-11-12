// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package ui is WittyPlay's server-driven UI (module API 4): what it adds to every app, as data
// the server checks, merges and renders (WittyMediaServer docs/SDUI-MODULES.md). sdui.json at
// the repository's root is written from UI and held equal to it by a test.
//
// The games pages themselves are the server's own (Games, each games library, a game's page),
// drawn from the library. What WittyPlay adds is the one thing only it knows: which consoles
// this server plays, and how (a core installed, or a standalone emulator set up). It's a row
// on the Games page, a page of its own, a section for the page editor, and its settings.
package ui

import (
	"github.com/wittywilla/wittymodule/sdui"
)

// Module is WittyPlay's ID.
const Module = "wittyplay"

// The source and its settings, by the names the helper answers (rpc/sdui.go).
const (
	SourceConsoles = Module + ".consoles"
	// SettingShowConsoles, for the server: the row on the Games page is shown.
	SettingShowConsoles = "showConsoles"
	// SettingOnlyPlayable, for each profile: list only the consoles that play here.
	SettingOnlyPlayable = "onlyPlayable"
)

// WittyPlay Online's sources (WittyMediaServer docs/plans/DESIGN-2-SDUI-GAPS.md, section 6).
// Friends and a room's players are people: person cards, round, with a line and a badge.
// They're about this server's profiles, saves and running games, so the server answers them
// (its wittyplay_online.go) through each profile's limits; they're declared here so that pages,
// editors and the checks know them.
const (
	SourceRecent      = Module + ".recent"
	SourceCollections = Module + ".collections"
	SourceFriends     = Module + ".friends"
	SourceRooms       = Module + ".rooms"
	SourceRoom        = Module + ".room"
	SourceSaveStates  = Module + ".saveStates"
	SourceMemories    = Module + ".memories"
	SourceControllers = Module + ".controllers"
	// Answered by the server for its Forms: the profile's two choices as switches, and an
	// administrator's choice of a Kids profile's friends (user: the profile's ID).
	SourcePrivacy     = Module + ".privacy"
	SourceKidsFriends = Module + ".kidsFriends"
)

// onlineSources are WittyPlay Online's sources, as the catalogue describes them.
func onlineSources() []sdui.SourceSpec {
	limit := func(def, most int) sdui.PropSpec {
		return sdui.PropSpec{Name: "limit", Type: "int", Label: "At most", Default: def, Min: sdui.Int(1), Max: sdui.Int(most)}
	}
	return []sdui.SourceSpec{
		{Type: SourceRecent, Label: "Games you played", Icon: "history", Title: "Recent",
			Description: "The games this profile played, newest first, each with its era, how many can play, how long it has been played and its latest save.",
			Params:      []sdui.PropSpec{limit(20, 60)}, Yields: []string{"game"}, CardStyle: "game"},
		{Type: SourceCollections, Label: "Classics by era", Icon: "game", Title: "Classics",
			Description: "The games this profile may play, by era, with how many each has. With an era, that era's games.",
			Params: []sdui.PropSpec{{Name: "era", Type: "enum", Label: "Era", Default: "",
				Options: []sdui.EnumOption{{Value: "", Label: "Every era, as collections"}, {Value: "first", Label: "First era"}, {Value: "8-bit", Label: "8-bit"},
					{Value: "16-bit", Label: "16-bit"}, {Value: "early-3d", Label: "3D era"}, {Value: "disc", Label: "Disc era"}, {Value: "pocket", Label: "Handheld"}}},
				limit(40, 500)},
			Yields: []string{"collection", "game"}, CardStyle: "tile", Paged: true},
		{Type: SourceFriends, Label: "Friends", Icon: "users", Title: "Friends",
			Description: "The other profiles of this server this profile may see, playing first, with what each is doing as far as it shares. A Kids profile sees only the friends an administrator chose.",
			Params:      []sdui.PropSpec{limit(20, 100)}, Yields: []string{"person"}, CardStyle: "person"},
		{Type: SourceRooms, Label: "Open rooms", Icon: "users", Title: "Open rooms",
			Description: "Friends' games on this server that are open to join, with a seat free, of games this profile may play. None for a Kids profile.",
			Params:      []sdui.PropSpec{limit(12, 50)}, Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceRoom, Label: "Your room", Icon: "users", Title: "Your room",
			Description: "The game this profile is playing with others: the room first (a view: its code for the host), then each player as a person, or Waiting… for a free seat.",
			Params:      []sdui.PropSpec{limit(9, 9)}, Yields: []string{"view", "person"}, CardStyle: "person"},
		{Type: SourceSaveStates, Label: "Save states", Icon: "history", Title: "Save states",
			Description: "One game's saves for this profile, its own first, newest first.", PageKinds: []string{"detail"},
			Params: []sdui.PropSpec{{Name: "item", Type: "ref", Label: "Game", Required: true, Help: "$item on a game's page."}, limit(20, 110)},
			Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceMemories, Label: "Memories", Icon: "photo", Title: "Memories",
			Description: "This profile's save states and kept screenshots, across its games, newest first.",
			Params:      []sdui.PropSpec{limit(24, 500)}, Yields: []string{"view", "photo"}, CardStyle: "landscape", Paged: true},
		{Type: SourcePrivacy, Label: "Friends and privacy", Icon: "users", Title: "Friends and what you play",
			Description: "The two choices a profile makes about what its friends know, as switches: whether friends see what it plays, and whether it appears online at all.",
			Yields:      []string{"view"}, CardStyle: "tile"},
		{Type: SourceKidsFriends, Label: "A Kids profile's friends", Icon: "kids", Title: "Approved friends",
			Description: "For an administrator: every profile that could be a Kids profile's friend, each a switch that approves or removes it. Nothing for anyone else.",
			Params:      []sdui.PropSpec{{Name: "user", Type: "ref", Label: "Kids profile", Help: "The profile's ID, $item on a page opened about it.", Required: true}},
			Yields:      []string{"view"}, CardStyle: "tile"},
		{Type: SourceControllers, Label: "Controllers", Icon: "devices", Title: "Controllers",
			Description: "Each player of the game this profile is in, and who is playing it. A controller's battery and kind are the device's to show.",
			Params:      []sdui.PropSpec{limit(8, 8)}, Yields: []string{"view"}, CardStyle: "landscape"},
	}
}

// UI is WittyPlay's SDUI.
func UI() *sdui.Module {
	m := sdui.New(Module, "Games").Colours(map[string]string{"accent": "#7F46B8"})
	m.AddSource(sdui.SourceSpec{
		Type:        SourceConsoles,
		Label:       "Consoles this server plays",
		Description: "Every console, whether its games play on this server, and how: a core, or a standalone emulator.",
		Icon:        "game",
		Params: []sdui.PropSpec{
			{Name: "only", Type: "enum", Label: "Which consoles", Default: "all",
				Options: []sdui.EnumOption{{Value: "all", Label: "Every console"}, {Value: "ready", Label: "Those that play here"}}},
			{Name: "limit", Type: "int", Label: "At most", Default: 40, Min: sdui.Int(1), Max: sdui.Int(60)},
		},
		Yields:    []string{"view"},
		CardStyle: "tile",
		SeeAll:    "/page/" + Module,
		Title:     "What this server plays",
	})
	for _, src := range onlineSources() {
		m.AddSource(src)
	}
	shelf := sdui.Shelf(Module+"-consoles", "What this server plays", sdui.Src(SourceConsoles, "only", "ready", "limit", 40))
	m.AddInsert("games", "playing", shelf)
	m.AddPage(sdui.Page{ID: Module, Kind: "custom", Title: "What this server plays", Icon: "game",
		Description: "Every console, whether its games play on this server, and how.",
		Sections: []sdui.Section{
			sdui.Grid("consoles", "", sdui.Src(SourceConsoles, "only", "all", "limit", 60)),
		}})
	addOnlinePages(m)
	m.AddPreset(sdui.ModulePreset{ID: Module + "-consoles", Label: "Consoles this server plays",
		Description: "A row of the consoles whose games play here.", Section: shelf})
	m.AddSettings(sdui.SettingsPage{ID: Module, Title: "Games", Icon: "game", Groups: []sdui.SettingsGroup{
		{Title: "Games page", Rows: []sdui.SettingRow{
			{Key: SettingShowConsoles, Kind: "bool", Label: "Show which consoles play here", Default: true,
				Help: "A row on the Games page, for everyone, of the consoles this server plays."},
		}},
		{Title: "For you", Footer: "Each profile chooses its own.", Rows: []sdui.SettingRow{
			{Key: SettingOnlyPlayable, Kind: "bool", Scope: "profile", Label: "Only consoles that play here", Default: false,
				Help: "On the page of every console, leave out those this server can't play yet."},
		}},
		{Rows: []sdui.SettingRow{
			{Kind: "info", Label: "Setting up consoles", Help: "Consoles, cores and standalone emulators are set up in Settings, Consoles & cores and Standalone emulators."},
		}},
	}})
	return m
}

// WittyPlay Online's pages (Design 2: docs/design-2/canvas/Play.dc.html in WittyMediaServer, and
// the boards in docs/design-2/images/play and gaps/GB2_7). All of it is the catalogue's own
// components over the sources above, so every app draws it; what only a device knows (its
// controllers' batteries, "press any button") the app adds itself.
const (
	PageOnline      = Module + "-online"
	PageControllers = Module + "-controllers"
	PageQuick       = Module + "-quick"
	PageRoom        = Module + "-room"
	PageMemory      = Module + "-memory"
	PageKeep        = Module + "-keep"
	PagePrivacy     = Module + "-privacy"
	PageKids        = Module + "-kids"
)

// onlineRegions is what WittyPlay Online puts into the shell while its pages show: the profile and
// the server in the status bar, and the round dock of its destinations.
func onlineRegions() *sdui.PageRegions {
	return &sdui.PageRegions{
		StatusBar: []sdui.StatusItem{{ID: "me", Kind: "profile", Text: "WittyPlay Online"}, {ID: "server", Kind: "server"}},
		SystemDock: []sdui.DockEntry{
			{ID: "search", Label: "Search", Icon: "search", Route: "/search"},
			{ID: "together", Label: "Together", Icon: "users", Route: "/page/" + PageOnline + "?tab=together"},
			{ID: "controllers", Label: "Controllers", Icon: "devices", Route: "/page/" + PageControllers, As: "layer"},
			{ID: "memories", Label: "Memories", Icon: "photo", Route: "/page/" + PageOnline + "?tab=memories"},
			{ID: "settings", Label: "Settings", Icon: "settings", Route: "/page/" + PagePrivacy, As: "layer"},
		},
	}
}

func addOnlinePages(m *sdui.Module) {
	page := func(id, title, icon string, sections ...sdui.Section) sdui.Page {
		return sdui.Page{ID: id, Kind: "custom", Title: title, Icon: icon, Scope: Module, Sections: sections}
	}
	tab := func(id, label, icon string, children ...sdui.Section) sdui.Section {
		return sdui.Sec(id, "Tab", sdui.Props("label", label, "icon", icon), nil, children...)
	}
	list := func(id, title string, src sdui.Source, kv ...any) sdui.Section {
		return sdui.Sec(id, "List", sdui.Props(append([]any{"title", title}, kv...)...), src)
	}
	legend := sdui.Sec("dock", "Dock", sdui.Props("legend", true), nil)

	// Home: the row of recent games, whose focused tile grows with its details under it, then the
	// classics by era, friends and rooms, memories, and what this server plays.
	home := page(PageOnline, "WittyPlay Online", "game",
		sdui.Sec("tabs", "Tabs", sdui.Props("style", "tabs", "default", "recent"), nil,
			tab("recent", "Recent", "history",
				sdui.Sec("recent-row", "Shelf", sdui.Props("title", "", "focusGrow", true, "detail", true, "cardSize", "l", "seeAll", "never",
					"empty", "message", "emptyText", "Games you play show up here."), sdui.Src(SourceRecent, "limit", 20))),
			tab("classics", "Classics", "game",
				sdui.Sec("eras", "Grid", sdui.Props("title", "Classics by era"), sdui.Src(SourceCollections))),
			tab("together", "Together", "users",
				sdui.Sec("lobby", "Split", sdui.Props("ratio", "1:2", "stackBelow", "expanded"), nil,
					list("friends", "Friends", sdui.Src(SourceFriends), "focusable", true, "empty", "message", "emptyText", "No friends to show yet."),
					sdui.Sec("rooms-side", "Stack", sdui.Props("gap", "l"), nil,
						list("room", "Your room", sdui.Src(SourceRoom), "focusable", true),
						sdui.Sec("rooms", "Grid", sdui.Props("title", "Open rooms", "focusable", true, "empty", "message",
							"emptyText", "No friend has a room open."), sdui.Src(SourceRooms))))),
			tab("memories", "Memories", "photo",
				sdui.Sec("memories-grid", "Grid", sdui.Props("title", "This week", "empty", "message", "emptyText", "Save states and screenshots you keep show up here."),
					sdui.Src(SourceMemories, "limit", 60))),
			tab("discover", "Discover", "sparkles",
				sdui.Sec("consoles-row", "Shelf", sdui.Props("title", "What this server plays"), sdui.Src(SourceConsoles, "only", "ready", "limit", 40)))),
		legend)
	home.Description = "WittyPlay Online: recent games, classics by era, friends and rooms, memories."
	home.Regions = onlineRegions()
	m.AddPage(home)

	// Controllers: each player and who holds it; the device adds its own pads' batteries.
	pads := page(PageControllers, "Controllers", "devices",
		list("players", "Players", sdui.Src(SourceControllers), "focusable", true),
		sdui.Notice("phone", "info", "Use a phone as a controller: open your room's join link on it, or scan its code from Your room."),
		legend)
	pads.Regions = onlineRegions()
	m.AddPage(pads)

	// The in-game menu, shown as a layer over the game. Rewind and the screen and sound panel are the
	// player's own; these are the places the server knows.
	m.AddPage(page(PageQuick, "Menu", "game",
		sdui.Sec("items", "List", sdui.Props("title", ""), nil,
			sdui.Sec("resume", "ListItem", sdui.Props("title", "Resume", "icon", "play", "back", true), nil),
			sdui.Sec("room", "ListItem", sdui.Props("title", "Invite a friend", "subtitle", "Your room, its code and its players", "icon", "users", "to", "/page/"+PageRoom), nil),
			sdui.Sec("pads", "ListItem", sdui.Props("title", "Controllers", "icon", "devices", "to", "/page/"+PageControllers), nil),
			sdui.Sec("memories", "ListItem", sdui.Props("title", "Memories", "subtitle", "Save states and screenshots", "icon", "photo", "to", "/page/"+PageOnline+"?tab=memories"), nil)),
		legend))

	// Your room (roomopen): the room with its buttons (from the server: Play, Invite, Open or Close),
	// then its players.
	m.AddPage(page(PageRoom, "Your room", "users",
		list("room", "Room", sdui.Src(SourceRoom), "focusable", true, "empty", "message", "emptyText", "You're not in a game with others. Start a game, then open your room from here."),
		sdui.Notice("join", "info", "A phone joins a seat from your room's join link and becomes a controller."),
		legend))

	// A kept screenshot (opened about a game, item, and the screenshot's name), with deleting it.
	m.AddPage(page(PageMemory, "Memory", "photo",
		sdui.Sec("actions", "List", sdui.Props("title", "{title}"), nil,
			sdui.Sec("delete", "ListItem", sdui.Props("title", "Delete this memory", "icon", "warning", "op", "wittyplay.memory.delete", "item", "$item",
				"params", map[string]any{"name": "$name"}, "confirm", "Delete this memory?"), nil)),
		legend))

	// Keep this screenshot (opened about the game being played: item is its session).
	m.AddPage(page(PageKeep, "Keep this screenshot", "photo",
		sdui.Sec("choice", "List", sdui.Props("title", ""), nil,
			sdui.Sec("keep", "ListItem", sdui.Props("title", "Keep", "subtitle", "Saved to Memories with the game and the time.", "icon", "check", "op", "wittyplay.screenshot.keep", "item", "$item"), nil),
			sdui.Sec("not-now", "ListItem", sdui.Props("title", "Not now", "back", true), nil)),
		sdui.Shelf("memories", "In Memories", sdui.Src(SourceMemories, "limit", 8)),
		legend))

	// Friends and what you play (gamesprivacy): the profile's two choices, saved together.
	m.AddPage(page(PagePrivacy, "Friends and what you play", "users",
		sdui.Sec("privacy", "Form", sdui.Props("title", "Privacy", "op", "wittyplay.online.set", "submitLabel", "Save"), nil,
			sdui.Sec("rows", "List", sdui.Props("title", ""), sdui.Src(SourcePrivacy)),
			sdui.Notice("kids", "info", "Friends are other profiles on this server. A Kids profile only sees friends an administrator has approved, and is only seen by them."))))

	// A Kids profile's friends (gameskids), for administrators: opened about the profile (item).
	m.AddPage(page(PageKids, "Friends", "kids",
		sdui.Sec("approved", "List", sdui.Props("title", "Approved"), sdui.Src(SourceKidsFriends, "user", "$item")),
		sdui.Notice("rooms", "info", "A Kids profile gets no rooms. Friends here are the only people it sees in WittyPlay Online.")))

	m.AddNav(sdui.NavItem{ID: PageOnline, Type: "page", Page: PageOnline, Label: "WittyPlay Online", Icon: "game",
		Placement: map[string]string{"phone": "more", "tablet": "more", "desktop": "more", "web": "more", "headset": "more"}})
}
