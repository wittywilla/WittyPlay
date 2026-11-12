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
