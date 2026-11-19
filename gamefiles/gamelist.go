// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// gamelist.xml: one file per folder of games, with a <game> entry per game keyed by its <path>
// (relative to the folder, usually "./Game.sfc"). Pictures are paths relative to the folder too.

// glGame is one <game> entry. Elements this package doesn't know are kept as they are.
type glGame struct {
	XMLName     xml.Name   `xml:"game"`
	Attrs       []xml.Attr `xml:",any,attr"`
	Path        string     `xml:"path"`
	Name        string     `xml:"name,omitempty"`
	SortName    string     `xml:"sortname,omitempty"`
	Desc        string     `xml:"desc,omitempty"`
	Image       string     `xml:"image,omitempty"`
	Thumbnail   string     `xml:"thumbnail,omitempty"`
	Marquee     string     `xml:"marquee,omitempty"`
	Fanart      string     `xml:"fanart,omitempty"`
	Rating      string     `xml:"rating,omitempty"`
	ReleaseDate string     `xml:"releasedate,omitempty"`
	Developer   string     `xml:"developer,omitempty"`
	Publisher   string     `xml:"publisher,omitempty"`
	Genre       string     `xml:"genre,omitempty"`
	Players     string     `xml:"players,omitempty"`
	Region      string     `xml:"region,omitempty"`
	Lang        string     `xml:"lang,omitempty"`
	MD5         string     `xml:"md5,omitempty"`
	CRC32       string     `xml:"crc32,omitempty"`
	Other       []rawNode  `xml:",any"`
}

// rawNode is an element kept as it was.
type rawNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Inner   []byte     `xml:",innerxml"`
}

// readGamelist reads a folder's gamelist.xml: its entries, and its other top-level elements.
func readGamelist(dir string) ([]glGame, []rawNode, bool) {
	b, ok := readSmall(filepath.Join(dir, "gamelist.xml"), MaxGamelist)
	if !ok {
		return nil, nil, false
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
			return r, nil
		}
		return nil, fmt.Errorf("gamelist.xml in %s is not UTF-8", label)
	}
	var games []glGame
	var other []rawNode
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if t.Name.Local != "gameList" {
					return nil, nil, false
				}
				depth++
				continue
			}
			if t.Name.Local == "game" {
				var g glGame
				if err := dec.DecodeElement(&g, &t); err != nil {
					return nil, nil, false
				}
				if len(games) < 100000 {
					games = append(games, g)
				}
			} else {
				var n rawNode
				if err := dec.DecodeElement(&n, &t); err != nil {
					return nil, nil, false
				}
				other = append(other, n)
			}
		case xml.EndElement:
			depth--
		}
	}
	return games, other, true
}

func samePath(entry, file string) bool {
	p := strings.TrimPrefix(strings.ReplaceAll(strings.TrimSpace(entry), "\\", "/"), "./")
	return strings.EqualFold(p, file)
}

func readGamelistEntry(dir, file string) (glGame, bool) {
	games, _, ok := readGamelist(dir)
	if !ok {
		return glGame{}, false
	}
	for _, g := range games {
		if samePath(g.Path, file) {
			return g, true
		}
	}
	return glGame{}, false
}

func (g glGame) into(d *wittyplayfile.Doc) {
	m := details(d)
	setIf(&m.Title, g.Name)
	setIf(&m.SortTitle, g.SortName)
	setIf(&m.Overview, clean(g.Desc))
	setIf(&m.Developer, g.Developer)
	setIf(&m.Publisher, g.Publisher)
	if gs := splitList(g.Genre); len(gs) > 0 && len(m.Genres) == 0 {
		m.Genres = gs
	}
	if r, err := strconv.ParseFloat(strings.TrimSpace(g.Rating), 64); err == nil && r >= 0 && r <= 1 && m.Rating == nil {
		v := float64(int(r*100+0.5)) / 10
		m.Rating = &v
	}
	if y, date := releaseDate(g.ReleaseDate); y != 0 && m.Year == nil {
		m.Year, m.ReleaseDate = &y, date
	}
	if p := players(g.Players); p != nil && m.Players == nil {
		m.Players = p
	}
	if r := splitList(g.Region); len(r) > 0 && len(m.Regions) == 0 {
		m.Regions = r
	}
	if l := splitList(g.Lang); len(l) > 0 && len(m.Languages) == 0 {
		m.Languages = l
	}
	if g.MD5 != "" || g.CRC32 != "" {
		if d.Game.Identity == nil {
			d.Game.Identity = &wittyplayfile.Identity{}
		}
		setIf(&d.Game.Identity.MD5, strings.ToLower(strings.TrimSpace(g.MD5)))
		setIf(&d.Game.Identity.CRC32, strings.ToLower(strings.TrimSpace(g.CRC32)))
	}
}

// pictures are the entry's pictures by role, inside the folder only.
func (g glGame) pictures(dir string) map[string]string {
	out := map[string]string{}
	for role, rel := range map[string]string{"boxfront": g.Thumbnail, "screenshot": g.Image, "logo": g.Marquee, "fanart": g.Fanart} {
		if p, ok := inside(dir, rel); ok {
			out[role] = p
		}
	}
	if out["screenshot"] != "" && out["screenshot"] == out["boxfront"] {
		delete(out, "screenshot")
	}
	return out
}

