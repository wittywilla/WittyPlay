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
	"bytes"
	"encoding/json"

	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
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

// The library and the store, for the apps that draw WittyPlay's screens themselves (the Apple TV
// and Android TV apps, and the phone apps): answered by package api (sdui_store.go).
const (
	SourceLibrary       = Module + ".library"
	SourceStoreConsoles = Module + ".storeConsoles"
	SourceStoreEmus     = Module + ".storeEmulators"
)

// tvSources are the library's and the store's sources, as the catalogue describes them.
func tvSources() []sdui.SourceSpec {
	return []sdui.SourceSpec{
		{Type: SourceLibrary, Label: "Games by console", Icon: "game", Title: "Games",
			Description: "With a console, that console's games by title, in one games library ($library) or every one. Without, the consoles that have games there, each with how many, opening the library that holds them.",
			Params: []sdui.PropSpec{
				{Name: "library", Type: "library", Label: "Library", Help: "$library on a games library's page; empty for every games library."},
				{Name: "console", Type: "string", Label: "Console", Help: "A console's ID, such as snes. Empty: the consoles themselves.", MaxLength: 40},
				{Name: "limit", Type: "int", Label: "At most", Default: 40, Min: sdui.Int(1), Max: sdui.Int(500)},
			},
			Yields: []string{"game", "view"}, CardStyle: "game", Paged: true},
		{Type: SourceStoreConsoles, Label: "Cores & emulators: consoles", Icon: "disc", Title: "Consoles & cores",
			Description: "For an administrator: a row for each console, whether it plays here and on what, opening its own rows. With a console: the core it plays on (a choice), each of its cores to install (its licence accepted), update or remove, with a download's progress, and its BIOS files. Nothing for anyone else.",
			Params:      []sdui.PropSpec{{Name: "console", Type: "string", Label: "Console", Help: "A console's ID, $item on a page opened about it. Empty: every console.", MaxLength: 64}},
			Yields:      []string{"view"}, CardStyle: "tile"},
		{Type: SourceStoreEmus, Label: "Cores & emulators: standalone emulators", Icon: "devices", Title: "Standalone emulators",
			Description: "For an administrator: each standalone emulator, its consoles, licence and firmware, and rows to accept its licence and install it, update it, stop a download or remove it. Nothing for anyone else.",
			Yields:      []string{"view"}, CardStyle: "tile"},
	}
}

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
	for _, src := range tvSources() {
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
	// The apps' own versions of WittyPlay's web pages (the web app keeps its React pages for these):
	// every games library's page, Settings, and Cores & emulators with each console's rows.
	PageLibrary      = Module + "-library"
	PageSettings     = Module + "-settings"
	PageStore        = Module + "-store"
	PageStoreConsole = Module + "-store-console"
)

