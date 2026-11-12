// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fileVersion is the version a Windows program's resources give, as major.minor.build.
func fileVersion(p string) string {
	size, err := windows.GetFileVersionInfoSize(p, nil)
	if err != nil || size == 0 || size > 1<<20 {
		return ""
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(p, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return ""
	}
	var info *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&info), &n); err != nil || info == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", info.FileVersionMS>>16, info.FileVersionMS&0xffff, info.FileVersionLS>>16)
}
