// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !linux

package libretro

// OnCoreThread runs fn. Only Linux needs a thread of its own for this: see
// thread_linux.go.
func OnCoreThread(fn func()) error {
	fn()
	return nil
}
