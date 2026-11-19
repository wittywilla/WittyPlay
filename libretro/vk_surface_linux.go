// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"fmt"
	"unsafe"
)

// Some cores won't draw without a surface to present to, though nothing is ever shown: Dolphin
// builds its stand-in swapchain on the one it's given. A server has no display, so it's a
// headless surface (VK_EXT_headless_surface), which Mesa's drivers and NVIDIA's have.

var vkSurfaceExtensions = []string{"VK_KHR_surface", "VK_EXT_headless_surface"}

type vkHeadlessSurfaceCreateInfo struct {
	sType uint32
	pNext uintptr
	flags uint32
}

const vkStructHeadlessSurfaceCreateInfo = 1000256000

func (v *vkOut) createSurface() error {
	create := v.instanceProc(v.instance, "vkCreateHeadlessSurfaceEXT")
	if create == 0 {
		return fmt.Errorf("no vkCreateHeadlessSurfaceEXT")
	}
	info := vkHeadlessSurfaceCreateInfo{sType: vkStructHeadlessSurfaceCreateInfo}
	if r := call(create, v.instance, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&v.surface))); int32(r) != vkSuccess {
		return fmt.Errorf("vkCreateHeadlessSurfaceEXT failed (%d)", int32(r))
	}
	return nil
}

func (v *vkOut) destroyWindow() {}
