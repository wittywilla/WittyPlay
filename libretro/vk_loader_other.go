// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows && !linux

package libretro

import "errors"

// openVulkanLoader: Vulkan isn't offered here. A Mac draws through OpenGL (gl_darwin.go).
func openVulkanLoader() (uintptr, error) {
	return 0, errors.New("Vulkan isn't offered on this system")
}
