// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import "syscall"

var procGetCurrentThreadID = syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentThreadId")

// currentThreadID is the system's number for the thread this runs on, for tracing.
func currentThreadID() uintptr {
	id, _, _ := procGetCurrentThreadID.Call()
	return id
}
