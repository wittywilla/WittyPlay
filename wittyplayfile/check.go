// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Problem is one thing wrong with a file: where (a JSON path such as "game.details.year", or an
// archive member) and what.
type Problem struct {
	Where string `json:"where"`
	What  string `json:"what"`
}

func (p Problem) String() string {
	if p.Where == "" {
		return p.What
	}
	return p.Where + ": " + p.What
}

// Error lists everything wrong with a file that was refused.
type Error struct {
	Problems []Problem `json:"problems"`
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return "not a usable .wittyplay: " + e.Problems[0].String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "not a usable .wittyplay (%d problems): ", len(e.Problems))
	for i, p := range e.Problems {
		if i == 5 {
			fmt.Fprintf(&b, "; and %d more", len(e.Problems)-5)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(p.String())
	}
	return b.String()
}

func refuse(where, format string, args ...any) *Error {
	return &Error{Problems: []Problem{{Where: where, What: fmt.Sprintf(format, args...)}}}
}

// Limits on a game's own file.
const (
	MaxListItems   = 50
	MaxShortText   = 200
	MaxLongText    = 20000
	MaxScreenshots = 50
	MaxCheatCount  = 500
	MaxCheatCode   = 2048
	MaxCoreOptions = 300
)

type checker struct{ problems []Problem }

func (c *checker) bad(where, format string, args ...any) {
	if len(c.problems) < 100 {
		c.problems = append(c.problems, Problem{Where: where, What: fmt.Sprintf(format, args...)})
	}
}

func (c *checker) text(where, s string, max int) {
	if utf8.RuneCountInString(s) > max {
		c.bad(where, "longer than %d characters", max)
	}
	for _, r := range s {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			c.bad(where, "holds control characters")
			return
		}
	}
}

func (c *checker) list(where string, l []string, max int) {
	if len(l) > MaxListItems {
		c.bad(where, "more than %d entries", MaxListItems)
	}
	for i, s := range l {
		c.text(fmt.Sprintf("%s[%d]", where, i), s, max)
	}
}

func (c *checker) oneOf(where, v string, allowed []string) {
	if !oneOfList(v, allowed) {
		c.bad(where, "%q is not one of %s", v, strings.Join(allowed, ", "))
	}
}

// member checks a reference to another member of the archive: a plain name of a member that is
// there, in the images folder. A URL or a path never is one.
func (c *checker) member(where, name string, files map[string][]byte) {
	if name == "" {
		c.bad(where, "names no picture")
		return
	}
	if strings.Contains(name, ":") || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || !ValidMemberName(name) {
		c.bad(where, "has to name a picture inside the .wittyplay, such as images/boxfront.png")
		return
	}
	if !strings.HasPrefix(strings.ToLower(name), "images/") {
		c.bad(where, "has to be in the images folder of the .wittyplay")
		return
	}
	if _, ok := files[name]; !ok {
		c.bad(where, "names %s, which isn't in the .wittyplay", name)
	}
}

var (
	idRe       = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	hexRe      = map[int]*regexp.Regexp{8: regexp.MustCompile(`^[0-9a-fA-F]{8}$`), 32: regexp.MustCompile(`^[0-9a-fA-F]{32}$`), 40: regexp.MustCompile(`^[0-9a-fA-F]{40}$`)}
	dateRe     = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
	aspectRe   = regexp.MustCompile(`^(\d{1,2}(\.\d{1,3})?:\d{1,2}(\.\d{1,3})?|core|stretch|square)$`)
	optionKey  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,96}$`)
	simpleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _.+-]{0,99}$`)
)

func goodCheatCode(code string) bool {
	if code == "" || len(code) > MaxCheatCode {
		return false
	}
	for _, r := range code {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case strings.ContainsRune(" +-:;,.=?_", r):
		default:
			return false
		}
	}
	return true
}

