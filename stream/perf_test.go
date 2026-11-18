// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import "testing"

func TestParseGPUProbe(t *testing.T) {
	p := parseGPUProbe("gpu: A card/PCIe\nvendor: A maker\nversion: 4.6.0 A driver 1.2\nvram-mb: 8192\n")
	if p.Name != "A card/PCIe" || p.Vendor != "A maker" || p.Version != "4.6.0 A driver 1.2" || p.VRAMMB != 8192 || p.Error != "" {
		t.Fatalf("got %+v", p)
	}
	// What an older runner says: the name alone.
	if p := parseGPUProbe("gpu: A card\n"); p.Name != "A card" || p.Vendor != "" {
		t.Fatalf("name alone: %+v", p)
	}
	if p := parseGPUProbe("none: OpenGL here is only llvmpipe\n"); p.Name != "" || p.Error != "OpenGL here is only llvmpipe" {
		t.Fatalf("none: %+v", p)
	}
	if p := parseGPUProbe("something else"); p.Name != "" || p.Error != "something else" {
		t.Fatalf("anything else is the reason: %+v", p)
	}
}

func TestFrameTimes(t *testing.T) {
	var f frameTimes
	if got := f.take(); got.Frames != 0 {
		t.Fatalf("empty: %+v", got)
	}
	for i := 1; i <= 100; i++ {
		f.add(float64(i), 1, int64(i)*16_000_000+int64(i/50)*84_000_000)
	}
	got := f.take()
	if got.Frames != 100 || got.CoreP50 != 51 || got.CoreP95 != 96 || got.TransitP50 != 1 || got.GapMax != 100 || got.GapP99 != 100 {
		t.Fatalf("got %+v", got)
	}
	if again := f.take(); again.Frames != 0 {
		t.Fatalf("taking starts again: %+v", again)
	}
}
