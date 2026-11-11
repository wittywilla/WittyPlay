// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package ipc

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestTouchesAreReadOnlyWhenWhole(t *testing.T) {
	good := []byte{TouchesMark, 2, 7, 1, 0x10, 0x00, 0xF0, 0xFF, 9, 0, 0, 0, 0, 0}
	list, ok := ParseTouches(good)
	if !ok || len(list) != 2 || list[0] != (Touch{ID: 7, Pressed: true, X: 16, Y: -16}) || list[1].Pressed {
		t.Fatalf("%v %+v", ok, list)
	}
	if l, ok := ParseTouches([]byte{TouchesMark, 0}); !ok || len(l) != 0 {
		t.Error("no fingers is a state too: nothing touching")
	}
	for _, bad := range [][]byte{nil, {TouchesMark}, {TouchesMark, 1, 0}, {TouchesMark, 11}, good[:len(good)-1], {PointerMark, 1, 0, 0, 0, 0, 0, 0}} {
		if _, ok := ParseTouches(bad); ok {
			t.Errorf("%v should be refused", bad)
		}
	}
	if limit(CmdTouches) != 2+MaxTouches*TouchSize {
		t.Error("the runner must take the largest list")
	}
}

func TestMotionIsInTheUnitsCoresTake(t *testing.T) {
	p := make([]byte, MotionSize)
	p[0], p[1] = MotionMark, 3
	binary.LittleEndian.PutUint16(p[2:], 4096)                 // 1 g
	binary.LittleEndian.PutUint16(p[6:], uint16(0x10000-2048)) // -0.5 g
	binary.LittleEndian.PutUint16(p[8:], 16*90)                // 90 degrees a second
	port, accel, gyro, ok := ParseMotion(p)
	if !ok || port != 3 {
		t.Fatal(port, ok)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-3 }
	if !near(float64(accel[0]), 9.80665) || accel[1] != 0 || !near(float64(accel[2]), -4.903325) || !near(float64(gyro[0]), math.Pi/2) {
		t.Fatalf("accel %v gyro %v", accel, gyro)
	}
	if _, _, _, ok := ParseMotion(p[:13]); ok {
		t.Error("short")
	}
	p[0] = 0xE0
	if _, _, _, ok := ParseMotion(p); ok {
		t.Error("wrong mark")
	}
}
