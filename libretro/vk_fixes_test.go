// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"testing"
	"unsafe"

	"github.com/ebitengine/purego"
)

// A hidden window's surface says it is 120 by 0 pixels and takes no other size; through the fix,
// it takes any size, as a headless one does.
func TestSurfaceTakesAnySize(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("Vulkan is offered on 64-bit systems only")
	}
	s := &vkShimState{orig: map[string]uintptr{}, fns: map[string]uintptr{}}
	s.orig["vkGetPhysicalDeviceSurfaceCapabilitiesKHR"] = purego.NewCallback(func(gpu, surface, caps uintptr) uintptr {
		c := (*[8]uint32)(ptr(caps))
		c[0], c[1] = 2, 8
		c[2], c[3], c[4], c[5], c[6], c[7] = 120, 0, 120, 0, 120, 0
		return 0
	})
	s.makeFixes()
	caps := new([13]uint32) // on the heap: a callback can move the stack
	if r := call(s.fns["vkGetPhysicalDeviceSurfaceCapabilitiesKHR"], 1, 2, uintptr(unsafe.Pointer(caps))); int32(r) != vkSuccess {
		t.Fatal(r)
	}
	if caps[2] != 0xffffffff || caps[3] != 0xffffffff || caps[4] != 1 || caps[5] != 1 || caps[6] != vkMaxSurface || caps[7] != vkMaxSurface || caps[0] != 2 || caps[1] != 8 {
		t.Fatalf("capabilities: %v", caps)
	}
}
