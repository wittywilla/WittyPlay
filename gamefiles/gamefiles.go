// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package gamefiles converts between a game's .wittyplay and the industry-standard files other
// game programs keep beside games: a folder's gamelist.xml, a game's .nfo, pictures in media/
// folders or named after the game, per-game remap (.rmp) and input override (.cfg) files.
//
// Gather reads what is beside a game into a .wittyplay document; WriteOut writes a document back
// out to those files. Both only read and write inside the game's own folder, check every file's
// size before reading it, and never follow a path a file names outside that folder. WriteOut never
// removes a file, keeps what it doesn't set, and replaces a file only when told to (a gamelist.xml
// entry for the same game, and the lines of a .cfg or .rmp it sets, are always updated).
package gamefiles

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// Limits on what is read.
const (
	MaxGamelist = 16 << 20
	MaxNFO      = 1 << 20
	MaxConfig   = 256 << 10
)

// Found says what Gather found beside a game.
type Found struct {
	Gamelist bool     `json:"gamelist,omitempty"`
	NFO      bool     `json:"nfo,omitempty"`
	Pictures []string `json:"pictures,omitempty"` // roles found, "screenshot" for each screenshot
	Remap    bool     `json:"remap,omitempty"`
	Config   bool     `json:"config,omitempty"`
	// Sources are the game's own files that were read (its .nfo, .rmp, .cfg and pictures), for a
	// caller that retires them once they're in a .wittyplay. A folder's gamelist.xml is shared by
	// its games, so it is never one of them.
	Sources []string `json:"-"`
}

// Any says something was found.
func (f Found) Any() bool { return f.Gamelist || f.NFO || len(f.Pictures) > 0 || f.Remap || f.Config }

// BaseOf is the name the files beside a game are named after. WittyPlay sets it to its own
// (which knows a game packed in an archive); Base is the default.
var BaseOf = Base

