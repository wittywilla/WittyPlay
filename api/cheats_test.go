// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"strings"
	"testing"
)

func TestCheatsAreEachPersonsOwn(t *testing.T) {
	_, h := testApp(t, personHost(t, nil))
	if w := as(t, h, "PUT", "/games/7/cheats", alex, map[string]any{"cheats": []map[string]any{{"label": "x", "code": "ABC", "enabled": true}}}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := as(t, h, "GET", "/games/7/cheats", sam, nil); w.Code != 200 || strings.Contains(w.Body.String(), "ABC") {
		t.Errorf("another person read the cheats: %d %s", w.Code, w.Body)
	}
	if w := as(t, h, "GET", "/games/7/cheats", alex, nil); !strings.Contains(w.Body.String(), "ABC") {
		t.Errorf("their own: %s", w.Body)
	}
	if w := as(t, h, "PUT", "/games/7/cheats", alex, map[string]any{"cheats": []map[string]any{{"code": "<script>"}}}); w.Code != 400 {
		t.Errorf("a code that isn't one: %d", w.Code)
	}
	if w := as(t, h, "PUT", "/games/12/cheats", kit, map[string]any{"cheats": []map[string]any{}}); w.Code != 404 {
		t.Errorf("a game the profile may not see: %d", w.Code)
	}
}

func TestCheatFilesAreReadNotKept(t *testing.T) {
	_, h := testApp(t, personHost(t, nil))
	file := []byte("cheats = 1\ncheat0_desc = \"Lives\"\ncheat0_code = \"7E0DBE:09\"\ncheat0_enable = true\n")
	w := as(t, h, "POST", "/games/7/cheats/import", alex, file)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Lives") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := as(t, h, "GET", "/games/7/cheats", alex, nil); strings.Contains(w.Body.String(), "Lives") {
		t.Error("an import was kept before it was saved")
	}
	if w := as(t, h, "POST", "/games/7/cheats/import", alex, make([]byte, 3<<20)); w.Code != 413 {
		t.Errorf("too large: %d", w.Code)
	}
}
