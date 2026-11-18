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

// procCPU is the processor time a process has used, in seconds.
func procCPU(pid int) float64 {
	if pid == 0 {
		return 0
	}
	out, err := exec.Command("ps", "-o", "time=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	var secs float64
	for _, part := range strings.Split(strings.TrimSpace(string(out)), ":") {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(part, "-", ""), 64)
		secs = secs*60 + v
	}
	return secs
}

// sysTimes isn't measured here.
func sysTimes() (idle, total float64) { return 0, 0 }
