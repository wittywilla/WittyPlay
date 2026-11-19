// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// OnCoreThread runs fn on a thread the Go runtime didn't create, and returns when it has
// finished, as on Linux (thread_linux.go): cores that run the game on stacks of their own
// (Mupen64Plus-Next's coroutine) call back from there, which the runtime allows only on a thread
// it didn't create. On the runner's own thread such a callback died at random ("morestack on g0"),
// when it happened to need more stack than the runtime thought the thread had left.
func OnCoreThread(fn func()) error {
	entry := purego.NewCallback(func(uintptr) uintptr {
		fn()
		return 0
	})
	var id uint32
	h, _, _ := procCreateThread.Call(0, coreThreadStack, entry, 0, stackSizeIsReservation, uintptr(unsafe.Pointer(&id)))
	if h == 0 {
		return errCoreThread
	}
	// Waited on in the system, not on a channel: until the thread first calls into Go, the
	// runtime would take a wait on a channel for a deadlock.
	_, _ = syscall.WaitForSingleObject(syscall.Handle(h), syscall.INFINITE)
	_ = syscall.CloseHandle(syscall.Handle(h))
	return nil
}

var procCreateThread = syscall.NewLazyDLL("kernel32.dll").NewProc("CreateThread")

// Emulators and graphics drivers use a good deal more stack than Go code does; this much is
// reserved, and only what's used is committed.
const (
	coreThreadStack        = 16 << 20
	stackSizeIsReservation = 0x10000 // STACK_SIZE_PARAM_IS_A_RESERVATION
)
