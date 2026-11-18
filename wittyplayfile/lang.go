// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"encoding/json"
	"fmt"

	"github.com/wittywilla/wittyplay/langtag"
)

// Languages, as in a .wittyinfo. A file says which language its own text is in ("language") and
// can carry the same text in others ("translations"), keyed by language tag: ISO 639-1, 639-2 or
// 639-3 codes and BCP 47 tags with a script or region are all read, and written in their usual
// form ("ger" becomes "de", "pt_br" becomes "pt-BR"). A player gets the translation closest to
// their language: the same tag, else the same language.

// MaxTranslations is the most languages one file carries.
const MaxTranslations = 200

func (d *Doc) checkLanguages(c *checker) {
	tag := func(where, v string) {
		if v != "" && !langtag.Valid(v) {
			c.bad(where, "%q is not a language code (use one such as en, deu, pt-BR or zh-Hant)", v)
		}
	}
	tag("language", d.Language)
	if g := d.Game; g != nil && g.Details != nil {
		for i, l := range g.Details.Languages {
			tag(fmt.Sprintf("game.details.languages[%d]", i), l)
		}
	}
	if len(d.Translations) > MaxTranslations {
		c.bad("translations", "more than %d languages", MaxTranslations)
	}
	cheats := map[string]bool{}
	if d.Game != nil {
		for _, ch := range d.Game.Cheats {
			if ch.ID != "" {
				cheats[ch.ID] = true
			}
		}
	}
	seen := map[string]string{}
	for _, k := range sortedKeys(d.Translations) {
		w := "translations." + k
		canon, ok := langtag.Canonical(k)
		if !ok {
			c.bad(w, "%q is not a language code", k)
			continue
		}
		if other, dup := seen[canon]; dup {
			c.bad(w, "the same language as %s", other)
		}
		seen[canon] = k
		tr := d.Translations[k]
		if tr == nil {
			continue
		}
		if tr.Details != nil {
			c.details(w+".details", tr.Details)
		}
		for _, id := range sortedKeys(tr.Cheats) {
			if !cheats[id] {
				c.bad(w+".cheats."+id, "names a cheat that isn't in the file")
			}
			c.text(w+".cheats."+id, tr.Cheats[id], 120)
		}
		c.text(w+".notes", tr.Notes, MaxLongText)
		c.text(w+".saves", tr.Saves, MaxLongText)
	}
}

// tidyLanguages writes every tag in its usual form, and merges translations that turn out to be
// the same language (the later one's text wins).
func (d *Doc) tidyLanguages() {
	canon := func(v string) string {
		if c, ok := langtag.Canonical(v); ok {
			return c
		}
		return v
	}
	d.Language = canon(d.Language)
	if g := d.Game; g != nil && g.Details != nil {
		for i := range g.Details.Languages {
			g.Details.Languages[i] = canon(g.Details.Languages[i])
		}
	}
	if len(d.Translations) == 0 {
		d.Translations = nil
		return
	}
	out := map[string]*Translation{}
	for _, k := range sortedKeys(d.Translations) {
		if tr := d.Translations[k]; tr != nil {
			if b, _ := json.Marshal(tr); string(b) != "{}" {
				out[canon(k)] = tr
			}
		}
	}
	if len(out) == 0 {
		out = nil
	}
	d.Translations = out
}

// Localize is the game's text in the language closest to want (a tag in any form). The file
// itself is not changed; the result shares nothing with it that Localize changes.
func (d *Doc) Localize(want string) *About {
	if d.Game == nil {
		return nil
	}
	b, _ := json.Marshal(d.Game)
	var g About
	_ = json.Unmarshal(b, &g)
	if want == "" || len(d.Translations) == 0 {
		return &g
	}
	cands := sortedKeys(d.Translations)
	if d.Language != "" {
		cands = append([]string{d.Language}, cands...) // its own text wins a tie
	}
	key := langtag.Best(want, cands)
	tr := d.Translations[key]
	if key == "" || key == d.Language || tr == nil {
		return &g
	}
	if m := tr.Details; m != nil {
		if g.Details == nil {
			g.Details = &Details{}
		}
		x := g.Details
		x.Title, x.SortTitle = first(m.Title, x.Title), first(m.SortTitle, x.SortTitle)
		x.Overview, x.Series = first(m.Overview, x.Overview), first(m.Series, x.Series)
		if len(m.Genres) > 0 {
			x.Genres = m.Genres
		}
		if len(m.Tags) > 0 {
			x.Tags = m.Tags
		}
	}
	for i := range g.Cheats {
		if l, ok := tr.Cheats[g.Cheats[i].ID]; ok && l != "" {
			g.Cheats[i].Label = l
		}
	}
	g.Notes = first(tr.Notes, g.Notes)
	if g.Saves != nil {
		g.Saves.Notes = first(tr.Saves, g.Saves.Notes)
	}
	return &g
}

func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
