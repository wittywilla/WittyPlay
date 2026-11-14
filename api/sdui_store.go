// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/ui"
)

// The sources behind WittyPlay's screens for the Apple TV and Android TV apps (and the phone apps),
// which have no web view to show its React pages in (sdui.json: wittyplay-library, wittyplay-store,
// wittyplay-store-console):
//
//   - wittyplay.library: a games library's games of one console, for its shelf; with no console,
//     the consoles it (or every games library) has games for, as tiles that open the library.
//   - wittyplay.storeConsoles: for an administrator, Cores & emulators' consoles: a row each, with
//     whether it plays here and on what; with a console, that console's rows: the core it plays on
//     (a choice), each of its cores to install, update or remove, and its BIOS files.
//   - wittyplay.storeEmulators: for an administrator, the standalone emulators: a row each, and
//     beside it what can be done: accept its licence and install it, update, stop a download, remove.
//
// Every action is one of WittyPlay's own commands (sdui.json "commands"), which run its own routes,
// and those check again that the person is an administrator. What needs a password or a file (custom
// cores) stays on a computer, and a row says so.

// The sources and commands, by the names sdui.json gives them (package ui).
const (
	srcPlayLibrary       = ui.SourceLibrary
	srcPlayStoreConsoles = ui.SourceStoreConsoles
	srcPlayStoreEmus     = ui.SourceStoreEmus

	opCoreInstall    = ui.CommandCoreInstall
	opCoreRemove     = ui.CommandCoreRemove
	opConsoleCore    = ui.CommandConsoleCore
	opEmuInstall     = ui.CommandEmulatorInstall
	opEmuRemove      = ui.CommandEmulatorRemove
	opEmuCancel      = ui.CommandEmulatorCancel
	storeConsolePage = "/page/" + ui.PageStoreConsole
)

// storeSources are the sources only an administrator is given rows of.
var storeSources = []string{srcPlayStoreConsoles, srcPlayStoreEmus}

