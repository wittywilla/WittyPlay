// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package wittyplayfile reads and writes .wittyplay files, an open format for games. Version 1 is
// a small JSON file that says how games are pictured in WittyPlay (box art fitted or filled, or
// drawn in a cartridge or case, which case, and what goes on its label), for everything, per
// console and per game. Version 2 adds controls (controls.go) and makes a .wittyplay a game's own
// shareable file: its details, pictures, the dump it was made for, how it is played, cheats and
// notes on its saves, in a ZIP archive with manifest.json (game.go, archive.go). The format's open
// specification is docs/specs/wittyplay-file.md (CC0) with its JSON Schema beside it.
//
// A reader keeps what it doesn't know: unknown fields, at any level, are carried through a read and
// a write unchanged, so a file made by a newer program loses nothing passing through an older one.
package wittyplayfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The format's identity.
const (
	// Version is the newest version this reader understands; a file says its version in its
	// "wittyplay" field. Writers write the lowest version that holds what they write (MinVersion).
	Version = 2
	// Ext is the file extension, MediaType its registered-style media type.
	Ext       = ".wittyplay"
	MediaType = "application/vnd.wittyplay+json"
	// MaxSize is the largest bare JSON file read, and the largest manifest in an archive, in bytes.
	// (Version 1 files are at most 64 KiB; version 2 ones may carry a game's details and controls.)
	MaxSize = 1 << 20
	// Limits on what one file holds.
	MaxGames    = 2000
	MaxConsoles = 200
	MaxName     = 200
	MaxText     = 1000
)

// Picture values: how the art is shown.
const (
	PictureFitted = "fitted" // the whole box art, over a blurred, dimmed copy of itself
	PictureFilled = "filled" // the box art fills the tile, cropped to it
	PictureCase   = "case"   // drawn in a cartridge, disc case or the like
)

// Label values: what goes on a case's label.
const (
	LabelBoxArt = "boxart"
	LabelLogo   = "logo"
	LabelTitle  = "title"
)

// CaseAuto lets the console decide the case.
const CaseAuto = "auto"

