// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
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
}

var pictureExts = []string{".png", ".jpg", ".jpeg", ".webp"}

// picturesBeside finds a game's pictures by role. Screenshots may be several: Game.png, Game-01.png...
func picturesBeside(dir, base string) map[string][]string {
	out := map[string][]string{}
	add := func(role, p string) {
		for _, have := range out[role] {
			if have == p {
				return
			}
		}
		if role != "screenshot" && len(out[role]) > 0 {
			return
		}
		out[role] = append(out[role], p)
	}
	for _, k := range pictureKinds {
		for _, folder := range k.read {
			for _, root := range []string{filepath.Join(dir, "media", folder), filepath.Join(dir, folder)} {
				for _, ext := range pictureExts {
					if p := filepath.Join(root, base+ext); isFile(p, 0) {
						add(k.role, p)
					}
				}
				if k.role == "screenshot" {
					for _, p := range numbered(root, base) {
						add(k.role, p)
					}
				}
			}
		}
	}
	for suffix, role := range suffixes {
		for _, root := range []string{dir, filepath.Join(dir, "images")} {
			for _, ext := range pictureExts {
				if p := filepath.Join(root, base+suffix+ext); isFile(p, 0) {
					add(role, p)
				}
			}
		}
	}
	return out
}

// numbered finds Game-01.png, Game-02.png... in a folder, at most 20.
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
		if len(out) == 20 {
			break
		}
	}
	sort.Strings(out)
	return out
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
