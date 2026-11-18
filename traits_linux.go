// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

// Dolphin's fast memory maps the console's memory straight into the emulator's and catches the
// faults on what isn't mapped; its arena is that mapping, and without it the option does nothing.
// Both are the core's own defaults, and the runner turned them off everywhere. On Linux they work
// in the runner's sandbox (the core gets a large address space and its own shared memory folder,
// see traits.go). Measured in WSL on an i7-12700H, Cars (GameCube) through the sandboxed runner,
// drawn on the card: 35 frames a second of 60 with the emulation thread using a whole core,
// against 60 using a quarter of one. macOS stays as it was: untested there.
func init() {
	traits["dolphin"].Options["dolphin_fastmem"] = "enabled"
	traits["dolphin"].Options["dolphin_fastmem_arena"] = "enabled"
}
