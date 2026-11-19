// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// Pictures beside a game: media/<kind>/<game>.<ext>, as scrapers lay them out, and
// <game>-<kind>.<ext> beside the game itself.

type pictureKind struct {
	role   string // a .wittyplay artwork role, or "screenshot"
	folder string // the media folder written to
	read   []string
}

var pictureKinds = []pictureKind{
	{"boxfront", "box2dfront", []string{"box2dfront", "boxfront", "covers", "boxart", "box2d"}},
	{"boxback", "box2dback", []string{"box2dback", "boxback"}},
	{"boxspine", "box2dside", []string{"box2dside", "boxside", "boxspine"}},
	{"media", "physicalmedia", []string{"physicalmedia", "support", "cartridge", "disc"}},
	{"logo", "wheel", []string{"wheel", "logo", "logos"}},
	{"marquee", "marquee", []string{"marquee", "marquees"}},
	{"banner", "banner", []string{"banner", "banners"}},
	{"titlescreen", "titlescreen", []string{"titlescreen", "titleshot", "titles"}},
	{"fanart", "fanart", []string{"fanart", "background", "backgrounds"}},
	{"snapshot", "videosnap", []string{"videosnap", "snapshot"}},
	{"icon", "icon", []string{"icon", "icons"}},
	{"screenshot", "screenshot", []string{"screenshot", "screenshots", "snap", "snaps"}},
}

// suffixes beside the game: Game-thumb.png and the like.
var suffixes = map[string]string{
	"-thumb": "boxfront", "-cover": "boxfront", "-boxfront": "boxfront", "-box": "boxfront", "-back": "boxback", "-boxback": "boxback",
	"-image": "screenshot", "-screenshot": "screenshot", "-marquee": "marquee", "-logo": "logo", "-wheel": "logo", "-fanart": "fanart",
	"-banner": "banner", "-titlescreen": "titlescreen", "-disc": "media", "-cart": "media", "-icon": "icon",
	"-boxart": "boxfront",
}

var pictureExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// picturesBeside finds a game's pictures by role, the one to show first first. A role may have
// several: Game.png, then Game-2.png or Game-02.png... in a media folder, and Game-boxart.png, then
// Game-boxart-2.png... beside the game. Numbered extras are looked for only once the first is found,
// so a folder of many games isn't listed for each of them.
func picturesBeside(dir, base string) map[string][]string {
	out := map[string][]string{}
	add := func(role, p string) {
		for _, have := range out[role] {
			if have == p {
				return
			}
		}
		if len(out[role]) < maxPerRole(role) {
			out[role] = append(out[role], p)
		}
	}
	for _, k := range pictureKinds {
		for _, folder := range k.read {
			for _, root := range []string{filepath.Join(dir, "media", folder), filepath.Join(dir, folder)} {
				found := false
				for _, ext := range pictureExts {
					if p := filepath.Join(root, base+ext); isFile(p, 0) {
						add(k.role, p)
						found = true
					}
				}
				if found || k.role == "screenshot" {
					for _, p := range numbered(root, base) {
						add(k.role, p)
					}
				}
			}
		}
	}
	for _, suffix := range sortedSuffixes() {
		role := suffixes[suffix]
		for _, root := range []string{dir, filepath.Join(dir, "images")} {
			for _, ext := range pictureExts {
				if p := filepath.Join(root, base+suffix+ext); isFile(p, 0) {
					add(role, p)
					for n := 2; n <= maxPerRole(role); n++ {
						q := filepath.Join(root, fmt.Sprintf("%s%s-%d%s", base, suffix, n, ext))
						if !isFile(q, 0) {
							break
						}
						add(role, q)
					}
				}
			}
		}
	}
	return out
}

func maxPerRole(role string) int {
	if role == "screenshot" {
		return wittyplayfile.MaxScreenshots
	}
	return wittyplayfile.MaxPicturesPerRole
}

// sortedSuffixes keeps the order pictures are found in the same from one run to the next.
func sortedSuffixes() []string {
	out := make([]string, 0, len(suffixes))
	for s := range suffixes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// numbered finds Game-2.png, Game-02.png... in a folder, at most 32.
func numbered(root, base string) []string {
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)+"-") {
			continue
		}
		rest := strings.TrimSuffix(name[len(base)+1:], filepath.Ext(name))
		if isDigits(rest) && len(rest) <= 3 && contains(pictureExts, strings.ToLower(filepath.Ext(name))) {
			out = append(out, filepath.Join(root, name))
		}
		if len(out) == wittyplayfile.MaxPicturesPerRole {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return numberOf(out[i]) < numberOf(out[j]) })
	return out
}

// numberOf is the number at the end of Game-12.png.
func numberOf(p string) int {
	name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	n := 0
	for i := strings.LastIndexByte(name, '-') + 1; i < len(name); i++ {
		n = n*10 + int(name[i]-'0')
	}
	return n
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// pictureFolder is the media folder a role is written to.
func pictureFolder(role string) string {
	for _, k := range pictureKinds {
		if k.role == role {
			return k.folder
		}
	}
	return ""
}
