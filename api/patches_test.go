// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// anIPS writes byte 'x' at offset 1.
var anIPS = append(append([]byte("PATCH"), 0, 0, 1, 0, 1, 'x'), "EOF"...)

func TestPatchesAreForGrownUpsAndTheirOwn(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	if w := as(t, h, "POST", "/games/7/patches?name=x", gus, anIPS); w.Code != 403 {
		t.Errorf("a guest: %d", w.Code)
	}
	if w := as(t, h, "POST", "/games/7/patches?name=x", kit, anIPS); w.Code != 403 {
		t.Errorf("a Kids profile: %d", w.Code)
	}
	if w := as(t, h, "POST", "/games/7/patches", alex, []byte("not a patch")); w.Code != 422 {
		t.Errorf("not a patch: %d", w.Code)
	}
	w := as(t, h, "POST", "/games/7/patches?name=../../evil.ips", alex, anIPS)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var list []GamePatch
	decode(t, w, &list)
	if len(list) != 1 || strings.ContainsAny(list[0].Name, `/\`) || !list[0].Enabled || list[0].Kind != "ips" {
		t.Fatalf("%+v", list)
	}
	pid := strconv.FormatInt(list[0].ID, 10)
	if _, err := os.Stat(patchFile(a.patchDir(alex.User, 7), list[0].ID)); err != nil {
		t.Fatalf("the file: %v", err)
	}
	if w := as(t, h, "GET", "/games/7/patches", sam, nil); strings.Contains(w.Body.String(), "evil") {
		t.Error("another person sees the patch")
	}
	if w := as(t, h, "PUT", "/games/7/patches/"+pid, sam, map[string]any{"enabled": false}); w.Code != 404 {
		t.Errorf("another person changed the patch: %d", w.Code)
	}
	if w := as(t, h, "DELETE", "/games/7/patches/"+pid, sam, nil); w.Code != 404 {
		t.Errorf("another person removed the patch: %d", w.Code)
	}

	// It applies to a copy of the game, never the game.
	rom := filepath.Join(t.TempDir(), "game.sfc")
	if err := os.WriteFile(rom, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, cleanup, notices := a.patchROM(t.Context(), alex.User, 7, rom, nil)
	if b, _ := os.ReadFile(out); out == rom || string(b) != "axcd" || len(notices) != 0 {
		t.Errorf("patched: %q %q %v", out, b, notices)
	}
	cleanup()
	if b, _ := os.ReadFile(rom); string(b) != "abcd" {
		t.Error("the game itself changed")
	}
	if w := as(t, h, "PUT", "/games/7/patches/"+pid, alex, map[string]any{"enabled": false}); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if out, _, _ := a.patchROM(t.Context(), alex.User, 7, rom, nil); out != rom {
		t.Error("a patch that's off was applied")
	}
	if w := as(t, h, "DELETE", "/games/7/patches/"+pid, alex, nil); w.Code != 200 {
		t.Fatalf("%d", w.Code)
	}
	if _, err := os.Stat(patchFile(a.patchDir(alex.User, 7), list[0].ID)); err == nil {
		t.Error("the file stayed")
	}
}

func TestPatchesHaveLimits(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	for i := range maxGamePatches {
		if w := as(t, h, "POST", "/games/7/patches", alex, anIPS); w.Code != 201 {
			t.Fatalf("%d: %d", i, w.Code)
		}
	}
	if w := as(t, h, "POST", "/games/7/patches", alex, anIPS); w.Code != 409 {
		t.Errorf("one too many: %d", w.Code)
	}
	old := maxAccountGameBytes
	maxAccountGameBytes = 10
	t.Cleanup(func() { maxAccountGameBytes = old })
	if w := as(t, h, "POST", "/games/12/patches", alex, anIPS); w.Code != 409 || !strings.Contains(w.Body.String(), "GB") {
		t.Errorf("past what an account may keep: %d %s", w.Code, w.Body)
	}
	a.forget(alex.User)
	if dirs, _ := filepath.Glob(filepath.Join(a.Dir, "saves", "u2", "i*", "patches")); len(dirs) != 0 {
		t.Errorf("an account's patches stayed: %v", dirs)
	}
}
