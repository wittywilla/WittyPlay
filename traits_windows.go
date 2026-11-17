// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

// Dolphin's fast memory maps the console's memory straight into the emulator's and catches the
// faults that hit what isn't mapped. It stays off elsewhere, where the runner can't recover from
// those faults (docs/PLATFORMS-AND-PLAN.md). On Windows the core catches them itself, and
// without it a Wii game's single emulation thread kept Mario Kart Wii's menus at 31 to 47 frames
// a second of 60 on a fast machine, with the sound breaking up; with it the game kept its pace.
func init() {
	traits["dolphin"].Options["dolphin_fastmem"] = "enabled"
}