// CaseStyles are the case drawings this version names. A file may name another (a newer program's);
// a reader that doesn't know it draws the console's own.
var CaseStyles = []string{
	"cart-nes", "cart-atari", "cart-super", "cart-64", "cart-mega", "cart-pocket", "cart-advance", "card-slim", "card-hu",
	"cart-nes-angled", "cart-atari-angled", "cart-super-angled", "cart-64-angled", "cart-mega-angled", "cart-pocket-angled",
	"cart-advance-angled", "card-slim-angled", "box-cart", "box-cart-wide", "box-small", "box-big", "case-clamshell", "case-card",
	"case-jewel", "case-keep", "case-keep-angled", "case-blu", "case-blu-angled", "case-mini", "disc-bare", "disc-gd",
	"disc-mini", "umd-bare", "case-umd", "disk-floppy", "disk-525", "tape-cassette", "case-tape", "arcade-marquee", "arcade-pcb",
	"digital-tile", "generic-box", "cart-nes-gold", "cart-nes-gold-angled", "cart-nes-dark", "cart-nes-dark-angled",
	"cart-nes-clear", "cart-nes-clear-angled", "cart-atari-wood", "cart-atari-wood-angled", "cart-atari-gold",
	"cart-atari-gold-angled", "cart-atari-clear", "cart-atari-clear-angled", "cart-super-gold", "cart-super-gold-angled",
	"cart-super-dark", "cart-super-dark-angled", "cart-super-clear", "cart-super-clear-angled", "cart-64-gold",
	"cart-64-gold-angled", "cart-64-red", "cart-64-red-angled", "cart-64-blue", "cart-64-blue-angled", "cart-64-clear",
	"cart-64-clear-angled", "cart-mega-gold", "cart-mega-gold-angled", "cart-mega-red", "cart-mega-red-angled", "cart-mega-clear",
	"cart-mega-clear-angled", "cart-pocket-red", "cart-pocket-red-angled", "cart-pocket-blue", "cart-pocket-blue-angled",
	"cart-pocket-green", "cart-pocket-green-angled", "cart-pocket-gold", "cart-pocket-gold-angled", "cart-pocket-clear",
	"cart-pocket-clear-angled", "cart-advance-clear", "cart-advance-clear-angled", "cart-advance-gold",
	"cart-advance-gold-angled", "cart-advance-blue", "cart-advance-blue-angled", "card-slim-red", "card-slim-red-angled",
	"card-slim-blue", "card-slim-blue-angled", "card-slim-green", "card-slim-green-angled", "card-slim-white",
	"card-slim-white-angled", "case-jewel-angled", "case-jewel-white", "case-jewel-white-angled", "case-jewel-clear",
	"case-jewel-clear-angled", "case-keep-white", "case-keep-white-angled", "case-keep-green", "case-keep-green-angled",
	"case-keep-blue", "case-keep-blue-angled", "case-keep-red", "case-keep-red-angled", "case-blu-dark", "case-blu-dark-angled",
	"case-blu-white", "case-blu-white-angled", "case-blu-green", "case-blu-green-angled", "case-mini-angled", "case-mini-white",
	"case-mini-white-angled", "case-mini-dark", "case-mini-dark-angled", "case-clamshell-angled", "case-clamshell-white",
	"case-clamshell-white-angled", "case-clamshell-clear", "case-clamshell-clear-angled", "case-card-angled", "case-card-dark",
	"case-card-dark-angled", "case-card-red", "case-card-red-angled", "case-card-blue", "case-card-blue-angled",
	"case-umd-angled", "case-umd-dark", "case-umd-dark-angled", "case-umd-blue", "case-umd-blue-angled", "case-tape-angled",
	"case-tape-white", "case-tape-white-angled", "case-tape-dark", "case-tape-dark-angled", "box-tall", "box-flip",
	"box-cart-white", "box-cart-dark", "box-cart-wide-white", "box-cart-wide-dark", "box-small-white", "box-small-dark",
	"box-big-white", "box-big-dark", "box-tall-white", "box-tall-dark", "box-flip-white", "box-flip-dark", "card-hu-dark",
	"card-hu-gold", "card-hu-blue", "disc-bd", "disc-dark", "disc-mini-dark", "disc-sleeve", "disc-sleeve-gd", "umd-bare-angled",
	"umd-bare-clear", "umd-bare-blue", "disk-floppy-blue", "disk-floppy-red", "disk-floppy-green", "disk-floppy-white",
	"disk-525-blue", "disk-525-red", "disk-525-clear", "tape-cassette-angled", "tape-cassette-white", "tape-cassette-dark",
	"tape-cassette-clear", "arcade-cabinet", "arcade-cabinet-angled", "arcade-cabinet-red", "arcade-cabinet-blue",
	"arcade-marquee-red", "arcade-marquee-blue", "arcade-marquee-green", "digital-poster", "digital-banner", "digital-window",
	"digital-tile-red", "digital-tile-blue", "digital-tile-green", "digital-tile-gold", "digital-tile-dark",
}

// Media are the kinds of medium games came on, each with its own case styles (docs/specs/wittyplay-file.md, "Media"): a look's
// "cases" chooses a style per medium.
var Media = []string{
	"cart-8bit", "cart-early", "cart-16bit", "cart-mega", "cart-64", "cart-pocket", "cart-advance", "card", "hucard",
	"cd", "gd", "dvd", "bluray", "minidisc", "umd", "floppy35", "floppy525", "tape", "arcade", "digital", "other",
}

