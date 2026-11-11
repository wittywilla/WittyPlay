// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !linux && !windows

package libretro

// withoutExecStack has nothing to do here: glibc's rule is Linux's.
func withoutExecStack(path, _ string, open func(string) (uintptr, error)) (uintptr, error) {
	return open(path)
}