// onlyInApps: shown by the apps alone; the web app has its own (its Consoles and All games tabs link
// to its own pages).
var onlyInApps = &sdui.Condition{Not: &sdui.Condition{Platform: []string{"web", "desktop"}}}

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
			// WittyPlay's Settings, which every app draws (the web app opens its own, /wittyplay/settings).
			{ID: "settings", Label: "Settings", Icon: "settings", Route: "/page/" + PageSettings},
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
					"empty", "message", "emptyText", "Nothing played yet. Pick a game from Classics.",
					"emptyAction", map[string]any{"label": "Open Classics", "to": "/page/" + PageOnline + "?tab=classics"}), sdui.Src(SourceRecent, "limit", 20))),
			withWhen(tab("consoles", "Consoles", "game",
				sdui.Sec("consoles-grid", "Grid", sdui.Props("title", "Consoles with games", "cardStyle", "tile", "empty", "message", "emptyText", "No games in your libraries yet.",
					"emptyHint", "An administrator adds a games library in Settings; its games appear after a scan.",
					"emptyAction", map[string]any{"label": "Add a games library", "native": "settings", "when": map[string]any{"admin": true}}), sdui.Src(SourceLibrary, "limit", 60))), onlyInApps),
			withWhen(tab("all", "All games", "library",
				sdui.Sec("libraries-grid", "Grid", sdui.Props("title", "Games libraries", "empty", "message", "emptyText", "No games library here yet.",
					"emptyHint", "An administrator adds a games library in Settings; its games appear after a scan.",
					"emptyAction", map[string]any{"label": "Add a games library", "native": "settings", "when": map[string]any{"admin": true}}), sdui.Src("libraries", "kind", "games"))), onlyInApps),
			tab("classics", "Classics", "game",
				sdui.Sec("eras", "Grid", sdui.Props("title", "Classics by era", "empty", "message", "emptyText", "No games in your libraries yet.",
					"emptyHint", "An administrator adds a games library in Settings; its games appear after a scan.",
					"emptyAction", map[string]any{"label": "Add a games library", "native": "settings", "when": map[string]any{"admin": true}}), sdui.Src(SourceCollections))),
			tab("together", "Together", "users",
				sdui.Sec("lobby", "Split", sdui.Props("ratio", "1:2", "stackBelow", "expanded"), nil,
					list("friends", "Friends", sdui.Src(SourceFriends), "focusable", true, "empty", "message", "emptyText", "No friends are online right now."),
					sdui.Sec("rooms-side", "Stack", sdui.Props("gap", "l"), nil,
						list("room", "Your room", sdui.Src(SourceRoom), "focusable", true, "empty", "message", "emptyText", "Start a game, then open your room to friends."),
						sdui.Sec("rooms", "Grid", sdui.Props("title", "Open rooms", "focusable", true, "empty", "message",
							"emptyText", "No friend has a room open."), sdui.Src(SourceRooms))))),
			tab("memories", "Memories", "photo",
				sdui.Sec("memories-grid", "Grid", sdui.Props("title", "This week", "empty", "message", "emptyText", "Saves and screenshots you keep appear here."),
					sdui.Src(SourceMemories, "limit", 60))),
			tab("discover", "Discover", "sparkles",
				sdui.Sec("consoles-row", "Shelf", sdui.Props("title", "What this server plays", "empty", "message", "emptyText", "No console plays here yet. An administrator sets them up in Settings."), sdui.Src(SourceConsoles, "only", "ready", "limit", 40)))),
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
	addTVPages(m, page, tab, list, legend)

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
		// The join link as a QR code, for the host only: the room's link is the host's alone, so for
		// anyone else this section isn't drawn.
		sdui.Sec("join-code", "QR", sdui.Props("label", "Scan to join as the next player", "size", "m"), sdui.Src(SourceRoom, "limit", 1)),
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

	// Friends and what you play (gamesprivacy): the profile's two choices, saved together. It's also
	// in You, Games.
	privacy := page(PagePrivacy, "Friends and what you play", "users",
		sdui.Sec("privacy", "Form", sdui.Props("title", "Privacy", "op", "wittyplay.online.set", "submitLabel", "Save"), nil,
			sdui.Sec("rows", "List", sdui.Props("title", ""), sdui.Src(SourcePrivacy)),
			sdui.Notice("kids", "info", "Friends are other profiles on this server. A Kids profile only sees friends an administrator has approved, and is only seen by them.")))
	privacy.Slot = "you.games"
	m.AddPage(privacy)

	// A Kids profile's friends (gameskids), for administrators: in Settings, People, opened about the
	// profile (item).
	kids := page(PageKids, "Friends", "kids",
		sdui.Sec("approved", "List", sdui.Props("title", "Approved"), sdui.Src(SourceKidsFriends, "user", "$item")),
		sdui.Notice("rooms", "info", "A Kids profile gets no rooms. Friends here are the only people it sees in WittyPlay Online."))
	kids.Slot = "people.profile"
	m.AddPage(kids)

	m.AddNav(sdui.NavItem{ID: PageOnline, Type: "page", Page: PageOnline, Label: "Games", Icon: "game",
		Placement: map[string]string{"phone": "primary", "tablet": "primary", "desktop": "more", "web": "more", "headset": "more"}})
}

func withWhen(s sdui.Section, when *sdui.Condition) sdui.Section { s.When = when; return s }

// libraryShelves are the consoles a games library's page has a shelf for, in the order of their
// eras (home consoles, then handhelds), each by the name people call it. A console with no games in
// the library has no shelf.
var libraryShelves = []struct{ id, name string }{
	{"a2600", "Atari 2600"}, {"a7800", "Atari 7800"}, {"nes", "NES"}, {"sms", "Master System"}, {"snes", "Super Nintendo"}, {"md", "Mega Drive"},
	{"pce", "PC Engine"}, {"n64", "Nintendo 64"}, {"ps1", "PlayStation"}, {"dc", "Dreamcast"}, {"ps2", "PlayStation 2"}, {"gc", "GameCube"},
	{"wii", "Wii"}, {"xbox", "Xbox"}, {"ps3", "PlayStation 3"}, {"wiiu", "Wii U"}, {"switch", "Switch"}, {"switch2", "Switch 2"},
	{"gb", "Game Boy"}, {"gg", "Game Gear"}, {"lynx", "Lynx"}, {"ngp", "Neo Geo Pocket"}, {"ws", "WonderSwan"}, {"vb", "Virtual Boy"},
	{"gba", "Game Boy Advance"}, {"nds", "Nintendo DS"}, {"psp", "PSP"}, {"3ds", "Nintendo 3DS"},
}

