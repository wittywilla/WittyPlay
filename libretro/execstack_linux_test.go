// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real library that asks for an executable stack: refused by a current glibc, and opened
// here without the stack becoming executable. It needs a C compiler to make one.
func TestWithoutExecStackOpensARealLibrary(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("no C compiler here")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "lib.c")
	if err := os.WriteFile(src, []byte("int answer(void) { return 42; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "marked.so")
	if out, err := exec.Command(gcc, "-shared", "-fPIC", "-z", "execstack", "-o", lib, src).CombinedOutput(); err != nil {
		t.Skipf("can't make a library: %v\n%s", err, out)
	}
	before, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}

	var got string
	opened, err := withoutExecStack(lib, dir, func(p string) (uintptr, error) {
		got = p
		f, err := elf.Open(p)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		for _, ph := range f.Progs {
			if ph.Type == elf.PT_GNU_STACK && ph.Flags&elf.PF_X != 0 {
				t.Error("what was opened still asks for an executable stack")
			}
		}
		return 7, nil
	})
	if err != nil || opened != 7 {
		t.Fatalf("opened %d, %v", opened, err)
	}
	if !strings.HasPrefix(got, "/proc/self/fd/") {
		t.Errorf("opened %s, a copy in memory was expected", got)
	}
	after, _ := os.ReadFile(lib)
	if string(before) != string(after) {
		t.Error("the core file itself changed")
	}

	// And the fallback, for a system that won't run anything from memory.
	b := []byte("anything")
	if _, err := openFromScratch(b, dir, func(p string) (uintptr, error) {
		data, err := os.ReadFile(p)
		if err != nil || string(data) != string(b) {
			t.Errorf("the scratch copy reads %q, %v", data, err)
		}
		return 1, nil
	}); err != nil {
		t.Error(err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".core-*")); len(left) != 0 {
		t.Errorf("the scratch copy stayed: %v", left)
	}
}

// A core that doesn't ask is opened from where it is, with nothing copied.
func TestWithoutExecStackLeavesOthersAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.so")
	if err := os.WriteFile(path, fakeELF(elfClass64, elfLE(), 6, 1, ptGNUStack), 0o644); err != nil {
		t.Fatal(err)
	}
	var got string
	if _, err := withoutExecStack(path, "", func(p string) (uintptr, error) { got = p; return 1, nil }); err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Errorf("opened %s, not the file", got)
	}
}
