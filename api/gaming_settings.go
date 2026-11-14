// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// WittyPlay's settings for the whole server, as Settings, Consoles & cores shows them: which
// version of a game comes first, picture quality, how many games at once, the limits on
// fast-forward and rewind, the runner, the room for unpacked games and the network. Each is one
// of WittyPlay's own settings (wittyplay_config, a.Config); nothing of them is the server's.
//
// GET /gaming/settings answers every one as text ("" when it isn't set); PUT takes some of them,
// checks them all before keeping any, keeps "" as not set, and answers as GET does. Both are for
// administrators (auth admin in integration.json).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /gaming/settings", w(handleGamingSettingsGet))
		mux.Handle("PUT /gaming/settings", w(handleGamingSettingsPut))
	})
}

// gameRegions are the regions a version of a game can be put first for (gamingRegionOrder).
var gameRegions = []string{"usa", "europe", "australia", "japan", "asia", "brazil"}

// gamingSettings are the settings, each with what it may hold.
var gamingSettings = map[string]func(string) bool{
	"gamingRegionOrder":    regionOrderOK,
	"gamingQuality":        oneOf("low", "high"),
	"gamingMaxSessions":    intIn(1, 16),
	"gamingMaxGpuSessions": intIn(1, 16),
	"gamingResolution":     oneOf("1", "2", "3", "4"),
	"gamingMaxSpeed":       numberIn(1, 16),
	"gamingRewindMB":       intIn(0, 512),
	"gamingRunAhead":       oneOf("0", "1"),
	"gamingRunner":         oneOf("auto", "builtin", "retroarch"),
	"gamingUnpackGB":       intIn(1, 100000),
	"gamingUdpPort":        intIn(0, 65535),
	"gamingPublicIp":       listOf(func(s string) bool { return net.ParseIP(s) != nil }),
	"gamingStun":           listOf(stunOK),
}

// stunOK takes a STUN server's address as a WebRTC peer is given it: stun:host:port.
func stunOK(s string) bool {
	return strings.HasPrefix(s, "stun:") && len(s) <= 256 && !strings.ContainsAny(s, " \t\r\n")
}

func oneOf(vals ...string) func(string) bool {
	return func(v string) bool { return slices.Contains(vals, v) }
}

func intIn(lo, hi int) func(string) bool {
	return func(v string) bool {
		n, err := strconv.Atoi(v)
		return err == nil && n >= lo && n <= hi
	}
}

func numberIn(lo, hi float64) func(string) bool {
	return func(v string) bool {
		f, err := strconv.ParseFloat(v, 64)
		return err == nil && f >= lo && f <= hi
	}
}

// listOf takes a list with commas between, each passing ok.
func listOf(ok func(string) bool) func(string) bool {
	return func(v string) bool {
		parts := splitNonEmpty(v)
		if len(parts) == 0 || len(parts) > 16 {
			return false
		}
		for _, p := range parts {
			if !ok(p) {
				return false
			}
		}
		return true
	}
}

func regionOrderOK(v string) bool {
	seen := map[string]bool{}
	for _, r := range strings.Split(v, ",") {
		r = strings.TrimSpace(r)
		if !slices.Contains(gameRegions, r) || seen[r] {
			return false
		}
		seen[r] = true
	}
	return true
}

func (a *App) gamingSettingsView(r *http.Request) map[string]string {
	out := make(map[string]string, len(gamingSettings))
	for k := range gamingSettings {
		out[k] = a.configText(r.Context(), k, "")
	}
	return out
}

func handleGamingSettingsGet(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	writeJSON(w, 200, a.gamingSettingsView(r))
}

func handleGamingSettingsPut(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	var in map[string]string
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "send the settings as an object of text")
		return
	}
	for k, v := range in {
		ok := gamingSettings[k]
		if ok == nil {
			writeErr(w, 400, "WittyPlay has no setting "+strconv.Quote(k))
			return
		}
		if v = strings.TrimSpace(v); v != "" && !ok(v) {
			writeErr(w, 400, "that value is not valid for "+k)
			return
		}
	}
	for k, v := range in {
		var val any
		if v = strings.TrimSpace(v); v != "" {
			val = v
		}
		if err := a.SetConfig(r.Context(), k, val); err != nil {
			writeErr(w, 500, "the settings could not be kept")
			return
		}
	}
	writeJSON(w, 200, a.gamingSettingsView(r))
}
