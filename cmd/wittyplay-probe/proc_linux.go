// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build linux

package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procInfo says how much memory a process holds, and how many files it has open.
func procInfo(pid int) (mb float64, handles int) {
	if pid == 0 {
		return 0, 0
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
		if f := strings.Fields(string(b)); len(f) > 1 {
			pages, _ := strconv.ParseFloat(f[1], 64)
			mb = pages * float64(os.Getpagesize()) / (1 << 20)
		}
	}
	if ents, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid)); err == nil {
		handles = len(ents)
	}
	return mb, handles
}

// alive says whether a process is still there.
func alive(pid int) bool {
	if pid == 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// procCPU is the processor time a process has used, in seconds, user and kernel together.
func procCPU(pid int) float64 {
	if pid == 0 {
		return 0
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// The name in brackets may hold spaces; the fields after it are counted from there.
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 {
		s = s[i+1:]
	}
	f := strings.Fields(s)
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return (ut + st) / 100 // USER_HZ, 100 on every Linux there is
}

// sysTimes is the whole machine's idle and total processor time so far, in ticks.
func sysTimes() (idle, total float64) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	for i, v := range f[1:] {
		n, _ := strconv.ParseFloat(v, 64)
		total += n
		if i == 3 || i == 4 { // idle and iowait
			idle += n
		}
	}
	return idle, total
}
