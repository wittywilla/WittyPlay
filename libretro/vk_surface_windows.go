// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"fmt"
	"syscall"
	"unsafe"
)

// Some cores won't draw without a surface to present to, though nothing is ever shown: Dolphin
// builds its stand-in swapchain on the one it's given. On Windows a surface needs a window, so it
// gets a hidden one, as an OpenGL context does (gl_windows.go). Nothing calls ShowWindow.

var vkSurfaceExtensions = []string{"VK_KHR_surface", "VK_KHR_win32_surface"}

type vkWin32SurfaceCreateInfo struct {
	sType     uint32
	pNext     uintptr
	flags     uint32
	hinstance uintptr
	hwnd      uintptr
}

const vkStructWin32SurfaceCreateInfo = 1000009000

// createSurface makes a surface on a hidden window.
func (v *vkOut) createSurface() error {
	create := v.instanceProc(v.instance, "vkCreateWin32SurfaceKHR")
	if create == 0 {
		return fmt.Errorf("no vkCreateWin32SurfaceKHR")
	}
	inst, _, _ := procGetModuleHandleW.Call(0)
	name, _ := syscall.UTF16PtrFromString("WittyPlayGL")
	if !wglClassDone {
		cls := wndClassEx{wndProc: procDefWindowProcW.Addr(), instance: inst, name: name, style: 0x20 /* CS_OWNDC */}
		cls.size = uint32(unsafe.Sizeof(cls))
		if a, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&cls))); a == 0 {
			return fmt.Errorf("registering a window class: %w", err)
		}
		wglClassDone = true
	}
	hwnd, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0x02000000|0x04000000, 0, 0, 16, 16, 0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("creating a hidden window: %w", err)
	}
	info := vkWin32SurfaceCreateInfo{sType: vkStructWin32SurfaceCreateInfo, hinstance: inst, hwnd: hwnd}
	if r := call(create, v.instance, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&v.surface))); int32(r) != vkSuccess {
		procDestroyWindow.Call(hwnd)
		return fmt.Errorf("vkCreateWin32SurfaceKHR failed (%d)", int32(r))
	}
	v.window = hwnd
	return nil
}

func (v *vkOut) destroyWindow() {
	if v.window != 0 {
		procDestroyWindow.Call(v.window)
		v.window = 0
	}
}
