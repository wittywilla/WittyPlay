// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !((amd64 || arm64) && !windows)

package libretro

// No float can come back through a callback here, so cores are told there's no sensor
// interface, and play without motion.
var cbSensorSet, cbSensorGet uintptr

func makeSensorCallbacks() {}
