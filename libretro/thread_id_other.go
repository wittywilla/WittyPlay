// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows

package libretro

// currentThreadID is the system's number for the thread this runs on, for tracing: not known here.
func currentThreadID() uintptr { return 0 }
