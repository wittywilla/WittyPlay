// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import "syscall"

// openVulkanLoader loads the system's Vulkan loader, which every graphics driver with Vulkan
// installs.
func openVulkanLoader() (uintptr, error) {
	h, err := syscall.LoadLibrary("vulkan-1.dll")
	return uintptr(h), err
}
