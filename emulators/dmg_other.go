// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !darwin

package emulators

import "errors"

// unpackDMG: only a Mac opens a Mac's disk image, and only a Mac's downloads are ones.
func unpackDMG(file, dir, entry string, limit int64) error {
	return errors.New("a Mac disk image can only be opened on a Mac")
}
