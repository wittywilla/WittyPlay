// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"github.com/wittywilla/wittymodule/sdui"
	core "github.com/wittywilla/wittymodule/sdui/core"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/stream"
	"github.com/wittywilla/wittyplay/ui"
)

// Performance profiles: one standard for what every emulator starts with, chosen for how strong
// this server is (gamingProfile). A profile only fills in the server-wide settings an
// administrator hasn't set by hand (gamingSetting), so anything set in Settings still wins, and
// each console's own settings ("Everyone on this console", gamingDefaults.<console>) are laid
// over it in WittyPlay's Settings. With no profile chosen, games start as "balanced", which is
// how they started before profiles.
//
// GET /gaming/profiles               → {chosen, recommended, why, profiles: [...]}
// PUT /gaming/profiles/{id}          → the same (administrators); "auto" clears the choice

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /gaming/profiles", w(adminRoute(handleProfilesGet)))
		mux.Handle("PUT /gaming/profiles/{id}", w(adminRoute(handleProfilePut)))
	})
}

const cfgProfile = "gamingProfile"

// Profile is one standard: who it's for and the settings it fills in.
type Profile struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	For      string            `json:"for"`      // hardware like this, in a few words
	Examples string            `json:"examples"` // named machines and parts
	Does     string            `json:"does"`     // what it changes, in a sentence
	Settings map[string]string `json:"settings"` // gamingSettings it fills in
}

// The profiles, lightest first. Resolution "native" is 1 for every console; "" is the console's
// own default (2 for N64, PS1, PSP and Dreamcast); "up" is one step more than that.
var profiles = []Profile{
	{ID: "lightest", Label: "Very light",
		For:      "Small, old or fanless computers, or no graphics card",
		Examples: "Raspberry Pi 4 or 5, Intel N100, Celeron or Atom mini PCs, dual-core laptops",
		Does:     "Native resolution, no run-ahead or rewind, a lighter picture, one game at a time. Best for consoles up to the PlayStation.",
		Settings: map[string]string{"gamingResolution": "native", "gamingRunAhead": "0", "gamingRewindMB": "0", "gamingQuality": "low",
			"gamingMaxSessions": "1", "gamingMaxGpuSessions": "1", "gamingMaxSpeed": "2"}},
	{ID: "light", Label: "Light",
		For:      "Four-core computers with built-in graphics",
		Examples: "Core i5-3470 or i5-7200U, Ryzen 3 2200G, Intel UHD 620/630 or Radeon Vega 8 graphics",
		Does:     "Native resolution, no run-ahead, a short rewind, a lighter picture, one game at a time.",
		Settings: map[string]string{"gamingResolution": "native", "gamingRunAhead": "0", "gamingRewindMB": "16", "gamingQuality": "low",
			"gamingMaxSessions": "1", "gamingMaxGpuSessions": "1", "gamingMaxSpeed": "2"}},
	{ID: "balanced", Label: "Balanced",
		For:      "Six cores and a mid-range graphics card",
		Examples: "Ryzen 5 3600 or Core i5-8400 with a GTX 1650, GTX 1060 or RX 570",
		Does:     "Each console's usual resolution, run-ahead and rewind on, two games at once.",
		Settings: map[string]string{}},
	{ID: "high", Label: "High quality",
		For:      "Eight or more cores and a strong graphics card",
		Examples: "Ryzen 7 5800X or Core i7-12700 with an RTX 3060, RX 6700 XT or better",
		Does:     "One step sharper than each console's usual resolution, a better picture, more games at once.",
		Settings: map[string]string{"gamingResolution": "up", "gamingQuality": "high", "gamingRewindMB": "64",
			"gamingMaxSessions": "4", "gamingMaxGpuSessions": "3", "gamingMaxSpeed": "8"}},
}

const defaultProfile = "balanced"

func profileByID(id string) *Profile {
	for i := range profiles {
		if profiles[i].ID == id {
			return &profiles[i]
		}
	}
	return nil
}

// profile is the profile in use: the one chosen, or balanced.
func (a *App) profile(ctx context.Context) *Profile {
	if p := profileByID(a.configText(ctx, cfgProfile, "")); p != nil {
		return p
	}
	return profileByID(defaultProfile)
}

