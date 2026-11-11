// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// maxCoreSize bounds what's read into memory to take the mark off. The largest core there
// is today is under 100 MB.
const maxCoreSize = 1 << 30

// withoutExecStack opens a core that asks for an executable stack, from a private copy
// without that mark. No core file changes, so what an administrator placed or the
// installer recorded stays as it was, and every core gets the same treatment wherever it
// came from. The stack is never made executable: on an older glibc that would have happened
// silently, and on a current one it's refused. A core that asks for it and really does run
// code on its stack is stopped by the processor when it tries, and that's its crash.
func withoutExecStack(path, scratch string, open func(string) (uintptr, error)) (uintptr, error) {
	f, err := os.Open(path)
	if err != nil {
		return open(path) // whoever opens it says what's wrong
	}
	flagsAt, order, wanted := execStackMark(f)
	if !wanted {
		f.Close()
		return open(path)
	}
	st, err := f.Stat()
	if err != nil || st.Size() > maxCoreSize {
		f.Close()
		return open(path)
	}
	b := make([]byte, st.Size())
	_, err = f.ReadAt(b, 0)
	f.Close()
	if err != nil {
		return 0, fmt.Errorf("reading the core: %w", err)
	}
	if err := clearExecStack(b, flagsAt, order); err != nil {
		return 0, err
	}
	if lib, err := openFromMemory(b, open); err == nil {
		return lib, nil
	}
	// Some systems forbid running anything from memory like that.
	return openFromScratch(b, scratch, open)
}

// openFromMemory opens a library that has no file anywhere that anyone else can reach.
func openFromMemory(b []byte, open func(string) (uintptr, error)) (uintptr, error) {
	// Kernels before 6.3 don't know MFD_EXEC, and mean the same without it.
	fd, err := unix.MemfdCreate("wittyplay-core", unix.MFD_CLOEXEC|unix.MFD_EXEC)
	if err == unix.EINVAL {
		fd, err = unix.MemfdCreate("wittyplay-core", unix.MFD_CLOEXEC)
	}
	if err != nil {
		return 0, err
	}
	defer unix.Close(fd)
	if err := writeAll(fd, b); err != nil {
		return 0, err
	}
	return open(fmt.Sprintf("/proc/self/fd/%d", fd))
}

// openFromScratch opens the copy from the save folder, the one place a game may write,
// and removes it straight away: once it's mapped, the name isn't needed.
func openFromScratch(b []byte, scratch string, open func(string) (uintptr, error)) (uintptr, error) {
	if scratch == "" {
		return 0, fmt.Errorf("this system won't run a core from memory, and there's no folder to run it from")
	}
	f, err := os.CreateTemp(scratch, ".core-*.so")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	return open(f.Name())
}

func writeAll(fd int, b []byte) error {
	for len(b) > 0 {
		n, err := unix.Write(fd, b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}
