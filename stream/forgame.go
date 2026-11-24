// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"maps"
	"slices"

	"github.com/wittywilla/wittyplay"
)

// forThisGame makes what a particular file needs of how its core starts. These can't be changed
// by a setting, as the game can't run without them.
func forThisGame(opt Options) Options {
	if opt.Console != "n64" || opt.CoreID != wittyplay.LibdragonCore || !wittyplay.IsLibdragon(opt.ROM) {
		return opt
	}
	notes := opt.Notices[:len(opt.Notices):len(opt.Notices)]
	if opt.GPU {
		opt.Notices = append(notes, wittyplay.LibdragonNotice)
		// A card: parallel-RDP over Vulkan, the one renderer of this core that draws these games.
		opt.fixedOptions = libdragonCardOptions()
		opt.CoreOptions = withFixed(opt.CoreOptions, opt.fixedOptions)
		if !slices.Contains(opt.RunnerArgs, "-vulkan") {
			opt.RunnerArgs = append(slices.Clone(opt.RunnerArgs), "-vulkan")
		}
		return opt
	}
	// No card: this core's angrylion leaves these games black, so say that plainly at the start, and
	// only that: the usual "the processor draws this game" note would promise a picture.
	opt.Notices = []string{libdragonNoCardNotice}
	opt.fixedOptions = wittyplay.LibdragonOptions()
	opt.CoreOptions = withFixed(opt.CoreOptions, opt.fixedOptions)
	opt.RunnerArgs = slices.DeleteFunc(slices.Clone(opt.RunnerArgs), func(a string) bool { return a == "-gpu" })
	return opt
}

// withFixed lays what a game can't run without over any other options.
func withFixed(options, fixed map[string]string) map[string]string {
	if len(fixed) == 0 {
		return options
	}
	out := make(map[string]string, len(options)+len(fixed))
	maps.Copy(out, options)
	maps.Copy(out, fixed)
	return out
}

// libdragonCardOptions are what ParaLLEl N64 takes to run a libdragon game on a graphics card:
// parallel-RDP and its RSP, which emulate the real graphics processor over Vulkan rather than
// guessing at what's drawn. On an RTX 3070 the 240p Test Suite shows its menu with them, where
// this core's angrylion (wittyplay.LibdragonOptions) leaves every frame black but for one column
// of pixels, on Windows and Linux alike.
func libdragonCardOptions() map[string]string {
	return map[string]string{
		"parallel-n64-gfxplugin": "parallel",
		"parallel-n64-rspplugin": "parallel",
		"parallel-n64-cpucore":   "cached_interpreter",
	}
}

// libdragonNoCardNotice is said on the start screen instead of wittyplay.LibdragonNotice on a
// server with no graphics card, where nothing in this core draws such a game: told before, rather
// than a black screen.
const libdragonNoCardNotice = "This game needs a graphics card on the server to show a picture."
