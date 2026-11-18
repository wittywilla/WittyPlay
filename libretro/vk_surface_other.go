// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows && !linux

package libretro

import "errors"

var vkSurfaceExtensions []string

func (v *vkOut) createSurface() error { return errors.New("no surface here") }

func (v *vkOut) destroyWindow() {}
