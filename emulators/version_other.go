// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows

package emulators

// fileVersion is a program's own version, where the system keeps one in the file: Windows
// does, others don't.
func fileVersion(string) string { return "" }