// checkV2 checks the parts version 2 added: a game's own details, the controls, who made the
// file and its languages, and that each picture it names is in Files.
func (d *Doc) checkV2(c *checker) {
	if d.Controls != nil {
		c.controlSet("controls.default", d.Controls.Default)
		if len(d.Controls.Consoles) > MaxConsoles {
			c.bad("controls.consoles", "at most %d consoles", MaxConsoles)
		}
		for _, id := range sortedKeys(d.Controls.Consoles) {
			if !consoleID.MatchString(id) {
				c.bad("controls.consoles."+id, "%q is not a console id", id)
			}
			s := d.Controls.Consoles[id]
			c.controlSet("controls.consoles."+id, &s)
		}
	}
	if g := d.Game; g != nil {
		if m := g.Details; m != nil {
			c.details("game.details", m)
		}
		if id := g.Identity; id != nil {
			c.identity("game.identity", id.FileName, id.Size, id.CRC32, id.MD5, id.SHA1)
			c.text("game.identity.serial", id.Serial, 64)
			if len(id.Files) > 100 {
				c.bad("game.identity.files", "more than 100 files")
			}
			for i, f := range id.Files {
				w := fmt.Sprintf("game.identity.files[%d]", i)
				if f.Name == "" {
					c.bad(w+".name", "a file's name is needed")
				}
				c.identity(w, f.Name, f.Size, f.CRC32, f.MD5, f.SHA1)
			}
		}
		for _, role := range sortedKeys(g.Artwork) {
			c.oneOf("game.artwork."+role, role, ArtworkRoles)
			c.member("game.artwork."+role, g.Artwork[role], d.Files)
		}
		if len(g.Screenshots) > MaxScreenshots {
			c.bad("game.screenshots", "more than %d screenshots", MaxScreenshots)
		}
		for i, s := range g.Screenshots {
			c.member(fmt.Sprintf("game.screenshots[%d]", i), s, d.Files)
		}
		if p := g.Play; p != nil {
			if p.Emulator != "" && !simpleName.MatchString(p.Emulator) {
				c.bad("game.play.emulator", "an emulator's id: letters, digits, space and _ . + -")
			}
			if p.Core != "" && !simpleName.MatchString(p.Core) {
				c.bad("game.play.core", "a core's id: letters, digits, space and _ . + -")
			}
			c.text("game.play.shader", p.Shader, MaxShortText)
			if p.AspectRatio != "" && !aspectRe.MatchString(p.AspectRatio) {
				c.bad("game.play.aspectRatio", "written as 4:3, 8:7, core, stretch or square")
			}
			if p.Rotation%90 != 0 || p.Rotation < 0 || p.Rotation > 270 {
				c.bad("game.play.rotation", "0, 90, 180 or 270")
			}
			if p.VideoStandard != "" {
				c.oneOf("game.play.videoStandard", p.VideoStandard, []string{"ntsc", "pal"})
			}
			if p.Disc < 0 || p.Disc > 99 {
				c.bad("game.play.disc", "a disc's number, from 1")
			}
			if len(p.CoreOptions) > MaxCoreOptions {
				c.bad("game.play.coreOptions", "more than %d options", MaxCoreOptions)
			}
			for _, k := range sortedKeys(p.CoreOptions) {
				if !optionKey.MatchString(k) {
					c.bad("game.play.coreOptions", "%q isn't a core option's name", k)
				}
				c.text("game.play.coreOptions."+k, p.CoreOptions[k], 256)
			}
		}
		if len(g.Cheats) > MaxCheatCount {
			c.bad("game.cheats", "more than %d cheats", MaxCheatCount)
		}
		ids := map[string]bool{}
		for i, ch := range g.Cheats {
			w := fmt.Sprintf("game.cheats[%d]", i)
			if ch.ID != "" {
				if !idRe.MatchString(ch.ID) {
					c.bad(w+".id", "an id is up to 64 letters, digits and _ . : -")
				} else if ids[ch.ID] {
					c.bad(w+".id", "the id %q is used twice", ch.ID)
				}
				ids[ch.ID] = true
			}
			c.text(w+".label", ch.Label, 120)
			if !goodCheatCode(ch.Code) {
				c.bad(w+".code", "a cheat code is letters, digits and + - : ; , . = ? _ only, at most %d characters", MaxCheatCode)
			}
		}
		if s := g.Saves; s != nil {
			if s.Kind != "" {
				c.oneOf("game.saves.kind", s.Kind, []string{"battery", "memorycard", "password", "none"})
			}
			if s.MemoryCard != "" {
				c.oneOf("game.saves.memoryCard", s.MemoryCard, []string{"shared", "game"})
			}
			if s.Blocks < 0 || s.Blocks > 1000 {
				c.bad("game.saves.blocks", "between 0 and 1000")
			}
			c.text("game.saves.notes", s.Notes, MaxLongText)
		}
		c.text("game.notes", g.Notes, MaxLongText)
	}
	if a := d.Authorship; a != nil {
		c.text("authorship.author", a.Author, MaxShortText)
		c.text("authorship.license", a.License, 64)
		c.text("authorship.source", a.Source, 2000)
		for _, f := range []struct{ where, v string }{{"authorship.created", a.Created}, {"authorship.updated", a.Updated}} {
			if f.v != "" {
				if _, err := time.Parse(time.RFC3339, f.v); err != nil {
					c.bad(f.where, "has to be a date and time such as 2026-11-18T20:15:00Z")
				}
			}
		}
		if a.Tool != nil {
			c.text("authorship.tool.name", a.Tool.Name, MaxShortText)
			c.text("authorship.tool.version", a.Tool.Version, 64)
		}
	}
	d.checkLanguages(c)
	if len(d.Extensions) > 100 {
		c.bad("extensions", "more than 100 extensions")
	}
}

