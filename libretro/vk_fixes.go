// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"sync/atomic"

	"github.com/ebitengine/purego"
)

// Core fixes the runner makes to what a core gets from Vulkan, through the shim the core looks its
// functions up with (vk_shim.go). Nothing in the core's file is changed. FixCores turns them on.

var vkFixes atomic.Bool

// FixCores turns the runner's fixes for cores on or off (the runner's -core-fixes).
func FixCores(on bool) { vkFixes.Store(on) }

func vkFixesOn() bool { return vkFixes.Load() }

// vkMaxSurface is the largest picture a stand-in surface takes: every Vulkan device makes images
// at least this large, and it's past any console at 4x.
const vkMaxSurface = 4096

func (s *vkShimState) makeFixes() {
	// A surface with no size of its own. Nothing is ever shown on the runner's surface: it's a
	// hidden window (Windows), whose size a surface reports as the only size a swapchain may have.
	// Dolphin builds its stand-in swapchain at the size its glue sets in currentExtent, then holds
	// it to the surface's smallest and largest sizes, so on a hidden window its pictures were 120 by
	// 0 pixels and every frame was black. Report what a headless surface does: any size.
	s.fns["vkGetPhysicalDeviceSurfaceCapabilitiesKHR"] = purego.NewCallback(func(gpu, surface, caps uintptr) uintptr {
		r := call(s.real("vkGetPhysicalDeviceSurfaceCapabilitiesKHR"), gpu, surface, caps)
		if int32(r) == vkSuccess && caps != 0 {
			// VkSurfaceCapabilitiesKHR: currentExtent 8, minImageExtent 16, maxImageExtent 24.
			c := (*[8]uint32)(ptr(caps))
			c[2], c[3] = 0xffffffff, 0xffffffff
			c[4], c[5] = 1, 1
			c[6], c[7] = vkMaxSurface, vkMaxSurface
		}
		return r
	})
}
