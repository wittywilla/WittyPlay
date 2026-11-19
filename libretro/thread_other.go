// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !linux && !windows

package libretro

// OnCoreThread runs fn. Linux and Windows give the core a thread of their own: see
// thread_linux.go.
func OnCoreThread(fn func()) error {
	fn()
	return nil
}
