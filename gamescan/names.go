// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/wittywilla/wittyplay/gamearchive"
)

// Working out what a file name means: a game's title, and what two names of one game have
// in common. Moved from the server with the scan (its gamemeta names and library titles).

var (
	tagRe    = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
	discRe   = regexp.MustCompile(`(?i)[\s_-]*\b(?:dis[ck]|cd|side)[\s_-]*(?:[0-9]+|[a-d])(?:\s*of\s*[0-9]+)?\b`)
	spacesRe = regexp.MustCompile(`\s+`)
	// A Nintendo product code: KTR-P-CAFE, NTR-AZEE-USA.
	productCodeRe = regexp.MustCompile(`^(?:CTR|KTR|TWL|NTR|AGB|CGB|DMG|NUS)-[A-Z0-9]{1,4}-[A-Z0-9]{2,4}$`)
	// 000400000F700100 at the front of a name.
	titleIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{16}\s+`)
	// "Mega Game Rev PRG1", "Mega Game v1.0".
	revisionTailRe = regexp.MustCompile(`\s+(?:Rev\.?\s*(?:PRG)?[0-9A-Z][0-9A-Za-z.]*|v[0-9]+\.[0-9]+[a-z]?)$`)
	// A product code left in a file name: SLUS-00594, scus_941.63, SLES 12345.
	serialNameRe = regexp.MustCompile(`(?i)\b(SL[UEPK][SM]|SC[UEPK][SM]|SCED|SLED|SLPM|PAPX|PBPX|ULUS|ULES|ULJS|ULJM|UCUS|UCES|UCJS|NPUG|NPEG|NPJG)[-_ ]?([0-9]{3})[-_. ]?([0-9]{2})\b`)
)

// CleanName splits a file's base name into the game's title and what else the name
// says, e.g. "Mega Game, The (USA) (Rev 1)" -> "The Mega Game", "USA, Rev 1".
func CleanName(base string) (title, tags string) {
	var found []string
	for _, m := range tagRe.FindAllString(base, -1) {
		t := strings.Trim(strings.TrimSpace(m), "()[]")
		if productCodeRe.MatchString(t) {
			continue // a serial says nothing to a person
		}
		if long, ok := regionLetters[t]; ok {
			t = long
		}
		if t != "" && t != "!" {
			found = append(found, t)
		}
	}
	title = strings.TrimSpace(tagRe.ReplaceAllString(base, ""))
	title = strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
	// A 3DS title's id is often left at the front of its name.
	title = titleIDRe.ReplaceAllString(title, "")
	// Some lists write a revision after the name, with no brackets.
	if m := revisionTailRe.FindStringIndex(title); m != nil {
		found = append(found, strings.TrimSpace(title[m[0]:m[1]]))
		title = strings.TrimSpace(title[:m[0]])
	}
	for _, art := range []string{"The", "A", "An"} {
		if s, ok := strings.CutSuffix(title, ", "+art); ok {
			title = art + " " + s
			break
		}
		if a, b, ok := strings.Cut(title, ", "+art+" - "); ok {
			title = art + " " + a + " - " + b
			break
		}
	}
	if title == "" {
		title = base
	}
	return title, strings.Join(found, ", ")
}

// regionLetters are what sets of cartridges write for a region.
var regionLetters = map[string]string{"U": "USA", "J": "Japan", "E": "Europe", "W": "World", "G": "Germany", "F": "France", "S": "Spain", "I": "Italy",
	"JU": "Japan, USA", "UE": "USA, Europe", "JE": "Japan, Europe", "JUE": "Japan, USA, Europe", "A": "Australia", "K": "Korea", "C": "China"}

// SearchTitle takes off what no database calls a game: which disc this is, and a
// product code left in the name. "Dashes-for-spaces" names become words.
func SearchTitle(title string) string {
	t := discRe.ReplaceAllString(title, "")
	t = serialNameRe.ReplaceAllString(t, "")
	if !strings.Contains(t, " ") && strings.Count(t, "-") > 0 {
		t = strings.ReplaceAll(t, "-", " ")
	}
	t = spacesRe.ReplaceAllString(t, " ")
	t = strings.Trim(t, " -_.,")
	if t == "" {
		return title
	}
	return t
}

// foldTable covers what game names actually use. Anything else is kept as it is, so
// a Japanese title still matches itself.
var foldTable = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "æ", "ae", "ç", "c", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ñ", "n", "ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o", "œ", "oe",
	"ß", "ss", "ú", "u", "ù", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y", "ō", "o", "ū", "u", "ā", "a", "ē", "e", "ī", "i",
	"&", " and ", "+", " and ", "'", "", "’", "", "`", "", ".", "")

var romans = map[string]string{"ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6", "vii": "7", "viii": "8", "ix": "9", "x": "10",
	"xi": "11", "xii": "12", "xiii": "13", "xiv": "14", "xv": "15", "xvi": "16"}

// words are a name's words in a form that doesn't care about punctuation, case,
// articles, or whether the sequel is written II or 2.
func words(name string) []string {
	s := strings.ToLower(tagRe.ReplaceAllString(name, ""))
	s = foldTable.Replace(s)
	fields := strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	out := fields[:0]
	for i, w := range fields {
		if (w == "the" || w == "a" || w == "an") && (i == 0 || i == len(fields)-1) && len(fields) > 1 {
			continue
		}
		if r, ok := romans[w]; ok && i > 0 {
			w = r
		}
		out = append(out, w)
	}
	return out
}

// NameKey is what two names of the same game have in common.
func NameKey(name string) string { return strings.Join(words(name), " ") }

// ParseGameName splits a game's file name into its title and a short note made
// of its tags, e.g. "Mega Game, The (USA) (Rev 1)" -> "The Mega Game", "USA, Rev 1".
func ParseGameName(base string) (title, note string) { return CleanName(base) }

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

var articles = []string{"the ", "a ", "an "}

// SortTitle strips leading articles.
func SortTitle(t string) string {
	l := strings.ToLower(t)
	for _, a := range articles {
		if strings.HasPrefix(l, a) {
			return strings.TrimSpace(t[len(a):])
		}
	}
	return t
}

// gameBaseName is a game's file name without its extension. For an archive that's
// everything that makes it one, and the game's own extension if the name carries it
// too: "Game (USA).sfc.7z" and "Game (USA).tar.gz" are both "Game (USA)".
func gameBaseName(name string) string {
	if !gamearchive.IsArchive(name) {
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	base := gamearchive.BaseName(name)
	if x := strings.ToLower(filepath.Ext(base)); x != "" && consoles().known(x) {
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}
