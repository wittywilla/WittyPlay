//go:build !((amd64 || arm64) && !windows)

package libretro

// No float can come back through a callback here, so cores are told there's no sensor
// interface, and play without motion.
var cbSensorSet, cbSensorGet uintptr

func makeSensorCallbacks() {}
