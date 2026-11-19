// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wittywilla/wittyplay/stream"
)

// A performance profile fills in what an administrator hasn't set, what they set by hand still
// wins, and only an administrator chooses one.
func TestPerformanceProfiles(t *testing.T) {
	a, h, _ := coresApp(t)
	ctx := context.Background()
	if w := asWho(t, h, "PUT", "/gaming/profiles/light", otherID, nil); w.Code != 403 {
		t.Fatalf("someone who isn't an administrator chose one: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/profiles/turbo", adminID, nil); w.Code != 400 {
		t.Fatalf("an unknown profile: %d", w.Code)
	}

	// None chosen: balanced, which is how games started before profiles.
	if a.profile(ctx).ID != "balanced" || a.gamingSetting(ctx, "gamingMaxSessions", "2") != "2" || !a.gameLimits(ctx).RunAhead {
		t.Fatalf("with no profile: %s, %+v", a.profile(ctx).ID, a.gameLimits(ctx))
	}

	w := asWho(t, h, "PUT", "/gaming/profiles/lightest", adminID, nil)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	var got struct {
		Chosen, InUse, Recommended string
		Profiles                   []Profile
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Chosen != "lightest" || got.InUse != "lightest" || len(got.Profiles) != 4 || got.Recommended == "" {
		t.Fatalf("answer: %+v %v", got, err)
	}
	lim := a.gameLimits(ctx)
	if a.gamingSetting(ctx, "gamingMaxSessions", "2") != "1" || a.gamingSetting(ctx, "gamingQuality", "") != "low" || lim.RunAhead || lim.RewindBytes != 0 || lim.MaxSpeed != 2 {
		t.Fatalf("very light: sessions %s, limits %+v", a.gamingSetting(ctx, "gamingMaxSessions", "2"), lim)
	}
	if o := a.StartOptions(ctx, "n64", "mupen64plus_next"); o["mupen64plus-43screensize"] != "640x480" {
		t.Fatalf("very light N64 isn't native: %q", o["mupen64plus-43screensize"])
	}

	// What an administrator set by hand wins over the profile.
	if w := asWho(t, h, "PUT", "/gaming/settings", adminID, map[string]string{"gamingMaxSessions": "3", "gamingRunAhead": "1", "gamingResolution": "2"}); w.Code != 200 {
		t.Fatalf("settings: %d", w.Code)
	}
	if a.gamingSetting(ctx, "gamingMaxSessions", "2") != "3" || !a.gameLimits(ctx).RunAhead {
		t.Fatal("the profile won over a setting set by hand")
	}
	if o := a.StartOptions(ctx, "gc", "dolphin"); o["dolphin_efb_scale"] != "2" {
		t.Fatalf("resolution set by hand: %q", o["dolphin_efb_scale"])
	}

	// "auto" goes back to none chosen.
	if w := asWho(t, h, "PUT", "/gaming/profiles/auto", adminID, nil); w.Code != 200 || a.configText(ctx, cfgProfile, "") != "" {
		t.Fatalf("auto: %d %q", w.Code, a.configText(ctx, cfgProfile, ""))
	}
}

func TestResolutionFor(t *testing.T) {
	for _, c := range []struct{ set, console, want string }{
		{"", "gc", ""}, {"3", "gc", "3"}, {"native", "n64", "1"}, {"up", "gc", "2"}, {"up", "n64", "3"},
	} {
		if got := resolutionFor(c.set, c.console); got != c.want {
			t.Errorf("%q on %s: %q, want %q", c.set, c.console, got, c.want)
		}
	}
}

func TestRecommendProfile(t *testing.T) {
	card := stream.GPUProbe{Name: "NVIDIA GeForce RTX 3060", VRAMMB: 12288}
	for _, c := range []struct {
		name    string
		threads int
		gpu     stream.GPUProbe
		missing string
		want    string
	}{
		{"a Raspberry Pi", 4, stream.GPUProbe{Name: "V3D 4.2"}, "", "light"},
		{"two threads", 2, card, "", "lightest"},
		{"no card, few threads", 4, stream.GPUProbe{Error: "none"}, "no graphics card", "lightest"},
		{"no card, many threads", 16, stream.GPUProbe{Name: "llvmpipe (LLVM 15.0.7, 256 bits)"}, "", "light"},
		{"a small card", 8, stream.GPUProbe{Name: "GeForce GT 710", VRAMMB: 1024}, "", "light"},
		{"a mid-range PC", 12, stream.GPUProbe{Name: "GeForce GTX 1650", VRAMMB: 4096}, "", "balanced"},
		{"a strong PC", 16, card, "", "high"},
		{"still checking", 16, stream.GPUProbe{Checking: true}, "", "balanced"},
	} {
		if got, why := recommendProfile(c.threads, c.gpu, c.missing); got != c.want || why == "" {
			t.Errorf("%s: %s (%s), want %s", c.name, got, why, c.want)
		}
	}
}