// gamingSetting is one of gamingSettings as games use it: what an administrator set, else what the
// profile says, else def.
func (a *App) gamingSetting(ctx context.Context, key, def string) string {
	if v := a.configText(ctx, key, ""); v != "" {
		return v
	}
	if v, ok := a.profile(ctx).Settings[key]; ok {
		return v
	}
	return def
}

// resolutionFor turns the resolution setting into what StartOptions takes for a console: "" for
// its own default, or "1" to "4".
func resolutionFor(setting, console string) string {
	switch setting {
	case "native":
		return "1"
	case "up":
		return strconv.Itoa(min(wittyplay.DefaultResolution(console)+1, 4))
	}
	return setting
}

// recommendProfile suggests a profile from the threads and graphics card here, and why.
func recommendProfile(threads int, gpu stream.GPUProbe, missing string) (string, string) {
	name := strings.ToLower(gpu.Name)
	switch {
	case gpu.Checking:
		return defaultProfile, "Still checking the graphics card."
	case missing != "" || strings.Contains(name, "llvmpipe") || strings.Contains(name, "basic render"):
		if threads >= 8 {
			return "light", "No graphics card games can use, so games draw on the processor."
		}
		return "lightest", "No graphics card games can use, and " + strconv.Itoa(threads) + " processor threads."
	case threads <= 2:
		return "lightest", strconv.Itoa(threads) + " processor threads."
	case threads <= 4:
		return "light", strconv.Itoa(threads) + " processor threads."
	case gpu.VRAMMB > 0 && gpu.VRAMMB < 2048:
		return "light", gpu.Name + " has under 2 GB of its own memory."
	case threads >= 12 && gpu.VRAMMB >= 6144:
		return "high", strconv.Itoa(threads) + " processor threads and " + gpu.Name + "."
	}
	why := strconv.Itoa(threads) + " processor threads"
	if gpu.Name != "" {
		why += " and " + gpu.Name
	}
	return defaultProfile, why + "."
}

func (a *App) profilesView(ctx context.Context) map[string]any {
	rec, why := recommendProfile(runtime.NumCPU(), a.GPUStatus(ctx), a.GPUMissing(ctx))
	return map[string]any{"chosen": a.configText(ctx, cfgProfile, ""), "inUse": a.profile(ctx).ID,
		"recommended": rec, "why": why, "profiles": profiles}
}

func handleProfilesGet(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.profilesView(r.Context()))
}

func handleProfilePut(a *App, w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var val any
	switch {
	case id == "auto":
	case profileByID(id) != nil:
		val = id
	default:
		writeErr(w, 400, "WittyPlay has no profile "+strconv.Quote(id))
		return
	}
	if err := a.SetConfig(ctx, cfgProfile, val); err != nil {
		writeErr(w, 500, "the profile could not be kept")
		return
	}
	a.Log.InfoContext(ctx, "wittyplay: performance profile", "cat", "games", "profile", id, "user", who(r).User)
	writeJSON(w, 200, a.profilesView(ctx))
}

// performanceRows are the profiles as rows for every app (ui.SourcePerformance): the one in use
// checked, the one this hardware suggests marked, each choosing itself.
func (a *App) performanceRows(ctx context.Context) []sdui.Item {
	inUse := a.profile(ctx).ID
	rec, why := recommendProfile(runtime.NumCPU(), a.GPUStatus(ctx), a.GPUMissing(ctx))
	out := make([]sdui.Item, 0, len(profiles)+1)
	for _, p := range profiles {
		trailing, icon := "", "chart"
		switch {
		case p.ID == inUse && p.ID == rec:
			trailing, icon = "In use, suggested", "check"
		case p.ID == inUse:
			trailing, icon = "In use", "check"
		case p.ID == rec:
			trailing = "Suggested"
		}
		out = append(out, sdui.Item{ID: "profile-" + p.ID, Kind: "view", Title: p.Label, Icon: icon, Trailing: trailing,
			Subtitle: p.For + ": " + p.Examples + ". " + p.Does,
			Action:   &sdui.Action{Type: core.ActCommand, Op: ui.CommandPerformanceSet, Item: p.ID}})
	}
	out = append(out, sdui.Item{ID: "profile-why", Kind: "view", Title: "Why " + profileByID(rec).Label + " is suggested", Icon: "info", Subtitle: why})
	return out
}
