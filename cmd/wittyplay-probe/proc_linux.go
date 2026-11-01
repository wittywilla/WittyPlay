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
