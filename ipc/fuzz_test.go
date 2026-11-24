// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package ipc

import "testing"

// Input arrives from people watching a game, over the network, and goes on to the runner. Every
// reader of it must refuse what doesn't fit rather than panic, and never hand back a player or a
// count out of range.
func FuzzInputMessages(f *testing.F) {
	f.Add([]byte{0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{3, 0xff, 0xff, 0x7f, 0xff, 0x80, 0, 0, 0, 0, 0, 200, 100})
	f.Add([]byte{0xFF, 1, 2, 3, 4, 1})
	f.Add([]byte{TouchesMark, 1, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{MotionMark, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, p []byte) {
		if port, _, _, _, _, _, ok := ParseInput(p); ok && (port < 0 || port > 255 || len(p) < InputSize) {
			t.Fatalf("input for player %d from %d bytes", port, len(p))
		}
		_, _ = ParseTriggers(p)
		_ = InputStamp(p)
		if touches, ok := ParseTouches(p); ok && len(touches) > MaxTouches {
			t.Fatalf("%d touches", len(touches))
		}
		if port, _, _, ok := ParseMotion(p); ok && (port < 0 || port > 255) {
			t.Fatalf("motion for player %d", port)
		}
		if _, _, _, ok := ParsePointer(p); ok && len(p) != PointerSize {
			t.Fatalf("a pointer from %d bytes", len(p))
		}
		_, _ = ParseFrameTiming(p)
		_, _, _ = ParseVideo(p)
	})
}
