// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gameid"
)

// Games made of several files, and games kept more than once. Three things happen here,
// after each file has been judged by itself:
//
//   - Copies of one file become one game: checksums settle it, however they're named.
//   - The discs of a game become one game with several discs.
//   - Versions of one game (regions, revisions) become one game with other versions
//     on its page. See linkGameVersions, which needs the games to have been listed first.

// gameFile is a game found by a scan.
type gameFile struct {
	cand    *candidate
	verdict gameVerdict
	key     string // what its name comes down to
	disc    int
	total   int
	dir     string
}

// fileBaseName is a file or archive's name for working out its title: what's inside
// when its own name is no use.
func fileBaseName(p string, v gameVerdict) string {
	if v.Name != "" {
		return v.Name
	}
	return gameBaseName(filepath.Base(p))
}

// gameKey is what's left of a name once everything that isn't its title has gone.
func gameKey(stem string) (key string, disc, total int) {
	if gameid.IsSlug(stem) {
		d, t, rest := gameid.DiscIn(stem)
		disc, total = d, t
		stem = gameid.Unslug(rest)
	} else {
		disc, total, stem = gameid.DiscIn(stem)
	}
	title, _ := CleanName(stem)
	return NameKey(SearchTitle(title)), disc, total
}

// mergeGames applies what a scan knows about several files at once. It returns the
// games to list, each knowing its discs.
// step, when set, is told about each file whose checksum is wanted (checksumsWanted of them).
func (ix *archiveIndex) mergeGames(found []*gameFile, step func()) []candidate {
	return ix.groupDiscs(ix.dropDuplicates(found, step))
}

// sameSize groups games by console and the size of their main file: only games the same size
// as another can be the same game.
func sameSize(found []*gameFile) map[string][]*gameFile {
	bySize := map[string][]*gameFile{}
	for _, g := range found {
		if g.verdict.Payload > 0 {
			k := g.cand.console + "|" + itoa(g.verdict.Payload)
			bySize[k] = append(bySize[k], g)
		}
	}
	return bySize
}

// checksumsWanted is how many of these games dropDuplicates looks at the checksum of.
func (ix *archiveIndex) checksumsWanted(found []*gameFile) int {
	n := 0
	for _, group := range sameSize(found) {
		if len(group) > 1 {
			n += len(group)
		}
	}
	return n
}

// dropDuplicates finds files with the same contents. Only files the same size as
// another are ever read for this, and a checksum an archive already holds costs nothing.
func (ix *archiveIndex) dropDuplicates(found []*gameFile, step func()) []*gameFile {
	bySize := sameSize(found)
	dup := map[*gameFile]*gameFile{}
	for _, group := range bySize {
		if len(group) < 2 {
			continue
		}
		byCRC := map[uint32][]*gameFile{}
		for _, g := range group {
			crc := ix.payloadCRC(g)
			if step != nil {
				step()
			}
			if crc != 0 {
				byCRC[crc] = append(byCRC[crc], g)
			}
		}
		for _, same := range byCRC {
			if len(same) < 2 {
				continue
			}
			// The one that needs no unpacking stays, then the one named more plainly.
			sort.SliceStable(same, func(a, b int) bool {
				aa, ab := gamearchive.IsArchive(same[a].cand.path), gamearchive.IsArchive(same[b].cand.path)
				if aa != ab {
					return ab
				}
				return len(filepath.Base(same[a].cand.path)) < len(filepath.Base(same[b].cand.path))
			})
			for _, g := range same[1:] {
				dup[g] = same[0]
			}
		}
	}
	if len(dup) == 0 {
		return found
	}
	out := found[:0]
	for _, g := range found {
		if keep := dup[g]; keep != nil {
			ix.noteFile(g.cand.path, g.cand.size, reviewDuplicate, "This is the same game as "+filepath.Base(keep.cand.path)+": their checksums match.", g.cand.console, nil)
			continue
		}
		out = append(out, g)
	}
	return out
}

// payloadCRC is the checksum of a game's main file. It's kept in its verdict, and a loose
// file is only read once.
func (ix *archiveIndex) payloadCRC(g *gameFile) uint32 {
	if g.verdict.CRC != 0 {
		return g.verdict.CRC
	}
	if gamearchive.IsArchive(g.cand.path) {
		return 0
	}
	p := mainFileOf(g.cand.path)
	crc := crcOfFile(p)
	if crc == 0 {
		return 0
	}
	g.verdict.CRC = crc
	key := ix.lastLibrary + "|" + g.cand.path
	if c, ok := ix.seenVerdicts[key]; ok {
		c.v.CRC = crc
		ix.seenVerdicts[key] = c
		ix.freshVerdicts[key] = true
	}
	return crc
}

// mainFileOf is the file holding a game: a sheet's first data track, or the file itself.
func mainFileOf(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cue", ".gdi", ".ccd", ".toc", ".m3u":
		if s, err := gameid.ReadSheet(p); err == nil && s.Data != "" {
			return s.Data
		}
	}
	return p
}

