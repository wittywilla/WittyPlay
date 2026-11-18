// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"testing"

	"github.com/wittywilla/wittyplay/engine"
)

func TestSensorDots(t *testing.T) {
	mid := func(p engine.PointerState) (x, y int) {
		x = (int(sensorDot(p, 0, 0)) + int(sensorDot(p, 1, 0))) / 2
		return x, int(sensorDot(p, 0, 1))
	}
	cx, cy := mid(engine.PointerState{})
	if d := cx - 0x7fff/2; d < -2 || d > 2 {
		t.Errorf("pointer in the middle: bar at x %d, want the middle", cx)
	}
	rx, _ := mid(engine.PointerState{X: 0x7fff})
	_, dy := mid(engine.PointerState{Y: 0x7fff})
	if rx >= cx || dy <= cy {
		t.Errorf("the camera should see the bar move left as the pointer goes right, and down with it: right %d, down %d, middle %d,%d", rx, dy, cx, cy)
	}
	// Both dots stay in the camera's view at every corner, and nothing else is seen.
	for _, p := range []engine.PointerState{{X: -0x7fff, Y: -0x7fff}, {X: 0x7fff, Y: 0x7fff}} {
		for i := uintptr(0); i < 2; i++ {
			if x, y := sensorDot(p, i, 0), sensorDot(p, i, 1); x <= 0 || x >= 0x7fff || y <= 0 || y >= 0x7fff {
				t.Errorf("dot %d out of view at %+v: %d,%d", i, p, x, y)
			}
			if sensorDot(p, i, 2) != 1 {
				t.Errorf("dot %d not seen", i)
			}
		}
	}
	if sensorDot(engine.PointerState{}, 2, 2) != 0 {
		t.Error("a third dot is seen")
	}
}
