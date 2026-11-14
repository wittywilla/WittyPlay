// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/ui"
)

// The answers to the server's SDUI calls (ui.UI says what they're for): which consoles this
// server plays, and the module's settings when an administrator changes them.

// lastCores is the server's cores as it last described them: where they're installed, and the
// custom ones. The server tells the module with every call about cores, which every games page
// makes; until then no core counts as installed.
var lastCores atomic.Pointer[cores.Manager]

var (
	settingsMu sync.Mutex
	settings   = readSettings()
)

// readSettings is the module's server settings as the server started it with them.
func readSettings() map[string]any {
	out := map[string]any{}
	if v := os.Getenv(sdui.SettingsEnv); v != "" {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}

// SettingsChanged hears the module's server settings after an administrator saved them.
func SettingsChanged(values map[string]any) {
	settingsMu.Lock()
	settings = values
	settingsMu.Unlock()
}

// boolSetting is a setting from the request (the server's, then the profile's), else the one the
// helper was told, else def.
func boolSetting(req map[string]any, key string, def bool) bool {
	if v, ok := req[key].(bool); ok {
		return v
	}
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if v, ok := settings[key].(bool); ok {
		return v
	}
	return def
}

// ConsoleStanding says whether a console plays here and how, in a few words for a card.
func ConsoleStanding(c *wittyplay.Console, mgr *cores.Manager, offers []Offer) (ready bool, how string) {
	if mgr != nil {
		if got := mgr.ForConsole(c, ""); got != nil {
			return true, "On " + got.Name
		}
	}
	for _, o := range offers {
		if o.Ready {
			return true, "In " + o.Name
		}
	}
	if len(c.Cores) == 0 && len(c.Emulators) == 0 {
		return false, "Needs an emulator of your own"
	}
	if len(c.Cores) == 0 {
		return false, "Needs a standalone emulator"
	}
	return false, "Needs a core"
}

// ConsoleItems answers wittyplay.consoles: every console, ready ones first, as tiles that open
// Settings' page of what this server plays. Package api answers WittyPlay's other sources, and
// asks this for the rest (api.SDUIItems).
func ConsoleItems(req sdui.ItemsRequest) (sdui.ItemsAnswer, error) {
	only, _ := req.Source["only"].(string)
	// The row on the Games page goes when an administrator turns it off; a profile can ask
	// for only the consoles that play here.
	if req.Source["only"] == "ready" && !boolSetting(req.Settings, ui.SettingShowConsoles, true) {
		return sdui.ItemsAnswer{}, nil
	}
	if boolSetting(req.Settings, ui.SettingOnlyPlayable, false) {
		only = "ready"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offers := Docker.ConsoleOffers(ctx, "")
	mgr := lastCores.Load()
	var ready, rest []sdui.Item
	for _, c := range wittyplay.All() {
		if c.Hidden {
			continue
		}
		ok, how := ConsoleStanding(&c, mgr, offers[c.ID])
		it := sdui.Item{ID: c.ID, Kind: "view", Title: c.Name, Subtitle: how, Icon: "game", Route: "/settings/games-support"}
		if ok {
			ready = append(ready, it)
		} else if only != "ready" {
			it.Badges = []string{"Not yet"}
			rest = append(rest, it)
		}
	}
	all := append(ready, rest...)
	total := len(all)
	if req.Offset > len(all) {
		req.Offset = len(all)
	}
	all = all[req.Offset:]
	limit := req.Limit
	if l, ok := req.Source["limit"].(float64); ok && int(l) > 0 && (limit == 0 || int(l) < limit) {
		limit = int(l)
	}
	more := false
	if limit > 0 && len(all) > limit {
		all, more = all[:limit], true
	}
	return sdui.ItemsAnswer{Items: all, More: more, Total: total}, nil
}
