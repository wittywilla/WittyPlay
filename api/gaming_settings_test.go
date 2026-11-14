// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"testing"
)

// Settings, Consoles & cores keeps WittyPlay's server-wide settings in WittyPlay, for
// administrators only, checked before any is kept, and the engine reads them back.
func TestGamingSettingsAreWittyPlays(t *testing.T) {
	a, h, _ := coresApp(t)
	ctx := context.Background()
	person := otherID
	if w := asWho(t, h, "GET", "/gaming/settings", person, nil); w.Code != 403 {
		t.Fatalf("someone who isn't an administrator read them: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/settings", person, map[string]string{"gamingQuality": "low"}); w.Code != 403 {
		t.Fatalf("someone who isn't an administrator changed them: %d", w.Code)
	}

	w := asWho(t, h, "PUT", "/gaming/settings", adminID, map[string]string{
		"gamingRegionOrder": "japan,europe", "gamingQuality": "high", "gamingMaxSpeed": "2.5", "gamingRunAhead": "0", "gamingStun": "stun:stun.example.org:3478"})
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got["gamingQuality"] != "high" || got["gamingRegionOrder"] != "japan,europe" || got["gamingUnpackGB"] != "" {
		t.Fatalf("answer: %v %v", got, err)
	}
	if a.configText(ctx, "gamingQuality", "") != "high" || a.gameLimits(ctx).MaxSpeed != 2.5 || a.gameLimits(ctx).RunAhead {
		t.Fatalf("not what the engine reads: quality %q, limits %+v", a.configText(ctx, "gamingQuality", ""), a.gameLimits(ctx))
	}

	// Anything refused leaves everything as it was.
	for name, bad := range map[string]map[string]string{
		"an unknown setting": {"gamingQuality": "low", "brandName": "x"},
		"a region twice":     {"gamingRegionOrder": "japan,japan"},
		"too many at once":   {"gamingQuality": "low", "gamingMaxSessions": "99"},
		"not an address":     {"gamingPublicIp": "example.org"},
		"not a runner":       {"gamingRunner": "/bin/sh"},
	} {
		if w := asWho(t, h, "PUT", "/gaming/settings", adminID, bad); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if a.configText(ctx, "gamingQuality", "") != "high" {
		t.Fatal("a refused change was kept in part")
	}

	// "" is not set.
	if w := asWho(t, h, "PUT", "/gaming/settings", adminID, map[string]string{"gamingQuality": ""}); w.Code != 200 {
		t.Fatalf("clearing: %d", w.Code)
	}
	if ok, _ := a.Config(ctx, "gamingQuality", new(string)); ok {
		t.Fatal("an empty setting was kept")
	}
}