// tvItems answers the library and the store; ok is false for a source that isn't one of them.
func tvItems(req sdui.ItemsRequest) (sdui.ItemsAnswer, bool, error) {
	typ := req.Source.Type()
	if typ != srcPlayLibrary && !slices.Contains(storeSources, typ) {
		return sdui.ItemsAnswer{}, false, nil
	}
	// A page for nobody, or a profile that may not play games, has none of these; the store is an
	// administrator's alone.
	if req.Viewer.User <= 0 || slices.Contains(req.Viewer.Cannot, "games") || (typ != srcPlayLibrary && !req.Viewer.Admin) {
		return sdui.ItemsAnswer{}, true, nil
	}
	a, err := Get()
	if err != nil {
		return sdui.ItemsAnswer{}, true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	on := host.On{User: req.Viewer.User}
	limit := req.Limit
	if lim := srcInt(req.Source, "limit"); lim > 0 && (limit <= 0 || lim < limit) {
		limit = lim
	}
	var all []sdui.Item
	switch typ {
	case srcPlayLibrary:
		if console := srcStr(req.Source, "console"); console != "" {
			ans, err := a.libraryGames(ctx, on, host.Query{Library: int64(srcInt(req.Source, "library")), Facets: map[string]string{"console": console}}, req.Offset, limit)
			return ans, true, err
		}
		if srcStr(req.Source, "every") == "true" || req.Source["every"] == true {
			ans, err := a.libraryGames(ctx, on, filteredQuery(int64(srcInt(req.Source, "library")), req.Filter), req.Offset, limit)
			return ans, true, err
		}
		all, err = a.libraryConsoles(ctx, on, int64(srcInt(req.Source, "library")))
	case srcPlayStoreConsoles:
		if console := srcStr(req.Source, "console"); console != "" {
			all = a.storeConsoleRows(ctx, on, console)
		} else {
			all = a.storeConsoles(ctx, on)
		}
	case srcPlayStoreEmus:
		var entries []rpc.CatalogEntry
		if entries, err = rpc.Docker.Catalog(); err == nil {
			all = emulatorRows(entries)
		}
	}
	if err != nil {
		return sdui.ItemsAnswer{}, true, err
	}
	return pageItems(all, req.Offset, limit), true, nil
}

// ---------------------------------------------------------------- the library

// libraryGames is one page of a console's games, by title: in one library, or in every games
// library the person may open. The server pages them, and says what the person may see.
func (a *App) libraryGames(ctx context.Context, on host.On, hq host.Query, offset, limit int) (sdui.ItemsAnswer, error) {
	if console := hq.Facets["console"]; console != "" && wittyplay.ByID(console) == nil {
		return sdui.ItemsAnswer{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := &sduiAsk{a: a, ctx: ctx, on: on}
	hq.Offset, hq.Limit = max(offset, 0), min(limit+1, 500)
	if hq.Library <= 0 {
		hq.LibraryKind = "games"
	}
	games, err := q.games(hq)
	if err != nil {
		return sdui.ItemsAnswer{}, err
	}
	more := len(games) > limit
	if more {
		games = games[:limit]
	}
	out := make([]sdui.Item, 0, len(games))
	for _, g := range games {
		it := gameItem(g)
		if line := eraLine(&it, g.Console); len(line) > 0 && it.Subtitle == "" {
			it.Subtitle = strings.Join(line, " · ")
		}
		it.Actions = gameActions(g.ID, false)
		out = append(out, it)
	}
	return sdui.ItemsAnswer{Items: out, More: more}, nil
}

// filteredQuery is every game in a library (0: every games library) as the page's FilterBar
// narrows and sorts it: a genre, a score at least, and title, added (newest first), rating
// (best first) or oldest release. Filters the host can't apply are left out.
func filteredQuery(library int64, filter map[string]string) host.Query {
	hq := host.Query{Library: library, Genre: clip(filter["genre"], 100)}
	if v, err := strconv.ParseFloat(filter["minRating"], 64); err == nil && v > 0 && v <= 10 {
		hq.MinRating = v
	}
	switch filter["sort"] {
	case "added", "rating":
		hq.Sort, hq.Desc = filter["sort"], filter["desc"] != "0"
	case "oldest":
		hq.Sort, hq.Desc = "oldest", filter["desc"] == "1"
	default:
		hq.Sort, hq.Desc = "title", filter["desc"] == "1"
	}
	return hq
}

// libraryConsoles are the consoles a library (0: every games library) has games for, as tiles in
// the order of the console list, each with how many and opening the library that holds them: the
// one kept for that console, else the one with the most of its games.
func (a *App) libraryConsoles(ctx context.Context, on host.On, library int64) ([]sdui.Item, error) {
	libs, err := a.Host.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	total := map[string]int{}
	home := map[string]int64{} // console → the library its tile opens
	best := map[string]int{}   // how many of its games that library has; -1: it's kept for the console
	for _, l := range libs {
		if l.Kind != "games" || (library > 0 && l.ID != library) {
			continue
		}
		counts, err := a.Host.CountsIn(ctx, on, l.ID, "console")
		if err != nil {
			continue // a library this person can't open
		}
		for c, n := range counts {
			if n <= 0 {
				continue
			}
			total[c] += n
			switch {
			case l.Console == c:
				home[c], best[c] = l.ID, -1
			case best[c] >= 0 && n > best[c]:
				home[c], best[c] = l.ID, n
			}
		}
	}
	var out []sdui.Item
	for _, c := range wittyplay.All() {
		n := total[c.ID]
		if c.Hidden || n == 0 {
			continue
		}
		it := sdui.Item{ID: c.ID, Kind: "view", Title: consoleShort(c.ID), Subtitle: fmt.Sprintf("%d %s", n, plural(n, "game", "games")), Count: n, Icon: "game",
			Route: "/library/" + strconv.FormatInt(home[c.ID], 10) + "?tab=consoles"}
		if l, ok := eraLabels[consoleEra(c.ID)]; ok {
			it.Badges = []string{l.badge}
		}
		out = append(out, it)
	}
	return out, nil
}

// ---------------------------------------------------------------- the store: consoles

// standing says whether a console plays here, in a word or two for a row's end, and in a line.
func standing(v *ConsoleView) (word, line string) {
	switch {
	case v.Ready:
		line = "Plays here"
		if now := playsOn(v); now != "" {
			line = "Plays with " + now
		}
		if v.Software {
			line += ", drawn by the processor"
		}
		return "Plays here", line
	case v.Reason == "gpu":
		return "Needs a graphics card", nonEmpty(v.Message, "Its games are drawn with a graphics card, and this server has none it can use.")
	case v.Reason == "unavailable":
		return "Not on this server", nonEmpty(v.Message, v.Unavailable)
	case v.Blocked != "" || v.BIOSMissing != "":
		return "Needs a BIOS", "Needs your own BIOS file, such as " + nonEmpty(v.Blocked, v.BIOSMissing) + "."
	case v.Reason == "emulator":
		return "Needs an emulator", nonEmpty(v.Message, "Install its standalone emulator.")
	}
	return "Needs a core", nonEmpty(v.Message, "Install a core, the free program that plays its games.")
}

// playsOn is the core a console's games play on now: its default, else the first installed.
func playsOn(v *ConsoleView) string {
	for _, c := range v.Installed {
		if c.ID == v.DefaultCore {
			return c.Name
		}
	}
	if len(v.Installed) > 0 {
		return v.Installed[0].Name
	}
	for _, e := range v.Emulators {
		if e.ID == v.Standalone {
			return e.Name
		}
	}
	return ""
}

func nonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// percent is how far a download has got, as a row's end says it: "42%", or "Downloading" with no size.
func percent(got, total int64) string {
	if total > 0 {
		return fmt.Sprintf("%d%%", min(got*100/total, 99))
	}
	return "Downloading"
}

// installing is how far a console's core downloads have got, when one is under way.
func (a *App) installing(v *ConsoleView) string {
	for _, c := range v.Cores {
		if got, total, ok := a.Cores().InstallProgress(c.ID); ok {
			return percent(got, total)
		}
	}
	return ""
}

// storeConsoles: a row for each console, those with games first (most first), each opening the
// console's own rows. Its end says whether it plays here, or how far a core's download has got.
func (a *App) storeConsoles(ctx context.Context, on host.On) []sdui.Item {
	views := a.gamingConsoles(ctx, on)
	sort.SliceStable(views, func(i, j int) bool { return views[i].TotalGames > views[j].TotalGames })
	out := make([]sdui.Item, 0, len(views)+1)
	for i := range views {
		v := &views[i]
		word, line := standing(v)
		if v.TotalGames > 0 {
			line = fmt.Sprintf("%d %s · %s", v.TotalGames, plural(v.TotalGames, "game", "games"), line)
		}
		if p := a.installing(v); p != "" {
			word = "Installing " + p
		}
		q := url.Values{"item": {v.ID}, "name": {v.Name}}
		out = append(out, sdui.Item{ID: "console-" + v.ID, Kind: "view", Title: v.Name, Subtitle: line, Trailing: word, Icon: "game",
			Action: &sdui.Action{Type: core.ActNavigate, To: storeConsolePage + "?" + q.Encode()}})
	}
	out = append(out, onAComputer("custom", "Custom cores and consoles",
		"Adding a core of your own needs your password and a file upload: open WittyPlay's Settings, Cores & emulators, in a browser."))
	return out
}

// onAComputer is a row for something done only in a browser: it says where, and does nothing.
func onAComputer(id, what, how string) sdui.Item {
	return sdui.Item{ID: "computer-" + id, Kind: "view", Title: "On a computer: " + what, Subtitle: how, Icon: "info"}
}

// storeConsoleRows are one console's rows: the core it plays on, as a choice of those installed;
// each of its cores, with a row to install it (its licence accepted in the row's words), download it
// again or remove it; any custom cores; and its BIOS files, found or not.
func (a *App) storeConsoleRows(ctx context.Context, on host.On, id string) []sdui.Item {
	var v *ConsoleView
	views := a.gamingConsoles(ctx, on)
	for i := range views {
		if views[i].ID == id {
			v = &views[i]
		}
	}
	if v == nil {
		return nil
	}
	console := v.Console
	ch := a.coreChoices(ctx, &console, 0)
	word, line := standing(v)
	if p := a.installing(v); p != "" {
		word = "Installing " + p
	}
	out := []sdui.Item{{ID: "standing", Kind: "view", Title: v.Name, Subtitle: line, Trailing: word, Icon: "game"}}

	// The core its games play on: Automatic, or one of those installed.
	auto := "Automatically"
	for _, c := range ch.Choices {
		if c.ID == ch.Effective {
			auto = "Automatically (" + c.Name + " now)"
		}
	}
	choose := &sdui.Control{Type: core.ControlSelect, Name: "core", Value: ch.Choice, Options: []core.EnumOption{{Value: "", Label: auto}}}
	for _, c := range ch.Choices {
		if c.Installed {
			choose.Options = append(choose.Options, core.EnumOption{Value: c.ID, Label: c.Name})
		}
	}
	if len(choose.Options) == 1 {
		choose.Disabled, choose.Why = true, "Install one of its cores first."
	}
	out = append(out, sdui.Item{ID: "plays-with", Kind: "view", Title: "Plays with", Subtitle: "The core its games start on, unless a game has its own.", Icon: "settings",
		Control: choose, Action: &sdui.Action{Type: core.ActCommand, Op: opConsoleCore, Item: v.ID}})

	have := map[string]cores.Core{}
	for _, c := range a.Cores().List() {
		have[c.ID] = c
	}
	for _, c := range ch.Choices {
		if c.Source == cores.SourceCustom && !consoleListsCore(&console, c.ID) {
			out = append(out, sdui.Item{ID: "core-" + c.ID, Kind: "view", Title: c.Name, Subtitle: "A custom core: third-party, unverified. Licence: " + nonEmpty(c.License, "not known") + ".",
				Trailing: "Ready", Icon: "plug", Badges: []string{"Custom"}})
			continue
		}
		out = append(out, coreRows(a, c, have[c.ID], ch.CanDownload)...)
	}

	found := map[string]bool{}
	for _, f := range v.BiosFound {
		found[f] = true
	}
	for _, b := range v.BIOS {
		it := sdui.Item{ID: "bios-" + b.File, Kind: "view", Title: "BIOS: " + b.File, Icon: "check", Trailing: "Found", Subtitle: "Found. " + b.For}
		if !found[b.File] {
			it.Icon, it.Subtitle, it.Trailing = "info", "Not found yet. "+b.For, "Optional"
			if b.Required {
				it.Icon, it.Trailing = "warning", "Needed"
			}
		}
		out = append(out, it)
	}
	if len(v.BIOS) > 0 {
		out = append(out, sdui.Item{ID: "bios-how", Kind: "view", Title: "Adding BIOS files", Icon: "folder",
			Subtitle: "Put yours in a folder named " + wittyplay.BIOSFolderName + " beside your " + v.Short + " games. Each is found by its checksum after a scan, or when you press Play. They're never downloaded for you."})
	}
	return append(out, onAComputer("custom-"+v.ID, "a core of your own for "+v.Short,
		"Adding or changing a custom core needs your password and a file upload: WittyPlay's Settings, Cores & emulators, in a browser."))
}

// coreRows are one core of a console's: what it is and whether it's here, then what can be done
// with it: install it (the row says its licence is accepted), or download it again and remove it.
// A download under way says how far it has got instead.
func coreRows(a *App, c coreChoice, got cores.Core, canDownload bool) []sdui.Item {
	sub := []string{}
	if c.About != "" {
		sub = append(sub, c.About)
	}
	sub = append(sub, "Licence: "+nonEmpty(c.License, "not known")+".")
	if c.BIOS != nil && !c.BIOS.Found {
		sub = append(sub, "Needs your own BIOS file, such as "+c.BIOS.Files[0]+".")
	}
	info := sdui.Item{ID: "core-" + c.ID, Kind: "view", Title: c.Name, Subtitle: strings.Join(sub, " "), Icon: "game"}
	if c.Recommended {
		info.Badges = []string{"Recommended"}
	}
	gets, total, busy := a.Cores().InstallProgress(c.ID)
	switch {
	case busy:
		info.Trailing = "Installing " + percent(gets, total)
		return []sdui.Item{info}
	case c.Installed && got.Source == "installed":
		info.Trailing = "Installed"
	case c.Installed && got.Source == "retroarch":
		info.Trailing = "In RetroArch's folder"
	case c.Installed:
		info.Trailing = "Found on this server"
	case c.Support != nil && !c.Support.Here.Build:
		info.Trailing = "Not for this server"
	case c.Support != nil && c.Support.Here.Draws == drawsNone:
		info.Trailing = "Needs a graphics card"
	case !canDownload:
		info.Trailing = "No downloads here"
		info.Subtitle += " There are no prebuilt cores for this platform: install it with your system's packages, or place it in the cores folder."
	default:
		info.Trailing = "Not installed"
	}
	out := []sdui.Item{info}
	install := &sdui.Action{Type: core.ActCommand, Op: opCoreInstall, Item: c.ID}
	switch {
	case c.CanInstall:
		out = append(out, sdui.Item{ID: "install-" + c.ID, Kind: "view", Title: "Accept " + nonEmpty(c.License, "its licence") + " and install " + c.Name, Icon: "download",
			Subtitle: "Downloads it from buildbot.libretro.com, the libretro project's build server. It stays its authors' work, a separate program.", Action: install})
	case c.Installed && got.Source == "installed":
		out = append(out,
			sdui.Item{ID: "update-" + c.ID, Kind: "view", Title: "Update " + c.Name, Icon: "download",
				Subtitle: "Downloads the newest build again, under the same licence (" + nonEmpty(c.License, "its own") + ").", Action: install},
			sdui.Item{ID: "remove-" + c.ID, Kind: "view", Title: "Remove " + c.Name, Icon: "warning",
				Subtitle: "Saves stay: they belong to people and games, not cores. Install it again any time.", Action: &sdui.Action{Type: core.ActCommand, Op: opCoreRemove, Item: c.ID}})
	}
	return out
}

// ---------------------------------------------------------------- the store: standalone emulators

// emulatorRows: a row for each standalone emulator (its consoles, licence, firmware and whether it
// is here), then what can be done with it: accept its licence and install it, update it, stop a
// download, remove it. One that's never downloaded says it is installed on a computer.
func emulatorRows(entries []rpc.CatalogEntry) []sdui.Item {
	var out []sdui.Item
	for _, e := range entries {
		systems := make([]string, 0, len(e.Systems))
		for _, s := range e.Systems {
			systems = append(systems, consoleShort(s))
		}
		sub := []string{}
		if len(systems) > 0 {
			sub = append(sub, "Plays "+strings.Join(systems, ", ")+".")
		}
		sub = append(sub, "Licence: "+nonEmpty(e.License, "not known")+".")
		if len(e.Bios) > 0 {
			n := 0
			for _, b := range e.Bios {
				if b.Present {
					n++
				}
			}
			sub = append(sub, fmt.Sprintf("Firmware: %d of %d found.", n, len(e.Bios)))
		}
		info := sdui.Item{ID: "emulator-" + e.ID, Kind: "view", Title: e.Name, Icon: "game"}
		if e.Experimental {
			info.Badges = []string{"Experimental"}
		}
		job := e.Job
		running := job != nil && job.State == "running"
		switch {
		case running:
			info.Trailing = percent(job.Got, job.Total)
			if job.Phase != "" && job.Phase != "downloading" {
				info.Trailing = "Installing"
			}
		case job != nil && job.State == "failed":
			info.Trailing = "Didn't install"
			sub = append(sub, job.Error)
		case e.Installed != nil && e.UpdateAvailable:
			info.Trailing = "Update to " + e.Version
		case e.Installed != nil:
			info.Trailing = nonEmpty("Installed "+e.Installed.Version, "Installed")
		case !e.Installable:
			info.Trailing = "Can't install here"
			if e.Manual == nil && e.Why != "" {
				sub = append(sub, upperFirst(e.Why)+".")
			}
		default:
			info.Trailing = "Not installed"
		}
		if e.Installed != nil && e.WhyNot != "" {
			sub = append(sub, e.WhyNot)
		}
		info.Subtitle = strings.Join(sub, " ")
		out = append(out, info)

		install := &sdui.Action{Type: core.ActCommand, Op: opEmuInstall, Item: e.ID, Params: map[string]any{"license": e.License}}
		switch {
		case running:
			out = append(out, sdui.Item{ID: "cancel-" + e.ID, Kind: "view", Title: "Stop installing " + e.Name, Icon: "warning",
				Action: &sdui.Action{Type: core.ActCommand, Op: opEmuCancel, Item: e.ID}})
			continue
		case e.Manual != nil && e.Installed == nil:
			out = append(out, onAComputer("emulator-"+e.ID, "installing "+e.Name,
				e.Name+" is never downloaded: install it from "+e.Manual.Download+", then add it in WittyPlay's Settings, Standalone emulators, in a browser."))
		case e.Installable && e.Installed == nil:
			out = append(out, sdui.Item{ID: "install-" + e.ID, Kind: "view", Title: "Accept " + nonEmpty(e.License, "its licence") + " and install " + e.Name, Icon: "download",
				Subtitle: sizeWords(e.DownloadBytes) + "It is its authors' work, under its own licence, and runs as a separate program.", Action: install})
		case e.Installable && e.UpdateAvailable:
			out = append(out, sdui.Item{ID: "update-" + e.ID, Kind: "view", Title: "Accept " + nonEmpty(e.License, "its licence") + " and update to " + e.Version, Icon: "download",
				Subtitle: sizeWords(e.DownloadBytes) + "Its settings and saves stay.", Action: install})
		}
		if e.Installed != nil {
			out = append(out, sdui.Item{ID: "remove-" + e.ID, Kind: "view", Title: "Remove " + e.Name, Icon: "warning",
				Subtitle: "Saves stay. Install it again any time.", Action: &sdui.Action{Type: core.ActCommand, Op: opEmuRemove, Item: e.ID}})
		}
	}
	return append(out, onAComputer("emulators", "container images, your own emulator programs and each emulator's settings",
		"These are set in WittyPlay's Settings, Standalone emulators, in a browser."))
}

// sizeWords starts a row's line with how big a download is, when known.
func sizeWords(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("A %d MB download. ", max(n>>20, 1))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// clip is s cut to at most n bytes.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
