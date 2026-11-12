// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows && !linux && !darwin

package libretro

import "fmt"

// errNoGLHere: this runner makes OpenGL contexts on Windows and Linux only.
var errNoGLHere = fmt.Errorf("%w: hardware-rendered games run on Windows, Linux and macOS servers", ErrNoGPU)

func newGLPlatform(req glRequest) (glPlatform, error) { return nil, errNoGLHere }

// glUnavailable says why no core can have a context here, whatever it asks for.
func glUnavailable() error { return errNoGLHere }

func prepareGPU() {}
