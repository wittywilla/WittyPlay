// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// A game's .nfo: an XML document named after the game, with a <game> root (a <movie> root, as
// some programs write for anything, is read too).

type nfoGame struct {
	XMLName       xml.Name
	Title         string   `xml:"title,omitempty"`
	OriginalTitle string   `xml:"originaltitle,omitempty"`
	SortTitle     string   `xml:"sorttitle,omitempty"`
	Plot          string   `xml:"plot,omitempty"`
	Outline       string   `xml:"outline,omitempty"`
	Year          string   `xml:"year,omitempty"`
	ReleaseDate   string   `xml:"releasedate,omitempty"`
	Premiered     string   `xml:"premiered,omitempty"`
	Genres        []string `xml:"genre,omitempty"`
	Developer     string   `xml:"developer,omitempty"`
	Publisher     string   `xml:"publisher,omitempty"`
	Studio        string   `xml:"studio,omitempty"`
	Players       string   `xml:"players,omitempty"`
	Rating        string   `xml:"rating,omitempty"`
	Region        string   `xml:"region,omitempty"`
	Platform      string   `xml:"platform,omitempty"`
	Series        string   `xml:"series,omitempty"`
}

func readNFO(p string) (nfoGame, bool) {
	b, ok := readSmall(p, MaxNFO)
	if !ok {
		return nfoGame{}, false
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.CharsetReader = func(label string, r io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
			return r, nil
		}
		return nil, fmt.Errorf("not UTF-8: %s", label)
	}
	var n nfoGame
	if err := dec.Decode(&n); err != nil {
		return nfoGame{}, false
	}
	if root := n.XMLName.Local; root != "game" && root != "movie" {
		return nfoGame{}, false
	}
	return n, true
}

func (n nfoGame) into(d *wittyplayfile.Doc) {
	m := details(d)
	setIf(&m.Title, n.Title)
	setIf(&m.OriginalTitle, n.OriginalTitle)
	setIf(&m.SortTitle, n.SortTitle)
	setIf(&m.Overview, clean(n.Plot))
	setIf(&m.Overview, clean(n.Outline))
	setIf(&m.Developer, n.Developer)
	setIf(&m.Publisher, n.Publisher)
	setIf(&m.Publisher, n.Studio)
	setIf(&m.Series, n.Series)
	if len(m.Genres) == 0 {
		for _, g := range n.Genres {
			m.Genres = append(m.Genres, splitList(g)...)
		}
		if len(m.Genres) > wittyplayfile.MaxListItems {
			m.Genres = m.Genres[:wittyplayfile.MaxListItems]
		}
	}
	if m.Year == nil {
		for _, s := range []string{n.ReleaseDate, n.Premiered, n.Year} {
			if y, date := releaseDate(s); y != 0 {
				m.Year, m.ReleaseDate = &y, date
				break
			}
		}
	}
	if r, err := strconv.ParseFloat(strings.TrimSpace(n.Rating), 64); err == nil && r >= 0 && r <= 10 && m.Rating == nil {
		m.Rating = &r
	}
	if p := players(n.Players); p != nil && m.Players == nil {
		m.Players = p
	}
	if r := splitList(n.Region); len(r) > 0 && len(m.Regions) == 0 {
		m.Regions = r
	}
}

// nfoOf is the .nfo for a document's details.
func nfoOf(d *wittyplayfile.Doc) ([]byte, bool) {
	if d.Game == nil || d.Game.Details == nil {
		return nil, false
	}
	m := d.Game.Details
	n := nfoGame{XMLName: xml.Name{Local: "game"}, Title: m.Title, OriginalTitle: m.OriginalTitle, SortTitle: m.SortTitle, Plot: m.Overview,
		ReleaseDate: m.ReleaseDate, Genres: m.Genres, Developer: m.Developer, Publisher: m.Publisher, Region: strings.Join(m.Regions, ", "),
		Platform: m.Console, Series: m.Series}
	if m.Year != nil {
		n.Year = strconv.Itoa(*m.Year)
	}
	if m.Rating != nil {
		n.Rating = strconv.FormatFloat(*m.Rating, 'f', -1, 64)
	}
	if p := m.Players; p != nil && p.Max > 0 {
		n.Players = strconv.Itoa(p.Max)
		if p.Min > 0 && p.Min < p.Max {
			n.Players = fmt.Sprintf("%d-%d", p.Min, p.Max)
		}
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(n); err != nil {
		return nil, false
	}
	buf.WriteByte('\n')
	return buf.Bytes(), true
}
