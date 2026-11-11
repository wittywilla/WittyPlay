// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAMemoryCardCoreGetsASystemFolderOfItsOwn(t *testing.T) {
	system, save := t.TempDir(), t.TempDir()
	shared := filepath.Join(system, "pcsx2", "bios")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chosen.BIN", "chosen.NVM", "other.bin", "other.nvm"} {
		if err := os.WriteFile(filepath.Join(shared, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(system, "pcsx2", "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	support := filepath.Join("pcsx2", "resources", "support.yaml")
	if err := os.WriteFile(filepath.Join(system, support), []byte("support"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, release, err := ownSystem("pcsx2", system, save, map[string]string{"pcsx2_bios": "chosen.BIN"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(save, "system"); dir != want {
		t.Fatalf("system folder %q, want %q", dir, want)
	}
	copies := filepath.Join(dir, "pcsx2", "bios")
	for name, there := range map[string]bool{"chosen.BIN": true, "chosen.NVM": true, "other.bin": false, "other.nvm": false} {
		if _, err := os.Stat(filepath.Join(copies, name)); (err == nil) != there {
			t.Errorf("%s copied: %v, want %v", name, err == nil, there)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, support)); string(b) != "support" {
		t.Errorf("the core's support files weren't copied: %q", b)
	}
	// A copy, so nothing a core does to it reaches the BIOS everyone uses.
	if err := os.WriteFile(filepath.Join(copies, "chosen.BIN"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(shared, "chosen.BIN")); string(b) != "chosen.BIN" {
		t.Fatalf("the shared BIOS changed: %q", b)
	}
	card := filepath.Join(dir, "pcsx2", "memcards", "game.ps2")
	if err := os.WriteFile(card, []byte("a save"), 0o600); err != nil {
		t.Fatalf("a memory card can't go there: %v", err)
	}

	release()
	if _, err := os.Stat(copies); !os.IsNotExist(err) {
		t.Fatalf("the BIOS copy stayed in the save folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, support)); !os.IsNotExist(err) {
		t.Fatalf("the support files stayed in the save folder: %v", err)
	}
	if b, _ := os.ReadFile(card); string(b) != "a save" {
		t.Fatalf("the memory card went with it: %q", b)
	}

	// The next time, the card is still there and the BIOS is back.
	if _, _, err := ownSystem("pcsx2", system, save, map[string]string{"pcsx2_bios": "chosen.BIN"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(copies, "chosen.BIN")); string(b) != "chosen.BIN" {
		t.Fatalf("BIOS copy %q", b)
	}
	if b, _ := os.ReadFile(card); string(b) != "a save" {
		t.Fatalf("the memory card changed: %q", b)
	}
}

func TestOtherCoresKeepTheSharedSystemFolder(t *testing.T) {
	system, save := t.TempDir(), t.TempDir()
	dir, release, err := ownSystem("snes9x", system, save, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if dir != system {
		t.Fatalf("system folder %q, want the shared one", dir)
	}
	if ents, _ := os.ReadDir(save); len(ents) != 0 {
		t.Fatalf("made %d things in the save folder", len(ents))
	}
}

// What a core works out, such as compiled shaders, costs seconds of stutter to work out again, so
// it stays with the save between games. The firmware beside it doesn't.
func TestOwnSystemKeepsTheCache(t *testing.T) {
	system, save := t.TempDir(), t.TempDir()
	dir, release, err := ownSystem("pcsx2", system, save, nil)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "pcsx2", "cache", "shaders.bin")
	other := filepath.Join(dir, "pcsx2", "other", "thing.bin")
	for _, p := range []string{cache, other} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	release()
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("the shader cache went at the end of the game: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Errorf("anything else should go: %v", err)
	}
	if _, _, err := ownSystem("pcsx2", system, save, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("the next game's set-up took the shader cache away: %v", err)
	}
}