var (
	pictures  = map[string]bool{PictureFitted: true, PictureFilled: true, PictureCase: true}
	labels    = map[string]bool{LabelBoxArt: true, LabelLogo: true, LabelTitle: true}
	caseID    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	consoleID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// Errors a caller can tell apart.
var (
	ErrTooBig = fmt.Errorf("a .wittyplay file is at most %d KB, or %d MB as an archive", MaxSize>>10, MaxFile>>20)
)

// Extra holds the fields a reader didn't know, as they were.
type Extra map[string]json.RawMessage

// Look is how pictures are shown at one level. Every field is optional: an empty one is decided by
// the level above.
type Look struct {
	Picture string `json:"picture,omitempty"`
	Case    string `json:"case,omitempty"`
	Label   string `json:"label,omitempty"`
	// Cases is a case style per kind of medium (Media), for every game of that medium the look covers.
	Cases map[string]string `json:"cases,omitempty"`
	Extra Extra             `json:"-"`
}

// Empty says nothing is set.
func (l Look) Empty() bool {
	return l.Picture == "" && l.Case == "" && l.Label == "" && len(l.Cases) == 0 && len(l.Extra) == 0
}

// Over is l with every field it leaves empty taken from under.
func (l Look) Over(under Look) Look {
	if l.Picture == "" {
		l.Picture = under.Picture
	}
	if l.Case == "" {
		l.Case = under.Case
	}
	if l.Label == "" {
		l.Label = under.Label
	}
	if len(under.Cases) > 0 {
		merged := make(map[string]string, len(l.Cases)+len(under.Cases))
		for k, v := range under.Cases {
			merged[k] = v
		}
		for k, v := range l.Cases {
			merged[k] = v
		}
		l.Cases = merged
	}
	return l
}

// Match says which game an entry is for. Item is a server's own id, meaningful only on the server
// that wrote it; File a ROM's file name; Title and Console a game's name on a console.
type Match struct {
	Item    int64  `json:"item,omitempty"`
	File    string `json:"file,omitempty"`
	Title   string `json:"title,omitempty"`
	Console string `json:"console,omitempty"`
	Extra   Extra  `json:"-"`
}

// Game is one game's look.
type Game struct {
	Match Match `json:"match"`
	Look
}

// Doc is a whole file.
type Doc struct {
	Version     int             `json:"wittyplay"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Generator   string          `json:"generator,omitempty"`
	Default     *Look           `json:"default,omitempty"`
	Consoles    map[string]Look `json:"consoles,omitempty"`
	Games       []Game          `json:"games,omitempty"`
	// Version 2. Controls: how games are played, by default and per console (controls.go). Game:
	// everything about the one game a game's own file is for (game.go). Authorship: who made the
	// file. Language and Translations: the language its text is in, and that text in others
	// (lang.go). Extensions: anything a program wants kept, under a name of its own ("tool.key").
	Controls     *Controls                  `json:"controls,omitempty"`
	Game         *About                     `json:"game,omitempty"`
	Authorship   *Authorship                `json:"authorship,omitempty"`
	Language     string                     `json:"language,omitempty"`
	Translations map[string]*Translation    `json:"translations,omitempty"`
	Extensions   map[string]json.RawMessage `json:"extensions,omitempty"`
	Extra        Extra                      `json:"-"`
	// Files: an archive's other members by name ("images/boxfront.png"), checked.
	Files map[string][]byte `json:"-"`

	// read is the version the file said when it was parsed: a write never says a lower one.
	read int
}

// Read reads one file from r, archive or bare JSON, refusing anything over MaxFile.
func Read(r io.Reader) (*Doc, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxFile+1))
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads and checks a file's bytes: an archive (a game's own file, with its pictures) or a
// bare JSON file. A file with anything wrong gives an error and no Doc; the parts version 2 added
// are checked all at once, and their problems come as an *Error listing every one.
func Parse(b []byte) (*Doc, error) {
	if IsArchive(b) {
		if len(b) > MaxFile {
			return nil, ErrTooBig
		}
		man, files, err := readArchive(b)
		if err != nil {
			return nil, err
		}
		d, err := parseJSON(man)
		if err != nil {
			return nil, err
		}
		d.Files = files
		if err := d.Validate(); err != nil {
			return nil, err
		}
		return d, nil
	}
	d, err := parseJSON(b)
	if err != nil {
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d, nil
}

// ParseManifest reads a bare JSON document without checking it, for a program that will add
// pictures to it before writing it (EncodeAny checks it all then).
func ParseManifest(b []byte) (*Doc, error) { return parseJSON(b) }

func parseJSON(b []byte) (*Doc, error) {
	if len(b) > MaxSize {
		return nil, ErrTooBig
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return nil, errors.New("a .wittyplay file is UTF-8 text")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil || top == nil {
		return nil, errors.New("a .wittyplay file is one JSON object")
	}
	if err := checkDepth(b, 24); err != nil {
		return nil, err
	}
	d := &Doc{}
	if err := d.fromFields(top); err != nil {
		return nil, err
	}
	return d, nil
}

// checkDepth refuses JSON nested deeper than max.
func checkDepth(b []byte, max int) error {
	depth, inStr, esc := 0, false, false
	for _, c := range b {
		switch {
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			if depth++; depth > max {
				return fmt.Errorf("a .wittyplay file is nested at most %d deep", max)
			}
		case c == '}' || c == ']':
			depth--
		}
	}
	return nil
}

// MinVersion is the lowest version that holds what the document says: 1 unless it uses a part
// version 2 added.
func (d *Doc) MinVersion() int {
	if d.Controls != nil || d.Game != nil || d.Authorship != nil || d.Language != "" || len(d.Translations) > 0 || len(d.Extensions) > 0 {
		return 2
	}
	return 1
}

// Newer says the file follows a later version of the format than this package knows. Such a
// file is read for what this version knows; everything else is kept and written back as it was.
func (d *Doc) Newer() bool { return d.Version > Version || d.read > Version }

// writeVersion is the version a write says: the lowest that holds the content, but never lower
// than the version the file was read at.
func (d *Doc) writeVersion() int {
	v := d.MinVersion()
	if d.read > v {
		v = d.read
	}
	if d.Version > Version && d.Version > v {
		v = d.Version
	}
	return v
}

// Validate checks a document against the format's rules.
func (d *Doc) Validate() error {
	if d.Version < 1 {
		return errors.New(`"wittyplay" is the format's version, 1 or more`)
	}
	if utf8.RuneCountInString(d.Name) > MaxName {
		return fmt.Errorf(`"name" is at most %d characters`, MaxName)
	}
	if utf8.RuneCountInString(d.Description) > MaxText || utf8.RuneCountInString(d.Generator) > MaxName {
		return errors.New(`"description" or "generator" is too long`)
	}
	if d.Default != nil {
		if err := d.Default.check("default"); err != nil {
			return err
		}
	}
	if len(d.Consoles) > MaxConsoles {
		return fmt.Errorf("at most %d consoles", MaxConsoles)
	}
	for id, l := range d.Consoles {
		if !consoleID.MatchString(id) {
			return fmt.Errorf("%q is not a console id (lower-case letters, digits, - and _)", id)
		}
		if err := l.check("consoles." + id); err != nil {
			return err
		}
	}
	if len(d.Games) > MaxGames {
		return fmt.Errorf("at most %d games", MaxGames)
	}
	for i, g := range d.Games {
		where := fmt.Sprintf("games[%d]", i)
		if err := g.Match.check(where); err != nil {
			return err
		}
		if err := g.Look.check(where); err != nil {
			return err
		}
	}
	c := &checker{newer: d.Newer()}
	d.checkV2(c)
	if len(c.problems) > 0 {
		return &Error{Problems: c.problems}
	}
	return nil
}