func sizeOf(p string) int64 {
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return st.Size()
	}
	return 0
}

// crcOfFile reads only an ordinary file: a pipe would hold a scan up for good, and a device has no end.
func crcOfFile(p string) uint32 {
	if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
		return 0
	}
	f, err := os.Open(p)
	if err != nil {
		return 0
	}
	defer f.Close()
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, f); err != nil {
		return 0
	}
	return h.Sum32()
}

// groupDiscs turns the discs of one game into one game. What it takes is a name that
// says which disc it is, in one folder, for one console. A disc that's missing is fine:
// its place stays empty, and the game still has the discs that are there.
func (ix *archiveIndex) groupDiscs(found []*gameFile) []candidate {
	type setKey struct{ console, dir, key string }
	sets := map[setKey][]*gameFile{}
	var out []candidate
	for _, g := range found {
		if g.disc == 0 {
			out = append(out, *g.cand)
			continue
		}
		k := setKey{g.cand.console, g.dir, g.key}
		sets[k] = append(sets[k], g)
	}
	for _, discs := range sets {
		sort.SliceStable(discs, func(a, b int) bool {
			if discs[a].disc != discs[b].disc {
				return discs[a].disc < discs[b].disc
			}
			// The same disc twice, and they aren't the same file: the unpacked one first.
			return !gamearchive.IsArchive(discs[a].cand.path) && gamearchive.IsArchive(discs[b].cand.path)
		})
		first := discs[0]
		c := *first.cand
		for i, d := range discs {
			if i > 0 && discs[i-1].disc == d.disc {
				ix.noteFile(d.cand.path, d.cand.size, reviewDuplicate, "This is another copy of disc "+itoa(int64(d.disc))+" of "+filepath.Base(first.cand.path)+".", d.cand.console, nil)
				continue
			}
			c.discs = append(c.discs, gameDisc{disc: d.disc, path: d.cand.path})
		}
		out = append(out, c)
	}
	return out
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// gameDisc is one disc of a game.
type gameDisc struct {
	disc int
	path string
}

// ---------------------------------------------------------------- versions of one game

// versionTagRe spots a pre-release, which is never what to show first.
var versionTagRe = regexp.MustCompile(`(?i)\b(?:beta|alpha|proto|prototype|demo|sample|pre-?release|preview|hack|pirate)\b`)

// versionRank orders versions of a game: a released version in the owner's preferred region
// first, then the newest revision of it. Lower is better.
func versionRank(tags string) int {
	rank := 100 * gamearchive.RankRegion(tags)
	if versionTagRe.MatchString(strings.ToLower(tags)) {
		rank += 1000
	}
	return rank
}

var revisionRe = regexp.MustCompile(`(?i)\b(?:rev|v|version)\s*\.?\s*([0-9a-z.]+)`)

func revisionOf(tags string) string {
	if m := revisionRe.FindStringSubmatch(tags); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// linkVersions puts versions of one game under one tile: games with the same name, on one
// console, in one library. Which goes first stays first once chosen (heads are the games
// other versions were under after the last scan), as a person's saves belong to it. Someone
// who has split a game off (own) has the last word. A game's own title is what its file calls
// it here, not what a source does, so nothing changes when a lookup comes back.
//
// It sets versionOf and versionLabel on the games that go under another.
func linkVersions(games []*listedGame, own, heads map[string]bool) {
	type row struct {
		g    *listedGame
		tags string
	}
	groups := map[string][]*row{}
	var order []string
	for _, g := range games {
		stem := gameBaseName(filepath.Base(g.c.path))
		key, _, _ := gameKey(stem)
		if key == "" || g.c.console == "" {
			continue
		}
		r := &row{g: g}
		_, r.tags = CleanName(stem)
		k := g.c.console + "|" + key
		if groups[k] == nil {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	for _, k := range order {
		var members []*row
		for _, r := range groups[k] {
			if !own[r.g.c.path] {
				members = append(members, r)
			}
		}
		if len(members) < 2 {
			continue
		}
		// Only versions that really differ belong together: two files with no region or
		// revision to tell them apart are different games that share a name.
		labels := map[string]bool{}
		for _, r := range members {
			labels[strings.ToLower(r.tags)] = true
		}
		if len(labels) < 2 {
			continue
		}
		sort.SliceStable(members, func(a, b int) bool {
			ra, rb := versionRank(members[a].tags), versionRank(members[b].tags)
			if ra != rb {
				return ra < rb
			}
			// The newest revision of the same region.
			return revisionOf(members[a].tags) > revisionOf(members[b].tags)
		})
		head := members[0]
		// One already at the head of these keeps its place.
		for _, r := range members {
			if heads[r.g.c.path] {
				head = r
				break
			}
		}
		for _, r := range members {
			if r == head {
				continue
			}
			label := r.tags
			if len(label) > 80 {
				label = label[:80]
			}
			r.g.versionOf, r.g.versionLabel = head.g.c.path, label
		}
	}
}
