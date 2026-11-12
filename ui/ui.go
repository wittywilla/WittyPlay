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
			Params:      []sdui.PropSpec{limit(20, 100)}, Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceRooms, Label: "Open rooms", Icon: "users", Title: "Open rooms",
			Description: "Friends' games on this server that are open to join, with a seat free, of games this profile may play. None for a Kids profile.",
			Params:      []sdui.PropSpec{limit(12, 50)}, Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceRoom, Label: "Your room", Icon: "users", Title: "Your room",
			Description: "The game this profile is playing with others: the room first (its code for the host), then each player, or Waiting… for a free seat.",
			Params:      []sdui.PropSpec{limit(9, 9)}, Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceSaveStates, Label: "Save states", Icon: "history", Title: "Save states",
			Description: "One game's saves for this profile, its own first, newest first.", PageKinds: []string{"detail"},
			Params: []sdui.PropSpec{{Name: "item", Type: "ref", Label: "Game", Required: true, Help: "$item on a game's page."}, limit(20, 110)},
			Yields: []string{"view"}, CardStyle: "landscape"},
		{Type: SourceMemories, Label: "Memories", Icon: "photo", Title: "Memories",
			Description: "This profile's save states and kept screenshots, across its games, newest first.",
			Params:      []sdui.PropSpec{limit(24, 500)}, Yields: []string{"view", "photo"}, CardStyle: "landscape", Paged: true},
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
