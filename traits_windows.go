// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

// Dolphin's fast memory maps the console's memory straight into the emulator's and catches the
// faults that hit what isn't mapped. It stays off elsewhere, where the runner can't recover from
// those faults (docs/PLATFORMS-AND-PLAN.md). On Windows the core catches them itself. Fast memory
// needs its arena, the mapping itself: without it the option does nothing. Measured in Mario
// Kart Wii on an i7-12700H: 26 to 28 frames a second of 60 in a race, with the emulation thread
// using a whole core and the sound breaking up; with both, 60, using under half a core.
func init() {
	traits["dolphin"].Options["dolphin_fastmem"] = "enabled"
	traits["dolphin"].Options["dolphin_fastmem_arena"] = "enabled"
}
