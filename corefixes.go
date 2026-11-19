// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

// Core fixes are the runner's corrections to what a core gets, made on this machine as the core
// runs: nothing in a core's file is changed, and no core is built here. A core looks up every
// graphics function it uses through the runner, so the runner can hand it a stand-in that fixes
// what goes wrong (libretro/vk_fixes.go). They're on unless an administrator turns them off
// (Settings, Games, Advanced, gamingCoreFixes); with them off, a core gets what it would from
// any frontend, and whatever needed a fix goes back to how it was.

// CoreFix is one fix: what it's called, which cores it's for, and what it does.
type CoreFix struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	About string   `json:"about"`
	Cores []string `json:"cores"`
}

// CoreFixes are every fix the runner makes.
var CoreFixes = []CoreFix{
	{ID: "vulkan-surface-size", Title: "Dolphin draws with Vulkan",
		About: "Dolphin builds its pictures at the size of the surface it's given. The runner's is a hidden window, whose size Windows makes 120 by 0 pixels, so on Vulkan every frame was black. The runner says the surface takes any size, as a server's headless one does, and Dolphin draws at its real size.",
		Cores: []string{"dolphin"}},
}

// FixesFor are the fixes made for a core.
func FixesFor(coreID string) []CoreFix {
	var out []CoreFix
	for _, f := range CoreFixes {
		for _, c := range f.Cores {
			if c == coreID {
				out = append(out, f)
				break
			}
		}
	}
	return out
}