func (l Look) check(where string) error {
	if l.Picture != "" && !pictures[l.Picture] {
		return fmt.Errorf(`%s: "picture" is fitted, filled or case`, where)
	}
	if l.Label != "" && !labels[l.Label] {
		return fmt.Errorf(`%s: "label" is boxart, logo or title`, where)
	}
	if l.Case != "" && !caseID.MatchString(l.Case) {
		return fmt.Errorf(`%s: "case" is a case style id`, where)
	}
	if len(l.Cases) > 64 {
		return fmt.Errorf(`%s: "cases" names at most 64 media`, where)
	}
	for media, style := range l.Cases {
		if !caseID.MatchString(media) || !caseID.MatchString(style) {
			return fmt.Errorf(`%s: "cases" maps a medium id to a case style id`, where)
		}
	}
	return nil
}

func (m Match) check(where string) error {
	if m.Item == 0 && m.File == "" && m.Title == "" {
		return fmt.Errorf(`%s: "match" names an item, a file or a title`, where)
	}
	if m.Item < 0 {
		return fmt.Errorf(`%s: "item" is a positive number`, where)
	}
	if strings.ContainsAny(m.File, `/\`) || m.File == "." || m.File == ".." {
		return fmt.Errorf(`%s: "file" is a file name, without a folder`, where)
	}
	if utf8.RuneCountInString(m.File) > MaxName || utf8.RuneCountInString(m.Title) > MaxName {
		return fmt.Errorf(`%s: a name is at most %d characters`, where, MaxName)
	}
	if m.Console != "" && !consoleID.MatchString(m.Console) {
		return fmt.Errorf(`%s: "console" is a console id`, where)
	}
	return nil
}

// KnownCase says the case style is one this version draws.
func KnownCase(id string) bool {
	for _, c := range CaseStyles {
		if c == id {
			return true
		}
	}
	return false
}

// For is the look a document gives one game: its default, then its console's, then each entry
// that matches, a later entry over an earlier one. A file name or title is matched
// without regard to case.
func (d *Doc) For(item int64, console, file, title string) Look {
	var out Look
	for i := len(d.Games) - 1; i >= 0; i-- {
		if d.Games[i].Match.Matches(item, console, file, title) {
			out = out.Over(d.Games[i].Look)
		}
	}
	if c, ok := d.Consoles[console]; ok && console != "" {
		out = out.Over(c)
	}
	if d.Default != nil {
		out = out.Over(*d.Default)
	}
	out.Extra = nil
	return out
}

// Matches says the entry is for this game: every field it gives agrees.
func (m Match) Matches(item int64, console, file, title string) bool {
	if m.Item == 0 && m.File == "" && m.Title == "" {
		return false
	}
	if m.Item != 0 && m.Item != item {
		return false
	}
	if m.File != "" && !strings.EqualFold(m.File, file) {
		return false
	}
	if m.Title != "" && !strings.EqualFold(strings.TrimSpace(m.Title), strings.TrimSpace(title)) {
		return false
	}
	return m.Console == "" || m.Console == console
}

// ---- reading and writing, keeping what isn't known ----

func take[T any](f map[string]json.RawMessage, key string, into *T) error {
	raw, ok := f[key]
	if !ok {
		return nil
	}
	delete(f, key)
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("%q has the wrong type", key)
	}
	return nil
}

// takeExplained is take, saying where in the part decoding failed.
func takeExplained(f map[string]json.RawMessage, key string, into any) error {
	raw, ok := f[key]
	if !ok {
		return nil
	}
	delete(f, key)
	if string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, into); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) && te.Field != "" {
			return refuse(key, "%s has the wrong kind of value", te.Field)
		}
		return refuse(key, "can't be read: %v", err)
	}
	return nil
}

func rest(f map[string]json.RawMessage) Extra {
	if len(f) == 0 {
		return nil
	}
	return Extra(f)
}

func (d *Doc) fromFields(f map[string]json.RawMessage) error {
	raw, ok := f["wittyplay"]
	if !ok {
		return errors.New(`a .wittyplay file starts with "wittyplay": 1`)
	}
	var v json.Number
	if len(raw) == 0 || raw[0] == '"' {
		return errors.New(`"wittyplay" is the format's version number`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return errors.New(`"wittyplay" is the format's version number`)
	}
	n, err := v.Int64()
	if err != nil || n > 1<<20 {
		return errors.New(`"wittyplay" is a whole number`)
	}
	delete(f, "wittyplay")
	if n < 1 {
		return errors.New(`"wittyplay" is the format's version, 1 or more`)
	}
	d.Version = int(n)
	d.read = d.Version
	for _, k := range []struct {
		key string
		to  *string
	}{{"name", &d.Name}, {"description", &d.Description}, {"generator", &d.Generator}} {
		if err := take(f, k.key, k.to); err != nil {
			return err
		}
	}
	if err := take(f, "default", &d.Default); err != nil {
		return err
	}
	if err := take(f, "consoles", &d.Consoles); err != nil {
		return err
	}
	if err := take(f, "games", &d.Games); err != nil {
		return err
	}
	for _, k := range []struct {
		key string
		to  any
	}{{"controls", &d.Controls}, {"game", &d.Game}, {"authorship", &d.Authorship}, {"language", &d.Language},
		{"translations", &d.Translations}, {"extensions", &d.Extensions}} {
		if err := takeExplained(f, k.key, k.to); err != nil {
			return err
		}
	}
	d.Extra = rest(f)
	return nil
}

