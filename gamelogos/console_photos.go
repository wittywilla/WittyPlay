// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamelogos

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// Console photos: a picture of each console's hardware for WittyPlay's Consoles tab, from Wikimedia Commons by way of the
// console's Wikidata entity (its "image", P18). Wikidata is CC0; Commons states each file's licence and author, and only files
// whose licence allows this use are taken (FreeLogoLicense), with the author and licence kept to be shown beside the picture.

// consoleWikidata is each console's Wikidata entity, by the console ids of WittyPlay's tables (consoleLogoNames has the
// same ids). A console missing here is looked for by its name; an entity whose description isn't a console's is never used.
var consoleWikidata = map[string]string{
	"nes": "Q172742", "snes": "Q183259", "n64": "Q184839", "gc": "Q182172", "wii": "Q8079",
	"gb": "Q186437", "gbc": "Q203992", "gba": "Q188642", "nds": "Q170323", "3ds": "Q203597", "vb": "Q164651",
	"ps1": "Q10677", "ps2": "Q10680", "psp": "Q170325",
	"md": "Q10676", "sms": "Q209868", "gg": "Q751719", "saturn": "Q200912", "dc": "Q184198",
	"a2600": "Q206261", "lynx": "Q753600", "pce": "Q1057377",
}

// PhotoVariant is one of the versions of a console sold under another name or shape in another
// part of the world (an NES and a Famicom), with the Commons photo of that one. A console with
// versions shows the one an administrator chose for its library, the first to begin with.
//
// Name and Short are what the console is called when that version is chosen (and the console's
// names follow it, which they do unless an administrator keeps its usual names).
type PhotoVariant struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	File  string `json:"-"`
	Name  string `json:"name"`
	Short string `json:"short"`
}

// consolePhotoVariants are the consoles whose versions look different enough to choose between.
// Each file is a free photo on Commons (checked 2026-11-16: all public domain, by Evan-Amos).
var consolePhotoVariants = map[string][]PhotoVariant{
	"nes": {{"nes", "NES", "NES-Console-Set.jpg", "Nintendo Entertainment System", "NES"},
		{"famicom", "Famicom", "Nintendo-Famicom-Console-Set-FL.jpg", "Family Computer", "Famicom"}},
	"snes": {{"snes", "SNES", "SNES-Mod1-Console-Set.jpg", "Super Nintendo", "SNES"},
		{"superfamicom", "Super Famicom", "Nintendo-Super-Famicom-Set-FL.jpg", "Super Famicom", "Super Famicom"}},
	"md": {{"genesis", "Genesis", "Sega-Genesis-Mod1-Set.jpg", "Sega Genesis", "Genesis"},
		{"megadrive", "Mega Drive", "Sega-Mega-Drive-EU-Mk1-wController-FL.jpg", "Sega Mega Drive", "Mega Drive"},
		{"megadrivejp", "Mega Drive (Japan)", "Sega-Mega-Drive-JP-Mk1-Console-Set.jpg", "Sega Mega Drive", "Mega Drive"}},
	"pce": {{"pcengine", "PC Engine", "PC-Engine-Console-Set.png", "PC Engine", "PC Engine"},
		{"turbografx", "TurboGrafx-16", "TurboGrafx16-Console-Set.png", "TurboGrafx-16", "TurboGrafx-16"}},
}

// ConsolePhotoVariants are a console's versions to choose a photo from, or none.
func ConsolePhotoVariants(console string) []PhotoVariant { return consolePhotoVariants[console] }

// PhotoVariantOf is the version with that id, or the console's first; ok false for a console
// with no versions.
func PhotoVariantOf(console, id string) (PhotoVariant, bool) {
	vs := consolePhotoVariants[console]
	if len(vs) == 0 {
		return PhotoVariant{}, false
	}
	for _, v := range vs {
		if v.ID == id {
			return v, true
		}
	}
	return vs[0], true
}

// ConsoleWikidataID is a console's Wikidata entity, or "".
func ConsoleWikidataID(console string) string { return consoleWikidata[console] }

// ErrNoPhoto: Commons has no picture of the console that may be used.
var ErrNoPhoto = errors.New("no free picture of this console")

// ConsolePhoto finds the picture of a console: for a console with versions, the chosen version's
// own photo (variant, or its first); otherwise its Wikidata entity's image. Either way with
// Commons' licence and author. name is used to search when the console has no entity in the table.
func (w *WikimediaLogos) ConsolePhoto(ctx context.Context, console, name, variant string) (LogoCandidate, error) {
	if v, ok := PhotoVariantOf(console, variant); ok {
		info, err := w.fileInfo(ctx, []string{"File:" + v.File})
		if err != nil {
			return LogoCandidate{}, err
		}
		if c, ok := info[normalFileTitle("File:"+v.File)]; ok {
			c.Name, c.Exact = v.Label, true
			return c, nil
		}
		return LogoCandidate{}, ErrNoPhoto
	}
	apiBase := w.WikidataAPI
	if apiBase == "" {
		apiBase = wikidataAPI
	}
	ids := []string{}
	if q := consoleWikidata[console]; q != "" {
		ids = append(ids, q)
	} else if name != "" {
		var search struct {
			Search []struct {
				ID string `json:"id"`
			} `json:"search"`
		}
		if err := w.get(ctx, apiBase, url.Values{"action": {"wbsearchentities"}, "search": {name}, "language": {"en"}, "type": {"item"}, "limit": {"6"}}, &search); err != nil {
			return LogoCandidate{}, err
		}
		for _, r := range search.Search {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return LogoCandidate{}, ErrNoPhoto
	}
	var ents struct {
		Entities map[string]struct {
			Labels       map[string]struct{ Value string }   `json:"labels"`
			Aliases      map[string][]struct{ Value string } `json:"aliases"`
			Descriptions map[string]struct{ Value string }   `json:"descriptions"`
			Claims       map[string][]logoClaim              `json:"claims"`
		} `json:"entities"`
	}
	if err := w.get(ctx, apiBase, url.Values{"action": {"wbgetentities"}, "ids": {strings.Join(ids, "|")}, "props": {"labels|aliases|descriptions|claims"}, "languages": {"en"}}, &ents); err != nil {
		return LogoCandidate{}, err
	}
	key := BrandKey(name)
	for _, id := range ids {
		e, ok := ents.Entities[id]
		if !ok {
			continue
		}
		desc := strings.ToLower(e.Descriptions["en"].Value)
		if containsAny(desc, logoNotWords...) || !containsAny(desc, logoKindWords[LogoConsole]...) {
			continue
		}
		// Found by a search, the entity must be called what was asked.
		if consoleWikidata[console] == "" {
			exact := BrandKey(e.Labels["en"].Value) == key
			for _, a := range e.Aliases["en"] {
				exact = exact || BrandKey(a.Value) == key
			}
			if !exact {
				continue
			}
		}
		file := bestLogoFile(e.Claims[wdImage])
		if file == "" {
			continue
		}
		info, err := w.fileInfo(ctx, []string{"File:" + file})
		if err != nil {
			return LogoCandidate{}, err
		}
		if c, ok := info[normalFileTitle("File:"+file)]; ok {
			c.Name, c.Note, c.Exact = e.Labels["en"].Value, e.Descriptions["en"].Value, true
			return c, nil
		}
	}
	return LogoCandidate{}, ErrNoPhoto
}