// addTVPages adds what the Apple TV and Android TV apps (and the phone apps) draw where the web app
// has WittyPlay's React pages: a games library, Settings, and Cores & emulators for administrators.
// Each ends with the dock and its key legend, and every row a remote can act on is a focus stop.
func addTVPages(m *sdui.Module, page func(id, title, icon string, sections ...sdui.Section) sdui.Page,
	tab func(id, label, icon string, children ...sdui.Section) sdui.Section,
	list func(id, title string, src sdui.Source, kv ...any) sdui.Section, legend sdui.Section) {
	admin, notAdmin := true, false
	forAdmins, forOthers := &sdui.Condition{Admin: &admin}, &sdui.Condition{Admin: &notAdmin}

	// A games library: what was being played, a shelf for each console, and every game with a filter bar.
	shelves := make([]sdui.Section, 0, len(libraryShelves))
	for _, c := range libraryShelves {
		shelves = append(shelves, sdui.Sec("c-"+c.id, "Shelf", sdui.Props("title", c.name, "cardStyle", "game", "seeAll", "never"),
			sdui.Src(SourceLibrary, "library", "$library", "console", c.id, "limit", 60)))
	}
	lib := sdui.Page{ID: PageLibrary, Kind: "library", Title: "{library}", Icon: "game", Scope: Module, Match: &sdui.Match{LibraryKind: "games"},
		Description: "A games library on every app: what you were playing, a shelf for each console, and every game with filters. The web app shows its own (/wittyplay/library).",
		Sections: []sdui.Section{
			sdui.Sec("playing", "Shelf", sdui.Props("title", "Continue playing", "cardStyle", "game"), sdui.Src("continuePlaying", "library", "$library")),
			sdui.Sec("views", "Tabs", sdui.Props("style", "segments", "default", "consoles"), nil,
				tab("consoles", "By console", "game", shelves...),
				tab("all", "All games", "grid",
					sdui.Sec("filters", "FilterBar", sdui.Props("target", "all-games", "fields", []string{"genre", "decade"}, "sorts", []string{"title", "added", "newest", "rating"}), nil),
					sdui.Sec("all-games", "Grid", sdui.Props("cardStyle", "game", "empty", "message", "emptyText", "No games match.",
						"emptyHint", "Change the filters, or add games to the library's folders; they appear after a scan."), sdui.Src("browse", "library", "$library", "filterable", true)))),
			sdui.Sec("dock", "Dock", sdui.Props("legend", true, "target", "all-games", "controls", []string{"sort", "filter"}), nil),
		}, Regions: onlineRegions()}
	m.AddPage(lib)

	// Settings, the dock's: the profile's own pages, and for an administrator the store.
	item := func(id, title, subtitle, icon string, kv ...any) sdui.Section {
		return sdui.Sec(id, "ListItem", sdui.Props(append([]any{"title", title, "subtitle", subtitle, "icon", icon}, kv...)...), nil)
	}
	set := page(PageSettings, "Settings", "settings",
		sdui.Sec("you", "List", sdui.Props("title", "For you"), nil,
			item("privacy", "Friends and what you play", "Who sees you online and what you are playing", "users", "to", "/page/"+PagePrivacy),
			item("controllers", "Controllers", "Who is on which player", "devices", "to", "/page/"+PageControllers),
			item("options", "WittyPlay options", "In this app's Settings, Modules, Games", "settings", "native", "settings")),
		withWhen(sdui.Sec("server", "List", sdui.Props("title", "This server"), nil,
			item("store", "Cores & emulators", "Consoles and their cores, BIOS files and standalone emulators: install, update, choose and remove", "disc", "to", "/page/"+PageStore),
			item("plays", "What this server plays", "Every console, and whether its games play here", "game", "to", "/page/"+Module)), forAdmins),
		sdui.Sec("computer", "List", sdui.Props("title", "On a computer", "focusable", true), nil,
			item("look", "On a computer: how WittyPlay looks and plays", "Layout, tabs and buttons, game pictures, controller mapping and my game settings are in WittyPlay's Settings in a browser.", "info")),
		legend)
	set.Description = "WittyPlay's Settings on every app: the dock's Settings. The web app shows its own (/wittyplay/settings)."
	set.Regions = onlineRegions()
	m.AddPage(set)

	// Cores & emulators, for administrators. Rows run WittyPlay's own commands; "Check again" reads
	// a download's progress again.
	admins := withWhen(sdui.Notice("admins", "info", "Only an administrator installs cores and emulators. What this server plays is in Settings."), forOthers)
	again := withWhen(sdui.Sec("again", "List", sdui.Props("title", ""), nil,
		item("check", "Check again", "Reads again how downloads and installs are going.", "history", "op", "refresh")), forAdmins)
	store := page(PageStore, "Cores & emulators", "disc", admins,
		withWhen(sdui.Sec("store", "Tabs", sdui.Props("style", "tabs", "default", "consoles"), nil,
			tab("consoles", "Consoles & cores", "game",
				list("consoles-list", "", sdui.Src(SourceStoreConsoles), "focusable", true, "empty", "message", "emptyText", "The consoles couldn't be read. Try again in a moment.")),
			tab("emulators", "Standalone emulators", "devices",
				list("emulators-list", "", sdui.Src(SourceStoreEmus), "focusable", true, "empty", "message", "emptyText", "The emulators couldn't be read. Try again in a moment.")),
			tab("support", "What this server plays", "check",
				sdui.Grid("support-grid", "", sdui.Src(SourceConsoles, "only", "all", "limit", 60)))), forAdmins),
		again, legend)
	store.Description = "For administrators, on every app: each console's cores (install, update, remove, the one it plays on) and BIOS files, and the standalone emulators."
	store.Regions = onlineRegions()
	m.AddPage(store)

	// One console's rows, opened with ?item=<console>&name=<its name> from the store's.
	one := page(PageStoreConsole, "{title}", "game", admins,
		withWhen(list("rows", "", sdui.Src(SourceStoreConsoles, "console", "$item"), "focusable", true, "empty", "message", "emptyText", "This server doesn't know this console."), forAdmins),
		again, legend)
	one.Description = "For administrators: one console's core, its cores to install, update or remove, and its BIOS files. Opened with ?item=<console>&name=<its name>."
	one.Regions = onlineRegions()
	m.AddPage(one)
}

