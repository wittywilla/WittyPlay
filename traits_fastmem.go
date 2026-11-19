// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build windows || linux

package wittyplay

// Dolphin's fast memory maps the console's memory straight into the emulator's and catches the
// faults on what isn't mapped; its arena is that mapping, and without it the option does nothing.
// Both are the core's own defaults, and the runner had turned them off everywhere. On Windows the
// core catches those faults itself; on Linux they work in the runner's sandbox (the core gets a
// large address space and its own shared memory folder, see traits.go). macOS stays as it was,
// untested there.
//
// Measured on an i7-12700H with an RTX 3070, Cars (GameCube) in a race, from a saved state: 25 to
// 28 frames a second of 60 with the emulation thread using a whole core, against 60 using a third
// of one. In WSL, on the card: 35 against 60. Mario Kart Wii in a race (#65, which had these for
// Windows alone): 26 to 28 against 60, using under half a core, with the sound no longer breaking up.
//
// Shaders compile in the background, with Dolphin's uber shaders drawing until each is ready
// ("Async (UberShaders)", 2): the default compiles every new one before the frame goes on, which
// stutters a game whenever it shows something new.
func init() {
	o := traits["dolphin"].Options
	o["dolphin_fastmem"] = "enabled"
	o["dolphin_fastmem_arena"] = "enabled"
	o["dolphin_shader_compilation_mode"] = "2"
}
