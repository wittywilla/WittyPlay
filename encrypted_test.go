// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRefusal(t *testing.T) {
	const size = 0x400
	cases := []struct {
		name, console string
		data          []byte
		want          string
	}{
		{"game.3ds", "3ds", fakeCCI(fakeNCCH(false, size)), "encrypted"},
		{"game.3ds", "3ds", fakeCCI(fakeNCCH(true, size)), ""},
		{"game.cci", "3ds", fakeCCI(fakeNCCH(false, size)), "encrypted"},
		{"game.cxi", "3ds", fakeNCCH(false, size), "encrypted"},
		{"game.cxi", "3ds", fakeNCCH(true, size), ""},
		{"game.cia", "3ds", fakeCIA(fakeNCCH(true, size), 1), ""}, // the mark says encrypted; the bytes say otherwise
		{"game.cia", "3ds", fakeCIA(scrambled(size), 1), "encrypted"},
		{"game.cia", "3ds", fakeCIA(scrambled(size), 0), "encrypted"}, // no readable program inside
		{"game.cia", "3ds", fakeCIA(fakeNCCH(true, size), 0), ""},     // the server copies the program out
		{"game.3ds", "3ds", []byte("too short to say"), ""},
		{"game.3dsx", "3ds", []byte("homebrew"), ""},
		{"game.3ds", "nds", fakeCCI(fakeNCCH(false, size)), ""},
	}
	for _, c := range cases {
		got := Refusal(writeTemp(t, c.name, c.data), c.console)
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s on %s: %q, want %q", c.name, c.console, got, c.want)
		}
	}
	if Refusal(filepath.Join(t.TempDir(), "missing.3ds"), "3ds") != "" {
		t.Error("a missing file is a different problem")
	}
}

func TestRefusalForCore(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "game.bin")
	if err := os.WriteFile(bin, []byte("ps2 bin data"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Lone .bin on Play! core is refused
	got := RefusalForCore(bin, "ps2", "play")
	if !strings.Contains(got, "single .bin") || !strings.Contains(got, "LRPS2") {
		t.Errorf("lone bin on play core: %q", got)
	}

	// 2. Lone .bin on play_libretro core is refused
	if got := RefusalForCore(bin, "ps2", "play_libretro"); !strings.Contains(got, "single .bin") {
		t.Errorf("lone bin on play_libretro: %q", got)
	}

	// 3. Lone .bin on PCSX2 (LRPS2) is NOT refused
	if got := RefusalForCore(bin, "ps2", "pcsx2"); got != "" {
		t.Errorf("lone bin on pcsx2 should be allowed, got %q", got)
	}

	// 4. ISO on Play! is NOT refused
	iso := filepath.Join(dir, "game.iso")
	if err := os.WriteFile(iso, []byte("ps2 iso data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RefusalForCore(iso, "ps2", "play"); got != "" {
		t.Errorf("iso on play should be allowed, got %q", got)
	}

	// 5. Missing .bin file is NOT refused (different problem)
	if got := RefusalForCore(filepath.Join(dir, "missing.bin"), "ps2", "play"); got != "" {
		t.Errorf("missing bin should not be refused, got %q", got)
	}

	// 6. Generic refusal without core allows .bin (PCSX2 exists)
	if got := RefusalForCore(bin, "ps2", ""); got != "" {
		t.Errorf("generic ps2 refusal should allow bin, got %q", got)
	}
	if got := Refusal(bin, "ps2"); got != "" {
		t.Errorf("Refusal(bin, ps2) should allow bin, got %q", got)
	}

	// 7. .bin with accompanying .cue sheet on Play! warns to open .cue sheet
	cueDir := t.TempDir()
	cueBin := filepath.Join(cueDir, "game.bin")
	cueSheet := filepath.Join(cueDir, "game.cue")
	if err := os.WriteFile(cueBin, []byte("bin data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cueSheet, []byte(`FILE "game.bin" BINARY`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := RefusalForCore(cueBin, "ps2", "play"); !strings.Contains(got, ".cue sheet") {
		t.Errorf("bin with cue on play: %q", got)
	}
}
