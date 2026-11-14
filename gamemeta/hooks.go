// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamemeta

import "io"

// What working out a game's identity borrows from WittyPlay's game identification (its gameid
// package), set there in an init so that this package doesn't depend on it.

// Played says an archive entry's name is a plain dump or a prerelease (beta, demo): what would be
// played, rather than a hack, a translation or a bad dump. Unset, every entry counts.
var Played = func(name string) bool { return true }

// SniffSerial reads the console and product code a Wii U, PS3 or Xbox disc says it is, in the form
// the libretro lists use. Unset, those discs are known by their name alone.
var SniffSerial func(r io.ReaderAt, size int64) (console, serial string)
