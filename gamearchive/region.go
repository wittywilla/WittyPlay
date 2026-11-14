// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// The region order (moved with this package from the server's gameregion) holds the owner's order of regions: which version of a game to
// prefer when there's more than one. USA first isn't right for everyone.
package gamearchive

import (
	"slices"
	"strings"
	"sync/atomic"
)

// The regions someone can put in order, and what's the default order. Anything else
// comes after them all.
const (
	USA       = "usa"
	Europe    = "europe"
	Australia = "australia"
	Japan     = "japan"
	Asia      = "asia"
	Brazil    = "brazil"
)

// defaultRegions is the order with nothing chosen: what's most useful to an English speaker.
var defaultRegions = []string{USA, Europe, Australia, Japan, Asia, Brazil}

// regionWords says what a region is called in file names, and in the tags made from them.
var regionWords = map[string]string{
	"usa": USA, "us": USA, "america": USA, "canada": USA,
	"europe": Europe, "eur": Europe, "uk": Europe, "germany": Europe, "france": Europe, "spain": Europe, "italy": Europe, "netherlands": Europe,
	"holland": Europe, "sweden": Europe, "denmark": Europe, "norway": Europe, "finland": Europe, "portugal": Europe, "greece": Europe, "poland": Europe,
	"australia": Australia,
	"japan":     Japan, "jpn": Japan,
	"asia": Asia, "korea": Asia, "kor": Asia, "china": Asia, "taiwan": Asia, "hong": Asia,
	"brazil": Brazil,
}

// regionCodes are the short forms older sets use, in brackets: (U), (JU). Other short words in
// brackets are usually languages, so only these count, and only in a file's name.
var regionCodes = map[string][]string{
	"u": {USA}, "ue": {USA, Europe}, "ju": {Japan, USA}, "jue": {Japan, USA, Europe},
	"e": {Europe}, "g": {Europe}, "f": {Europe}, "s": {Europe}, "i": {Europe}, "h": {Europe},
	"a": {Australia}, "j": {Japan}, "k": {Asia}, "c": {Asia}, "b": {Brazil},
}

// worldRegion is for every region, so it's as good as the first of them.
const worldRegion = "world"

var currentRegions atomic.Pointer[[]string]

// parseRegions reads a setting: known regions, in order. Any left out go after those, in the
// usual order, so a saved order still works when a region is added.
func parseRegions(setting string) []string {
	out := make([]string, 0, len(defaultRegions))
	for _, p := range strings.Split(setting, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if slices.Contains(defaultRegions, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	for _, r := range defaultRegions {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// ValidRegionOrder says a setting names only known regions, each once.
func ValidRegionOrder(setting string) bool {
	seen := map[string]bool{}
	for _, p := range strings.Split(setting, ",") {
		p = strings.TrimSpace(p)
		if !slices.Contains(defaultRegions, p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// SetRegionOrder makes this order the one in use.
func SetRegionOrder(setting string) {
	o := parseRegions(setting)
	currentRegions.Store(&o)
}

// regionOrder is the order in use.
func regionOrder() []string {
	if o := currentRegions.Load(); o != nil {
		return *o
	}
	return defaultRegions
}

// unknownRegion is the rank of something from no region in the order.
func unknownRegion() int { return len(defaultRegions) }

// rankRegion says how wanted these tags are, lowest best: the best of the regions they name. A
// tag has to be a whole word: "Thousand" says nothing about the USA.
func rankRegion(tags string) int {
	best := unknownRegion()
	order := regionOrder()
	eachRegionWord(strings.ToLower(tags), func(w string) {
		if w == worldRegion {
			best = 0
			return
		}
		if r, ok := regionWords[w]; ok {
			best = min(best, slices.Index(order, r))
		}
	})
	return best
}

// rankRegionFile is rankRegion for a file's name, where only what's in brackets says anything:
// a game can be called America. It reads the short forms older sets use, too.
func rankRegionFile(name string) int {
	best := unknownRegion()
	order := regionOrder()
	n := strings.ToLower(name)
	for {
		start := strings.IndexByte(n, '(')
		if start < 0 {
			return best
		}
		n = n[start+1:]
		end := strings.IndexByte(n, ')')
		if end < 0 {
			return best
		}
		group := n[:end]
		n = n[end+1:]
		if rs, ok := regionCodes[group]; ok {
			for _, r := range rs {
				best = min(best, slices.Index(order, r))
			}
			continue
		}
		if group == "w" {
			best = 0
			continue
		}
		best = min(best, rankRegion(group))
	}
}

func eachRegionWord(s string, do func(string)) {
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] >= 'a' && s[i] <= 'z' {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			do(s[start:i])
			start = -1
		}
	}
}
