// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !linux && !windows

package main

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// procInfo says how much memory a process holds. macOS and FreeBSD have no /proc to count open
// files from, so handles is always 0 there.
func procInfo(pid int) (mb float64, handles int) {
	if pid == 0 {
		return 0, 0
	}
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, 0
	}
	kb, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return kb / 1024, 0
}

// alive says whether a process is still there.
func alive(pid int) bool {
	if pid == 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
