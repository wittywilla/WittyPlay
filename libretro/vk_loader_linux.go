// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import "github.com/ebitengine/purego"

// openVulkanLoader loads the system's Vulkan loader.
func openVulkanLoader() (uintptr, error) {
	return purego.Dlopen("libvulkan.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
}
