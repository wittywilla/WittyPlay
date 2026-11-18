// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// Options say how far WriteOut may go.
type Options struct {
	// Overwrite replaces an .nfo or a picture that is already there. Without it they are kept.
	Overwrite bool
	// Skip leaves these out: "gamelist", "nfo", "pictures", "remap", "config".
	Skip map[string]bool
}

// Written lists what WriteOut wrote, as paths relative to the game's folder.
type Written struct {
	Files   []string `json:"files,omitempty"`
	Kept    []string `json:"kept,omitempty"` // there already, and not to be replaced
	Problem []string `json:"problems,omitempty"`
}

// WriteOut writes a game's .wittyplay out to the standard files beside it. The game's console is
// what its own file says, else console.
func WriteOut(gamePath string, d *wittyplayfile.Doc, console string, opt Options) (Written, error) {
	var w Written
	if d == nil || (d.Game == nil && d.Controls == nil) {
		return w, errors.New("this .wittyplay says nothing to write out")
	}
	dir, base, file := filepath.Dir(gamePath), BaseOf(gamePath), filepath.Base(gamePath)
	rel := func(p string) string {
		if r, err := filepath.Rel(dir, p); err == nil {
			return filepath.ToSlash(r)
		}
		return p
	}
	write := func(p string, b []byte) {
		if !opt.Overwrite {
			if _, err := os.Lstat(p); err == nil {
				w.Kept = append(w.Kept, rel(p))
				return
			}
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.Problem = append(w.Problem, rel(p)+": "+err.Error())
			return
		}
		if err := writeAtomic(p, b); err != nil {
			w.Problem = append(w.Problem, rel(p)+": "+err.Error())
			return
		}
		w.Files = append(w.Files, rel(p))
	}
	pics := map[string]string{}
	if g := d.Game; g != nil {
		if !opt.Skip["pictures"] {
			for _, role := range sortedRoles(g.Artwork) {
				name := g.Artwork[role]
				b, ok := d.Files[name]
				folder := pictureFolder(role)
				if !ok || folder == "" {
					continue
				}
				p := filepath.Join(dir, "media", folder, base+strings.ToLower(filepath.Ext(name)))
				write(p, b)
				pics[role] = p
			}
			for i, name := range g.Screenshots {
				b, ok := d.Files[name]
				if !ok {
					continue
				}
				p := filepath.Join(dir, "media", "screenshot", base+strings.ToLower(filepath.Ext(name)))
				if i > 0 {
					p = filepath.Join(dir, "media", "screenshot", base+fmt.Sprintf("-%02d", i+1)+strings.ToLower(filepath.Ext(name)))
				} else {
					pics["screenshot"] = p
				}
				write(p, b)
			}
		}
		if !opt.Skip["nfo"] {
			nd := d
			if nd.Game.Details != nil && nd.Game.Details.Console == "" && console != "" {
				c := *nd.Game.Details
				c.Console = console
				nd = &wittyplayfile.Doc{Game: &wittyplayfile.About{Details: &c}}
			}
			if b, ok := nfoOf(nd); ok {
				write(filepath.Join(dir, base+".nfo"), b)
			}
		}
		if !opt.Skip["gamelist"] {
			if err := writeGamelistEntry(dir, file, d, pics); err != nil {
				w.Problem = append(w.Problem, "gamelist.xml: "+err.Error())
			} else {
				w.Files = append(w.Files, "gamelist.xml")
			}
		}
	}
	if d.Controls != nil {
		set := d.Controls.For(console)
		for _, f := range []struct {
			skip, ext string
			lines     map[string]string
		}{{"remap", ".rmp", remapLines(set)}, {"config", ".cfg", configLines(set)}} {
			if opt.Skip[f.skip] || len(f.lines) == 0 {
				continue
			}
			p := filepath.Join(dir, base+f.ext)
			changed, err := mergeConfigFile(p, f.lines)
			switch {
			case err != nil:
				w.Problem = append(w.Problem, rel(p)+": "+err.Error())
			case changed:
				w.Files = append(w.Files, rel(p))
			default:
				w.Kept = append(w.Kept, rel(p))
			}
		}
	}
	if len(w.Files) == 0 && len(w.Problem) > 0 {
		return w, errors.New(w.Problem[0])
	}
	return w, nil
}

func sortedRoles(a wittyplayfile.Artwork) []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