// writeGamelistEntry adds or updates the game's entry in its folder's gamelist.xml: fields the
// document gives replace the entry's, the rest of the entry and the file are kept.
func writeGamelistEntry(dir, file string, d *wittyplayfile.Doc, pics map[string]string) error {
	games, other, ok := readGamelist(dir)
	if !ok && isFile(filepath.Join(dir, "gamelist.xml"), 0) {
		return errors.New("gamelist.xml is there but can't be read: left alone")
	}
	idx := -1
	for i, g := range games {
		if samePath(g.Path, file) {
			idx = i
			break
		}
	}
	if idx < 0 {
		games = append(games, glGame{Path: "./" + file})
		idx = len(games) - 1
	}
	g := &games[idx]
	if m := d.Game.Details; m != nil {
		replaceIf(&g.Name, m.Title)
		replaceIf(&g.SortName, m.SortTitle)
		replaceIf(&g.Desc, m.Overview)
		replaceIf(&g.Developer, m.Developer)
		replaceIf(&g.Publisher, m.Publisher)
		replaceIf(&g.Genre, strings.Join(m.Genres, ", "))
		replaceIf(&g.Region, strings.ToLower(strings.Join(m.Regions, ",")))
		replaceIf(&g.Lang, strings.Join(m.Languages, ","))
		if m.Rating != nil {
			g.Rating = strconv.FormatFloat(*m.Rating/10, 'f', -1, 64)
		}
		if m.ReleaseDate != "" || m.Year != nil {
			g.ReleaseDate = toGamelistDate(m)
		}
		if p := m.Players; p != nil && p.Max > 0 {
			g.Players = strconv.Itoa(p.Max)
			if p.Min > 0 && p.Min < p.Max {
				g.Players = fmt.Sprintf("%d-%d", p.Min, p.Max)
			}
		}
	}
	if id := d.Game.Identity; id != nil {
		replaceIf(&g.MD5, id.MD5)
		replaceIf(&g.CRC32, id.CRC32)
	}
	for role, field := range map[string]*string{"boxfront": &g.Thumbnail, "screenshot": &g.Image, "logo": &g.Marquee, "fanart": &g.Fanart} {
		if p := pics[role]; p != "" {
			if rel, err := filepath.Rel(dir, p); err == nil {
				*field = "./" + filepath.ToSlash(rel)
			}
		}
	}
	if g.Name == "" {
		g.Name = gameTitle(BaseOf(file))
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString("<gameList>\n")
	enc := xml.NewEncoder(&buf)
	enc.Indent("\t", "\t")
	for _, n := range other {
		if err := enc.Encode(n); err != nil {
			return err
		}
	}
	for _, g := range games {
		if err := enc.Encode(g); err != nil {
			return err
		}
	}
	if err := enc.Flush(); err != nil {
		return err
	}
	buf.WriteString("\n</gameList>\n")
	return writeAtomic(filepath.Join(dir, "gamelist.xml"), buf.Bytes())
}

func details(d *wittyplayfile.Doc) *wittyplayfile.Details {
	if d.Game == nil {
		d.Game = &wittyplayfile.About{}
	}
	if d.Game.Details == nil {
		d.Game.Details = &wittyplayfile.Details{}
	}
	return d.Game.Details
}

func setIf(dst *string, v string) {
	if v = strings.TrimSpace(v); v != "" && *dst == "" {
		*dst = v
	}
}

func replaceIf(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

// clean drops control characters other than new lines and tabs.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '/' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" && len(out) < wittyplayfile.MaxListItems {
			out = append(out, p)
		}
	}
	return out
}

// releaseDate reads 19940815T000000, 1994-08-15 or 1994.
func releaseDate(s string) (int, string) {
	s = strings.TrimSpace(s)
	if len(s) >= 8 && isDigits(s[:8]) {
		y, _ := strconv.Atoi(s[:4])
		if y >= 1950 && y <= 3000 {
			return y, s[:4] + "-" + s[4:6] + "-" + s[6:8]
		}
	}
	if len(s) >= 4 && isDigits(s[:4]) {
		y, _ := strconv.Atoi(s[:4])
		if y >= 1950 && y <= 3000 {
			if len(s) >= 10 && s[4] == '-' && isDigits(s[5:7]) && s[7] == '-' && isDigits(s[8:10]) {
				return y, s[:10]
			}
			return y, ""
		}
	}
	return 0, ""
}

func toGamelistDate(m *wittyplayfile.Details) string {
	if d := strings.ReplaceAll(m.ReleaseDate, "-", ""); len(d) == 8 {
		return d + "T000000"
	} else if len(d) == 6 {
		return d + "01T000000"
	} else if len(d) == 4 {
		return d + "0101T000000"
	}
	if m.Year != nil {
		return fmt.Sprintf("%04d0101T000000", *m.Year)
	}
	return ""
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// players reads "2", "1-4" or "1+".
func players(s string) *wittyplayfile.Players {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "+"))
	lo, hi, found := strings.Cut(s, "-")
	a, err1 := strconv.Atoi(strings.TrimSpace(lo))
	if err1 != nil || a < 0 || a > 64 {
		return nil
	}
	p := &wittyplayfile.Players{Min: 1, Max: a}
	if found {
		b, err := strconv.Atoi(strings.TrimSpace(hi))
		if err != nil || b < a || b > 64 {
			return nil
		}
		p.Min, p.Max = a, b
	}
	if p.Max == 0 {
		return nil
	}
	if p.Min > p.Max {
		p.Min = p.Max
	}
	return p
}

// writeAtomic writes a file through a temporary file beside it, so a reader never sees half of it.
func writeAtomic(p string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
