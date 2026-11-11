// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

type memoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
}

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	getProcessMemory  = kernel32.NewProc("K32GetProcessMemoryInfo")
	getProcessHandleN = kernel32.NewProc("GetProcessHandleCount")
)

// procInfo says how much memory a process holds, and how many handles it has open.
func procInfo(pid int) (mb float64, handles int) {
	if pid == 0 {
		return 0, 0
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, 0
	}
	defer windows.CloseHandle(h)
	var c memoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	if r, _, _ := getProcessMemory.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.cb)); r != 0 {
		mb = float64(c.pagefileUsage) / (1 << 20)
	}
	var n uint32
	if r, _, _ := getProcessHandleN.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r != 0 {
		handles = int(n)
	}
	return mb, handles
}

// alive says whether a process is still there.
func alive(pid int) bool {
	if pid == 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}
