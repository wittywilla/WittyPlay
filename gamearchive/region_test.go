// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamearchive

import (
	"slices"
	"testing"
)

func TestRegionRankFollowsTheOrder(t *testing.T) {
	t.Cleanup(func() { SetRegionOrder("") })
	SetRegionOrder("")
	if rankRegion("USA") >= rankRegion("Europe") || rankRegion("Europe") >= rankRegion("Japan") {
		t.Fatalf("usual order: usa %d, europe %d, japan %d", rankRegion("USA"), rankRegion("Europe"), rankRegion("Japan"))
	}
	SetRegionOrder("japan,europe")
	if got := regionOrder(); !slices.Equal(got, []string{Japan, Europe, USA, Australia, Asia, Brazil}) {
		t.Fatalf("order %v", got)
	}
	if rankRegion("Japan") != 0 || rankRegion("Europe") != 1 || rankRegion("USA") != 2 {
		t.Fatalf("chosen order: japan %d, europe %d, usa %d", rankRegion("Japan"), rankRegion("Europe"), rankRegion("USA"))
	}
	if rankRegion("USA, Japan") != 0 {
		t.Fatal("a version for two regions counts for the better one")
	}
	if rankRegion("World") != 0 {
		t.Fatal("a version for everywhere is as good as the first choice")
	}
}

func TestRegionRankOnlyReadsWholeWords(t *testing.T) {
	t.Cleanup(func() { SetRegionOrder("") })
	SetRegionOrder("")
	for _, s := range []string{"", "Thousand", "Rev 1", "Busan", "Japanese"} {
		if got := rankRegion(s); got != unknownRegion() {
			t.Errorf("rankRegion(%q) = %d", s, got)
		}
	}
	if rankRegion("Germany") != rankRegion("Europe") || rankRegion("Canada") != rankRegion("USA") || rankRegion("Korea") != rankRegion("Asia") {
		t.Error("a country counts as its region")
	}
}

func TestRegionRankFile(t *testing.T) {
	t.Cleanup(func() { SetRegionOrder("") })
	SetRegionOrder("japan,usa")
	for name, want := range map[string]int{
		"Game (Japan).sfc":             0,
		"Game (J).sfc":                 0,
		"Game (JU) [!].sfc":            0,
		"Game (USA).sfc":               1,
		"Game (U) [!].sfc":             1,
		"Game (USA, Europe) (Rev 1).z": 1,
		"Game (W).sfc":                 0,
		"Game (World).sfc":             0,
		"America (Beta).sfc":           unknownRegion(),
		"Japan.sfc":                    unknownRegion(),
		"Game (En,Ja).sfc":             unknownRegion(),
		"Game (unfinished":             unknownRegion(),
	} {
		if got := rankRegionFile(name); got != want {
			t.Errorf("rankRegionFile(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestRegionSettingIsChecked(t *testing.T) {
	for v, want := range map[string]bool{
		"japan,usa":                 true,
		"usa":                       true,
		"japan, usa":                true,
		"japan,japan":               false,
		"mars":                      false,
		"usa,../../etc":             false,
		"usa,europe,australia,asia": true,
	} {
		if got := ValidRegionOrder(v); got != want {
			t.Errorf("ValidRegionOrder(%q) = %v", v, got)
		}
	}
	if got := parseRegions("mars,japan,japan"); !slices.Equal(got, []string{Japan, USA, Europe, Australia, Asia, Brazil}) {
		t.Errorf("Parse: %v", got)
	}
}
