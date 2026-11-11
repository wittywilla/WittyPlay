// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"math"
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/wittywilla/wittyplay/engine"
)

// A core asks for the sensor interface, turns a player's accelerometer on, and reads it as a
// C float, through the same entry points a core calls.
func TestSensorsReachACore(t *testing.T) {
	if cbSensorGet == 0 && func() bool { callbacks.Do(makeCallbacks); return cbSensorGet == 0 }() {
		t.Skip("no float results from callbacks on this platform: cores get no sensor interface")
	}
	offered := false
	type want struct {
		port        int
		accel, gyro bool
		hz          int
	}
	var wanted []want
	newTestCore(t, engine.Handlers{
		MotionOffered: func() { offered = true },
		MotionWanted:  func(port int, accel, gyro bool, hz int) { wanted = append(wanted, want{port, accel, gyro, hz}) },
		Motion: func(port int) engine.MotionState {
			return engine.MotionState{Accel: [3]float32{float32(port) + 0.5, -9.80665, 3}, Gyro: [3]float32{0.25, 0, -1}}
		},
	})
	var iface [2]uintptr
	if !env(envGetSensorIface, unsafe.Pointer(&iface)) || iface[0] == 0 || iface[1] == 0 || !offered {
		t.Fatal("the sensor interface should be offered")
	}
	if r, _, _ := purego.SyscallN(iface[0], 1, sensorAccelOn, 60); r&0xff != 1 {
		t.Fatal("turning the accelerometer on should succeed")
	}
	purego.SyscallN(iface[0], 1, sensorGyroOn, 30)
	purego.SyscallN(iface[0], 1, sensorAccelOff, 0)
	if len(wanted) != 3 || wanted[0] != (want{1, true, false, 60}) || wanted[1] != (want{1, true, true, 30}) || wanted[2] != (want{1, false, true, 0}) {
		t.Fatalf("what the game wanted: %+v", wanted)
	}
	if r, _, _ := purego.SyscallN(iface[0], 1, 4, 60); r&0xff != 0 {
		t.Error("a light sensor isn't offered")
	}
	var get func(port, id uint32) float32
	purego.RegisterFunc(&get, iface[1])
	for _, c := range []struct {
		port, id uint32
		want     float32
	}{{1, sensorAccelX, 1.5}, {1, sensorAccelX + 1, -9.80665}, {2, sensorAccelX + 2, 3}, {0, sensorGyroX, 0.25}, {0, sensorGyroX + 2, -1}, {0, sensorIllum, 0}, {engine.MaxPorts, 0, 0}} {
		if got := get(c.port, c.id); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("port %d sensor %d: %v, want %v", c.port, c.id, got, c.want)
		}
	}
}

func TestLEDsReachTheHost(t *testing.T) {
	var got [][2]int
	newTestCore(t, engine.Handlers{LED: func(i, s int) { got = append(got, [2]int{i, s}) }})
	var set uintptr
	if !env(envGetLEDIface, unsafe.Pointer(&set)) || set == 0 {
		t.Fatal("the LED interface should be offered")
	}
	purego.SyscallN(set, 2, 1)
	purego.SyscallN(set, 0, 255)
	if len(got) != 2 || got[0] != [2]int{2, 1} || got[1] != [2]int{0, 255} {
		t.Fatalf("lights: %v", got)
	}
}

// Several fingers on a touch screen: libretro's pointer indexes them, and its count says how many.
func TestSeveralFingersOnATouchScreen(t *testing.T) {
	inputs := [engine.MaxPorts]engine.PadState{}
	fingers := []engine.PointerState{{X: -100, Y: 200, Pressed: true}, {X: 3000, Y: -4000, Pressed: true}}
	newTestCore(t, engine.Handlers{
		Input: func() *[engine.MaxPorts]engine.PadState { return &inputs },
		Pointer: func() engine.PointerState {
			if len(fingers) == 0 {
				return engine.PointerState{}
			}
			return fingers[0]
		},
		Touches: func() []engine.PointerState { return fingers },
	})
	purego.SyscallN(cbPoll)
	state := func(port, index, id uintptr) int16 {
		r, _, _ := purego.SyscallN(cbState, port, devicePointer, index, id)
		return int16(uint16(r))
	}
	if state(0, 0, 3) != 2 {
		t.Error("two fingers down")
	}
	if state(0, 1, 0) != 3000 || state(0, 1, 1) != -4000 || state(0, 1, 2) != 1 || state(0, 0, 0) != -100 {
		t.Error("each finger by its index")
	}
	if state(0, 2, 2) != 0 || state(1, 0, 2) != 0 {
		t.Error("no third finger, and the screen is the first player's")
	}
	fingers = fingers[:0]
	purego.SyscallN(cbPoll)
	if state(0, 0, 3) != 0 || state(0, 0, 2) != 0 {
		t.Error("nothing touching")
	}
}