func (l *Look) UnmarshalJSON(b []byte) error {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(b, &f); err != nil {
		return errors.New("a look is an object")
	}
	*l = Look{}
	for _, k := range []struct {
		key string
		to  *string
	}{{"picture", &l.Picture}, {"case", &l.Case}, {"label", &l.Label}} {
		if err := take(f, k.key, k.to); err != nil {
			return err
		}
	}
	if err := take(f, "cases", &l.Cases); err != nil {
		return err
	}
	l.Extra = rest(f)
	return nil
}

func (l Look) fields() map[string]any {
	out := map[string]any{}
	for k, v := range l.Extra {
		out[k] = v
	}
	put(out, "picture", l.Picture)
	put(out, "case", l.Case)
	put(out, "label", l.Label)
	if len(l.Cases) > 0 {
		out["cases"] = l.Cases
	}
	return out
}

func (l Look) MarshalJSON() ([]byte, error) { return marshalSorted(l.fields()) }

func (m *Match) UnmarshalJSON(b []byte) error {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(b, &f); err != nil {
		return errors.New(`"match" is an object`)
	}
	*m = Match{}
	if err := take(f, "item", &m.Item); err != nil {
		return err
	}
	for _, k := range []struct {
		key string
		to  *string
	}{{"file", &m.File}, {"title", &m.Title}, {"console", &m.Console}} {
		if err := take(f, k.key, k.to); err != nil {
			return err
		}
	}
	m.Extra = rest(f)
	return nil
}

