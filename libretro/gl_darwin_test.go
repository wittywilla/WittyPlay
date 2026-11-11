// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"errors"
	"testing"
)

// What each kind of request gets on a Mac: 4.1 core for anything modern, 2.1 for old
// compatibility requests, and a plain "no usable graphics card" for what macOS can't give.
func TestWhatAMacGivesACoreThatAsks(t *testing.T) {
	for _, c := range []struct {
		req  glRequest
		want int32
		no   bool
	}{
		{glRequest{major: 3, minor: 3, core: true}, cglProfileGL4Core, false}, // GLideN64, PPSSPP
		{glRequest{major: 3, minor: 2, core: true}, cglProfileGL4Core, false}, // Play!
		{glRequest{major: 4, minor: 1, core: true}, cglProfileGL4Core, false}, // Panda3DS
		{glRequest{major: 3, minor: 0}, cglProfileGL4Core, false},             // compatibility 3.x: core is the nearest
		{glRequest{}, cglProfileLegacy, false},                                // a core that didn't say
		{glRequest{major: 2, minor: 1}, cglProfileLegacy, false},
		{glRequest{major: 4, minor: 3, core: true}, 0, true}, // more than macOS has
		{glRequest{es: true, major: 3}, 0, true},             // no OpenGL ES on a Mac
	} {
		got, err := cglProfile(c.req)
		if c.no {
			if !errors.Is(err, ErrNoGPU) {
				t.Errorf("%+v: %v, want a refusal", c.req, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: %#x %v, want %#x", c.req, got, err, c.want)
		}
	}
}