func (c *checker) details(w string, m *Details) {
	for _, f := range []struct{ name, v string }{{"title", m.Title}, {"sortTitle", m.SortTitle}, {"originalTitle", m.OriginalTitle},
		{"developer", m.Developer}, {"publisher", m.Publisher}, {"series", m.Series}} {
		c.text(w+"."+f.name, f.v, MaxShortText)
	}
	c.text(w+".overview", m.Overview, MaxLongText)
	if m.Console != "" && !consoleID.MatchString(m.Console) {
		c.bad(w+".console", "%q is not a console id", m.Console)
	}
	if m.Year != nil && (*m.Year < 1950 || *m.Year > 3000) {
		c.bad(w+".year", "has to be a year")
	}
	if m.ReleaseDate != "" {
		if !dateRe.MatchString(m.ReleaseDate) {
			c.bad(w+".releaseDate", "has to be a date such as 1994-11-21, 1994-11 or 1994")
		}
	}
	c.list(w+".genres", m.Genres, 64)
	c.list(w+".tags", m.Tags, 64)
	c.list(w+".regions", m.Regions, 64)
	if p := m.Players; p != nil {
		if p.Min < 0 || p.Max < 0 || p.Min > 64 || p.Max > 64 || (p.Max > 0 && p.Min > p.Max) {
			c.bad(w+".players", "min and max players are 0 to 64, min no more than max")
		}
	}
	if r := m.Rating; r != nil && (math.IsNaN(*r) || *r < 0 || *r > 10) {
		c.bad(w+".rating", "a score out of 10")
	}
	if len(m.AgeRatings) > 20 {
		c.bad(w+".ageRatings", "more than 20 boards")
	}
	for _, k := range sortedKeys(m.AgeRatings) {
		c.text(w+".ageRatings."+k, k, 32)
		c.text(w+".ageRatings."+k, m.AgeRatings[k], 32)
	}
	if len(m.ExternalIDs) > MaxListItems {
		c.bad(w+".externalIds", "more than %d ids", MaxListItems)
	}
	for _, k := range sortedKeys(m.ExternalIDs) {
		if !idRe.MatchString(k) {
			c.bad(w+".externalIds", "%q isn't a plain name", k)
		}
		c.text(w+".externalIds."+k, m.ExternalIDs[k], 128)
	}
}

func (c *checker) identity(w, name string, size int64, crc, md5, sha1 string) {
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		c.bad(w+".fileName", "a file name, without a folder")
	}
	c.text(w+".fileName", name, 255)
	if size < 0 {
		c.bad(w+".size", "can't be negative")
	}
	for _, h := range []struct {
		name, v string
		n       int
	}{{"crc32", crc, 8}, {"md5", md5, 32}, {"sha1", sha1, 40}} {
		if h.v != "" && !hexRe[h.n].MatchString(h.v) {
			c.bad(w+"."+h.name, "%d hexadecimal digits", h.n)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
