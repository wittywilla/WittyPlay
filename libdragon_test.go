// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

import (
	"os"
	"path/filepath"
	"testing"
)

// romWith is the start of a Nintendo 64 file, in big-endian order, with these words in its boot code.
func romWith(boot string) []byte {
	b := make([]byte, 0x1000)
	copy(b, []byte{0x80, 0x37, 0x12, 0x40})
	copy(b[0x500:], boot)
	return b
}

func writeROM(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "game.z64")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLibdragonIsFoundInEveryByteOrder(t *testing.T) {
	big := romWith("Libdragon IPL3  Coded by someone")
	swapped := make([]byte, len(big))
	reversed := make([]byte, len(big))
	for i := 0; i+3 < len(big); i += 4 {
		swapped[i], swapped[i+1], swapped[i+2], swapped[i+3] = big[i+1], big[i], big[i+3], big[i+2]
		reversed[i], reversed[i+1], reversed[i+2], reversed[i+3] = big[i+3], big[i+2], big[i+1], big[i]
	}
	for name, b := range map[string][]byte{"big-endian": big, "byte-swapped": swapped, "little-endian": reversed} {
		if !IsLibdragon(writeROM(t, b)) {
			t.Errorf("%s: not found", name)
		}
	}
}

func TestOtherFilesAreNotLibdragon(t *testing.T) {
	later := append(romWith(""), []byte("Libdragon IPL3")...) // past the boot code, so only a game's own data
	notN64 := romWith("Libdragon IPL3")
	notN64[0] = 0x12
	for name, b := range map[string][]byte{"a commercial boot": romWith("nothing of note"), "the words after the boot code": later, "not a Nintendo 64 file": notN64, "too short": {0x80}, "empty": {}} {
		if IsLibdragon(writeROM(t, b)) {
			t.Errorf("%s: taken for libdragon", name)
		}
	}
	if IsLibdragon(filepath.Join(t.TempDir(), "missing.z64")) {
		t.Error("a missing file was taken for libdragon")
	}
}

// Set WITTYPLAY_LIBDRAGON_ROM to a real one to check this against what libdragon really writes.
func TestARealLibdragonFile(t *testing.T) {
	p := os.Getenv("WITTYPLAY_LIBDRAGON_ROM")
	if p == "" {
		t.Skip("no WITTYPLAY_LIBDRAGON_ROM")
	}
	if !IsLibdragon(p) {
		t.Errorf("%s: not found", p)
	}
}
