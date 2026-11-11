// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows

package libretro

import "github.com/ebitengine/purego"

// Some cores use the maths library without saying so, because the frontends they
// are built for link it themselves. This runner doesn't, so it opens it for them.
var mathLibraries = []string{"libm.so.6", "libm.so", "libm.dylib"}

// openLibrary opens a core. scratch is a folder it may write to if it has to.
func openLibrary(path, scratch string) (uintptr, error) {
	for _, name := range mathLibraries {
		if _, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL); err == nil {
			break
		}
	}
	return withoutExecStack(path, scratch, func(p string) (uintptr, error) {
		return purego.Dlopen(p, purego.RTLD_NOW|purego.RTLD_LOCAL)
	})
}

func symbol(lib uintptr, name string) (uintptr, error) { return purego.Dlsym(lib, name) }

func closeLibrary(lib uintptr) { _ = purego.Dlclose(lib) }