// The commands the store's rows run: WittyPlay's own routes (api/cores.go, rpc/docker.go), which
// every app runs from the server's catalogue (WittyMediaServer docs/SDUI-MODULES.md, "Commands").
// Every one is an administrator's route, and none needs a password.
const (
	CommandCoreInstall     = Module + ".core.install"
	CommandCoreRemove      = Module + ".core.remove"
	CommandConsoleCore     = Module + ".console.core"
	CommandEmulatorInstall = Module + ".emulator.install"
	CommandEmulatorCancel  = Module + ".emulator.cancel"
	CommandEmulatorRemove  = Module + ".emulator.remove"
)

// Commands are WittyPlay's own commands, as sdui.json declares them.
func Commands() []core.CommandSpec {
	routes := "/api/modules/" + Module
	return []core.CommandSpec{
		{Op: CommandCoreInstall, Label: "Install the core", Method: "POST", Path: routes + "/gaming/cores/{item}/install",
			Description: "Cores & emulators, administrators: downloads a console's core (item: its ID) from the libretro build server, or downloads it again to update it."},
		{Op: CommandCoreRemove, Label: "Remove the core", Method: "DELETE", Path: routes + "/gaming/cores/{item}",
			Description: "Cores & emulators, administrators: removes a core this server installed (item: its ID)."},
		{Op: CommandConsoleCore, Label: "Choose the console's core", Method: "PUT", Path: routes + "/gaming/consoles/{item}/core", Body: `{"core":"{value}"}`,
			Description: "Cores & emulators, administrators: the core a console (item: its ID) plays on unless a game has its own; params.value is an installed core's ID, empty for automatic. A select row adds value.",
			Params:      []core.CommandParam{{Name: "value", Type: "string", Label: "Core"}}},
		{Op: CommandEmulatorInstall, Label: "Accept the licence and install", Method: "POST", Path: routes + "/docker/emulators/{item}/install", Body: `{"license":"{license}"}`,
			Description: "Cores & emulators, administrators: installs or updates a standalone emulator (item: its ID) in the background; params.license is the licence the row named, which must be the emulator's.",
			Params:      []core.CommandParam{{Name: "license", Type: "string", Label: "Licence accepted", Required: true}}},
		{Op: CommandEmulatorCancel, Label: "Stop installing", Method: "DELETE", Path: routes + "/docker/emulators/{item}/install",
			Description: "Cores & emulators, administrators: stops a standalone emulator's download (item: its ID)."},
		{Op: CommandEmulatorRemove, Label: "Remove the emulator", Method: "DELETE", Path: routes + "/docker/emulators/{item}",
			Description: "Cores & emulators, administrators: uninstalls a standalone emulator (item: its ID). Saves stay."},
	}
}

// JSON is sdui.json: UI, with Commands. WittyModule's sdui.Module has no field for commands yet (its
// core has, and the server reads them), so they are added here; once it has, they go into UI.
func JSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(struct {
		*sdui.Module
		Commands []core.CommandSpec `json:"commands,omitempty"`
	}{UI(), Commands()})
	return buf.Bytes(), err
}