func (m Match) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, v := range m.Extra {
		out[k] = v
	}
	if m.Item != 0 {
		out["item"] = m.Item
	}
	put(out, "file", m.File)
	put(out, "title", m.Title)
	put(out, "console", m.Console)
	return marshalSorted(out)
}

func (g *Game) UnmarshalJSON(b []byte) error {
	var f map[string]json.RawMessage
	if err := json.Unmarshal(b, &f); err != nil {
		return errors.New("a game is an object")
	}
	*g = Game{}
	raw, ok := f["match"]
	if !ok {
		return errors.New(`a game has a "match"`)
	}
	delete(f, "match")
	if err := json.Unmarshal(raw, &g.Match); err != nil {
		return err
	}
	rest, _ := json.Marshal(f)
	return json.Unmarshal(rest, &g.Look)
}

func (g Game) MarshalJSON() ([]byte, error) {
	out := g.Look.fields()
	out["match"] = g.Match
	return marshalSorted(out)
}

// MarshalJSON writes the document with "wittyplay" first, so a person opening it sees what it is.
func (d Doc) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	for k, v := range d.Extra {
		out[k] = v
	}
	put(out, "name", d.Name)
	put(out, "description", d.Description)
	put(out, "generator", d.Generator)
	if d.Default != nil {
		out["default"] = *d.Default
	}
	if len(d.Consoles) > 0 {
		out["consoles"] = d.Consoles
	}
	if len(d.Games) > 0 {
		out["games"] = d.Games
	}
	if d.Controls != nil {
		out["controls"] = d.Controls
	}
	if d.Game != nil {
		out["game"] = d.Game
	}
	if d.Authorship != nil {
		out["authorship"] = d.Authorship
	}
	put(out, "language", d.Language)
	if len(d.Translations) > 0 {
		out["translations"] = d.Translations
	}
	if len(d.Extensions) > 0 {
		out["extensions"] = d.Extensions
	}
	body, err := marshalSorted(out)
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf(`{"wittyplay":%d`, d.writeVersion())
	if len(body) > 2 {
		return []byte(head + "," + string(body[1:])), nil
	}
	return []byte(head + "}"), nil
}

// Encode writes the document as a file: indented, ending in a new line.
func (d *Doc) Encode() ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func put(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// marshalSorted writes an object's keys in order, so the same document is always the same bytes.
func marshalSorted(m map[string]any) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := json.Marshal(m[k])
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
