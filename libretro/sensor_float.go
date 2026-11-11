// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build (amd64 || arm64) && !windows

package libretro

import "github.com/ebitengine/purego"

// A sensor reading comes back to the core as a C float. purego returns a struct of one float in
// the register a float comes back in, on these platforms: XMM0 on x86-64, S0 on ARM64.
type floatResult struct{ V float32 }

var cbSensorSet, cbSensorGet uintptr

func makeSensorCallbacks() {
	cbSensorSet = purego.NewCallback(onSensorSet)
	cbSensorGet = purego.NewCallback(func(port, id uintptr) floatResult { return floatResult{sensorValue(port, id)} })
}
