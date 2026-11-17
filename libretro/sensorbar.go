// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import "github.com/wittywilla/wittyplay/engine"

// A Wii Remote finds where it points from what its camera sees: the two glowing ends of the sensor
// bar. Dolphin's core can take those dots from the frontend ("IR passthrough", the option below)
// instead of working them out from an angle it is told to turn the remote by, which leans on
// three settings (yaw, pitch and the bar's height) that are wrong in a different way for every
// window and game. Here the dots are placed so that the pointer lands on the screen where the
// player's mouse or finger is.
//
// The core reads pointer indices 0 to 3 as the camera's dots: X and Y from 0 to 0x7fff across its
// field, and "pressed" for a dot it can see.
const sensorBarOption = "dolphin_ir_passthrough"

// Where the dots go. The camera sees the bar move the other way to the pointer: pointing right
// puts the bar on the left of its picture. The screen spans sensorSpanX and sensorSpanY of the
// camera's field, which keeps both dots in view at every edge, with the bar at sensorRest when the
// pointer is in the middle of the screen. The bar is sensorGap of the field wide, as it looks from
// a couple of metres away.
const (
	sensorSpanX, sensorSpanY = 0.62, 0.62
	sensorRestX, sensorRestY = 0.5, 0.5
	sensorGap                = 0.2
)

// sensorDot is the camera's view of the sensor bar for a pointer: dot 0 and dot 1, the bar's
// two ends; nothing else in view.
func sensorDot(p engine.PointerState, index, id uintptr) int16 {
	if index > 1 {
		return 0
	}
	// The pointer, 0 to 1 across the picture.
	px := (float64(p.X) + 0x7fff) / (2 * 0x7fff)
	py := (float64(p.Y) + 0x7fff) / (2 * 0x7fff)
	x := sensorRestX - (px-0.5)*sensorSpanX
	y := sensorRestY - (py-0.5)*sensorSpanY
	if index == 0 {
		x -= sensorGap / 2
	} else {
		x += sensorGap / 2
	}
	switch id {
	case 0:
		return toField(x)
	case 1:
		return toField(y)
	case 2:
		return 1 // in view
	}
	return 0
}

func toField(f float64) int16 {
	f = min(max(f, 0), 1)
	return int16(f * 0x7fff)
}
