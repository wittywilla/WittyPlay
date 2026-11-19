// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

import (
	"slices"
	"testing"
)

// Every fix names cores the runner knows, says what it does, and turns on the runner's fixes for
// them; with fixes off, nothing that needs one is offered.
func TestCoreFixes(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range CoreFixes {
		if f.ID == "" || f.Title == "" || f.About == "" || len(f.Cores) == 0 || seen[f.ID] {
			t.Errorf("fix %+v", f)
		}
		seen[f.ID] = true
		for _, c := range f.Cores {
			if _, ok := traits[c]; !ok {
				t.Errorf("%s: no core %s", f.ID, c)
			}
			if !slices.Contains(RunnerArgsWith(c, true), "-core-fixes") || slices.Contains(RunnerArgsWith(c, false), "-core-fixes") {
				t.Errorf("%s: %v / %v", c, RunnerArgsWith(c, true), RunnerArgsWith(c, false))
			}
		}
	}
	if !slices.Contains(RunnerArgs("dolphin"), "-vulkan") || slices.Contains(RunnerArgsWith("dolphin", false), "-vulkan") {
		t.Errorf("Dolphin's Vulkan: %v / %v", RunnerArgs("dolphin"), RunnerArgsWith("dolphin", false))
	}
	if slices.Contains(RunnerArgs("snes9x"), "-core-fixes") || !slices.Contains(RunnerArgsWith("pcsx2", false), "-vulkan") {
		t.Errorf("cores without fixes: %v, %v", RunnerArgs("snes9x"), RunnerArgsWith("pcsx2", false))
	}
}