// Base is a game file's name without its extension: "Made Up Quest (USA)".
func Base(gamePath string) string {
	name := filepath.Base(gamePath)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// SidecarPath is where a game's own .wittyplay goes: beside it, named after it.
func SidecarPath(gamePath string) string {
	return filepath.Join(filepath.Dir(gamePath), BaseOf(gamePath)+wittyplayfile.Ext)
}

// Has says some standard file is beside the game, cheaply (names only, nothing read).
func Has(gamePath string) bool {
	dir, base := filepath.Dir(gamePath), BaseOf(gamePath)
	for _, p := range []string{filepath.Join(dir, base+".nfo"), filepath.Join(dir, base+".rmp"), filepath.Join(dir, base+".cfg")} {
		if isFile(p, 0) {
			return true
		}
	}
	if gamelistHas(dir, filepath.Base(gamePath)) {
		return true
	}
	for _, k := range pictureKinds {
		for _, ext := range pictureExts {
			if isFile(filepath.Join(dir, "media", k.folder, base+ext), 0) {
				return true
			}
		}
	}
	return false
}

// Gather reads what the standard files beside a game say into a .wittyplay document. A nil
// document and no error: nothing was found.
func Gather(gamePath string) (*wittyplayfile.Doc, Found, error) {
	var found Found
	dir, base := filepath.Dir(gamePath), BaseOf(gamePath)
	d := &wittyplayfile.Doc{Version: wittyplayfile.Version, Game: &wittyplayfile.About{}}
	seen := map[string]bool{} // a picture is added once, however many files name it
	addPicture := func(d *wittyplayfile.Doc, role, p string) bool {
		key := strings.ToLower(filepath.Clean(p))
		if seen[key] {
			return false
		}
		seen[key] = true
		if !addPicture(d, role, p) {
			return false
		}
		found.Sources = append(found.Sources, p)
		return true
	}
	if e, ok := readGamelistEntry(dir, filepath.Base(gamePath)); ok {
		found.Gamelist = true
		e.into(d)
		for role, p := range e.pictures(dir) {
			if addPicture(d, role, p) {
				found.Pictures = append(found.Pictures, role)
			}
		}
	}
	if n, ok := readNFO(filepath.Join(dir, base+".nfo")); ok {
		found.NFO = true
		found.Sources = append(found.Sources, filepath.Join(dir, base+".nfo"))
		n.into(d)
	}
	for role, paths := range picturesBeside(dir, base) {
		for _, p := range paths {
			if addPicture(d, role, p) {
				found.Pictures = append(found.Pictures, role)
			}
		}
	}
	if ps, dev, ok := readRemap(filepath.Join(dir, base+".rmp")); ok {
		found.Remap = true
		found.Sources = append(found.Sources, filepath.Join(dir, base+".rmp"))
		set := ensureControls(d)
		for _, p := range ps {
			mergePlayer(set, p)
		}
		for n, v := range dev {
			mergePlayer(set, wittyplayfile.PlayerControls{Player: n, Device: v})
		}
	}
	if cs, ok := readConfig(filepath.Join(dir, base+".cfg")); ok {
		found.Config = true
		found.Sources = append(found.Sources, filepath.Join(dir, base+".cfg"))
		set := ensureControls(d)
		*set = cs.Over(*set)
	}
	if !found.Any() {
		return nil, found, nil
	}
	if d.Game.Details != nil && d.Game.Details.Title == "" {
		d.Game.Details.Title = gameTitle(base)
	}
	return d, found, nil
}

func ensureControls(d *wittyplayfile.Doc) *wittyplayfile.ControlSet {
	if d.Controls == nil {
		d.Controls = &wittyplayfile.Controls{}
	}
	if d.Controls.Default == nil {
		d.Controls.Default = &wittyplayfile.ControlSet{}
	}
	return d.Controls.Default
}

func mergePlayer(s *wittyplayfile.ControlSet, p wittyplayfile.PlayerControls) {
	*s = wittyplayfile.ControlSet{Players: []wittyplayfile.PlayerControls{p}}.Over(*s)
}

// gameTitle is a title from a dump's file name: "Made Up Quest (USA) (Rev 1)" is "Made Up Quest".
func gameTitle(base string) string {
	if i := strings.IndexAny(base, "(["); i > 0 {
		base = base[:i]
	}
	return strings.TrimSpace(base)
}

func addPicture(d *wittyplayfile.Doc, role, path string) bool {
	if !isFile(path, wittyplayfile.MaxImage) {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil || wittyplayfile.ImageType(b) == "" {
		return false
	}
	switch {
	case role == "screenshot":
		_, err = d.AddScreenshot(b)
	case d.Game != nil && len(d.Game.Artwork[role]) > 0:
		_, err = d.AddImage(role, b) // another region's box, a second fan art...
	default:
		_, err = d.SetImage(role, b)
	}
	return err == nil
}

// isFile says p is an ordinary file of at most max bytes (any size when max is 0). Links are
// not followed out of the folder: a link is not an ordinary file to Lstat.
func isFile(p string, max int64) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular() && (max == 0 || st.Size() <= max)
}

func readSmall(p string, max int64) ([]byte, bool) {
	if !isFile(p, max) {
		return nil, false
	}
	b, err := os.ReadFile(p)
	return b, err == nil && int64(len(b)) <= max
}

// inside says rel, a path a file in dir names, stays inside dir, and gives it as a full path.
func inside(dir, rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.Contains(rel, "\x00") || strings.Contains(rel, "://") {
		return "", false
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(rel, "/") || len(rel) >= 2 && rel[1] == ':' { // rooted, or a drive on any system
		return "", false
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", false
	}
	p := filepath.Join(dir, rel)
	r, err := filepath.Rel(dir, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

// gamelistHas says the folder's gamelist.xml has an entry for the file. A folder's entries are
// read once and kept while the file stays the same, so counting a whole library reads each
// gamelist.xml once.
func gamelistHas(dir, file string) bool {
	st, err := os.Lstat(filepath.Join(dir, "gamelist.xml"))
	if err != nil || !st.Mode().IsRegular() || st.Size() > MaxGamelist {
		return false
	}
	glMu.Lock()
	c, ok := glCache[dir]
	glMu.Unlock()
	if !ok || !c.mod.Equal(st.ModTime()) || c.size != st.Size() {
		games, _, _ := readGamelist(dir)
		c = glCached{mod: st.ModTime(), size: st.Size(), paths: map[string]bool{}}
		for _, g := range games {
			c.paths[strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(g.Path), "\\", "/"), "./"))] = true
		}
		glMu.Lock()
		if len(glCache) >= 256 {
			glCache = map[string]glCached{}
		}
		glCache[dir] = c
		glMu.Unlock()
	}
	return c.paths[strings.ToLower(file)]
}

type glCached struct {
	mod   time.Time
	size  int64
	paths map[string]bool
}

var (
	glMu    sync.Mutex
	glCache = map[string]glCached{}
)
