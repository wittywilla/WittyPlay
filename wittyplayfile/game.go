// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Version 2 makes a .wittyplay a game's own file too: beside a game, or shared with a friend, it
// can carry everything WittyPlay knows or can be told about that game. Its details, its pictures,
// which dump it was made for, how it is played, its controls, cheats and notes on its saves, and
// who made the file. Such a file is a ZIP archive holding manifest.json (the same JSON as a bare
// file) and the pictures it names (archive.go). A bare JSON file can still say all of it but the
// pictures. Version 1 files are read as they always were.

// About is everything a game's own file says about the game. Every part is optional.
type About struct {
	Details  *Details  `json:"details,omitempty"`
	Identity *Identity `json:"identity,omitempty"`
	// Artwork names a picture in the archive for each role (ArtworkRoles): {"boxfront":
	// "images/boxfront.png"}. Screenshots are a list of their own.
	Artwork     Artwork  `json:"artwork,omitempty"`
	Screenshots []string `json:"screenshots,omitempty"`
	Play        *Play    `json:"play,omitempty"`
	Cheats      []Cheat  `json:"cheats,omitempty"`
	Saves       *Saves   `json:"saves,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	More
}

// Details are a game's facts, which replace what WittyPlay's sources found.
type Details struct {
	Title         string `json:"title,omitempty"`
	SortTitle     string `json:"sortTitle,omitempty"`
	OriginalTitle string `json:"originalTitle,omitempty"` // in its own script: 「ゼルダの伝説」
	// Console is a console id, as WittyPlay names them ("snes", "ps1").
	Console     string   `json:"console,omitempty"`
	Year        *int     `json:"year,omitempty"`
	ReleaseDate string   `json:"releaseDate,omitempty"` // YYYY-MM-DD, YYYY-MM or YYYY
	Overview    string   `json:"overview,omitempty"`
	Genres      []string `json:"genres,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Developer   string   `json:"developer,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Series      string   `json:"series,omitempty"`
	Players     *Players `json:"players,omitempty"`
	// Rating is a score out of 10. AgeRatings is a rating per board: {"ESRB": "E", "PEGI": "3"}.
	Rating     *float64          `json:"rating,omitempty"`
	AgeRatings map[string]string `json:"ageRatings,omitempty"`
	// Regions are where this release is from, as dump names say it: "USA", "Europe", "Japan".
	Regions []string `json:"regions,omitempty"`
	// Languages the game itself can be played in, as language tags.
	Languages []string `json:"languages,omitempty"`
	// ExternalIDs: the game's id in other catalogues, by a plain name of the catalogue.
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	More
}

// Players says how many can play, and how.
type Players struct {
	Min    int  `json:"min,omitempty"`
	Max    int  `json:"max,omitempty"`
	Coop   bool `json:"coop,omitempty"`
	Versus bool `json:"versus,omitempty"`
	Online bool `json:"online,omitempty"`
	More
}

// Identity says which dump a file was made for: a file made for another region or another dump
// is flagged and not applied (identity.go).
type Identity struct {
	FileName string `json:"fileName,omitempty"`
	Size     int64  `json:"size,omitempty"`
	CRC32    string `json:"crc32,omitempty"` // 8 hex digits
	MD5      string `json:"md5,omitempty"`   // 32
	SHA1     string `json:"sha1,omitempty"`  // 40
	// Serial is a disc's or cartridge's product code: SLUS-00594.
	Serial string `json:"serial,omitempty"`
	// Files are the parts of a game of more than one file (a disc's tracks, a set's discs), each
	// identified as the game is.
	Files []FileID `json:"files,omitempty"`
	More
}

// FileID identifies one file of a game of several.
type FileID struct {
	Name  string `json:"name"`
	Size  int64  `json:"size,omitempty"`
	CRC32 string `json:"crc32,omitempty"`
	MD5   string `json:"md5,omitempty"`
	SHA1  string `json:"sha1,omitempty"`
	More
}

// Artwork roles.
var ArtworkRoles = []string{"boxfront", "boxback", "boxspine", "boxfull", "media", "logo", "marquee", "banner", "titlescreen", "fanart", "snapshot", "icon"}

// Artwork names a picture in the archive for each role.
type Artwork map[string]string

// Play holds how the game is best played. WittyPlay follows what it can; a person's own choices
// still come first.
type Play struct {
	Emulator    string            `json:"emulator,omitempty"` // "libretro" or a standalone emulator's id
	Core        string            `json:"core,omitempty"`     // a core's id, for libretro
	CoreOptions map[string]string `json:"coreOptions,omitempty"`
	Shader      string            `json:"shader,omitempty"`      // a shader preset's name
	AspectRatio string            `json:"aspectRatio,omitempty"` // "4:3", "8:7", "core", "stretch"
	Integer     *bool             `json:"integerScaling,omitempty"`
	Rotation    int               `json:"rotation,omitempty"` // 0, 90, 180 or 270
	// VideoStandard: "ntsc" or "pal", for a core that can be told.
	VideoStandard string `json:"videoStandard,omitempty"`
	// Disc: which disc of a set starts, from 1.
	Disc int `json:"disc,omitempty"`
	More
}

// Cheat is one cheat code, given to the core through its cheat interface. Only the characters
// codes are written in are allowed: a code is only ever read by the core.
type Cheat struct {
	ID      string `json:"id,omitempty"`
	Label   string `json:"label"`
	Code    string `json:"code"`
	Enabled bool   `json:"enabled,omitempty"`
	More
}

// Saves are notes about the game's saves: never the saves themselves, which are a person's own.
type Saves struct {
	// Kind: "battery", "memorycard", "password", "none".
	Kind string `json:"kind,omitempty"`
	// MemoryCard: "shared" (one card for every game) or "game" (a card of its own).
	MemoryCard string `json:"memoryCard,omitempty"`
	// Blocks a memory card save takes.
	Blocks int    `json:"blocks,omitempty"`
	Notes  string `json:"notes,omitempty"`
	More
}

// Authorship says who made the file, when, and with what.
type Authorship struct {
	Author  string `json:"author,omitempty"`
	Created string `json:"created,omitempty"` // RFC 3339
	Updated string `json:"updated,omitempty"`
	Tool    *Tool  `json:"tool,omitempty"`
	License string `json:"license,omitempty"` // an SPDX id, such as CC-BY-4.0
	Source  string `json:"source,omitempty"`  // where the details came from, in words
	More
}

// Tool is the program that wrote the file last.
type Tool struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	More
}

// Translation is a file's text in one language. Anything left out stays as the file has it.
type Translation struct {
	Details *Details `json:"details,omitempty"`
	// Cheats: a cheat's label, by its id.
	Cheats map[string]string `json:"cheats,omitempty"`
	Notes  string            `json:"notes,omitempty"`
	Saves  string            `json:"saves,omitempty"` // the saves' notes
	More
}

// ------------------------------------------------------------------ unknown fields

// More keeps the fields of an object this version doesn't know, so that a file written by a
// newer program loses nothing when an older one saves it again. They are never used.
type More struct {
	Unknown map[string]json.RawMessage `json:"-"`
}

var knownFields sync.Map // reflect.Type -> map[string]bool

func fieldsOf(t reflect.Type) map[string]bool {
	if v, ok := knownFields.Load(t); ok {
		return v.(map[string]bool)
	}
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || f.Anonymous {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = true
	}
	knownFields.Store(t, out)
	return out
}

// decodeKeeping fills v (a pointer to an alias of a struct with More) and returns the fields it
// didn't know.
func decodeKeeping(b []byte, v any) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(b, v); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	known := fieldsOf(reflect.TypeOf(v).Elem())
	var extra map[string]json.RawMessage
	for k, raw := range all {
		if known[k] {
			continue
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[k] = raw
	}
	return extra, nil
}

// encodeKeeping writes v (an alias) with the unknown fields put back after the known ones.
func encodeKeeping(v any, extra map[string]json.RawMessage) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(extra) == 0 {
		return b, err
	}
	known := fieldsOf(reflect.TypeOf(v).Elem())
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if !known[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.Write(b[:len(b)-1])
	for _, k := range keys {
		if buf.Len() > 1 {
			buf.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(extra[k])
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// Each type with More reads and writes itself through an alias, which has no methods.

func (x *About) UnmarshalJSON(b []byte) error {
	type a About
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x About) MarshalJSON() ([]byte, error) { type a About; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Details) UnmarshalJSON(b []byte) error {
	type a Details
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Details) MarshalJSON() ([]byte, error) {
	type a Details
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Players) UnmarshalJSON(b []byte) error {
	type a Players
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Players) MarshalJSON() ([]byte, error) {
	type a Players
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Identity) UnmarshalJSON(b []byte) error {
	type a Identity
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Identity) MarshalJSON() ([]byte, error) {
	type a Identity
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *FileID) UnmarshalJSON(b []byte) error {
	type a FileID
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x FileID) MarshalJSON() ([]byte, error) {
	type a FileID
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Play) UnmarshalJSON(b []byte) error {
	type a Play
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Play) MarshalJSON() ([]byte, error) { type a Play; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Cheat) UnmarshalJSON(b []byte) error {
	type a Cheat
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Cheat) MarshalJSON() ([]byte, error) { type a Cheat; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Saves) UnmarshalJSON(b []byte) error {
	type a Saves
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Saves) MarshalJSON() ([]byte, error) { type a Saves; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Authorship) UnmarshalJSON(b []byte) error {
	type a Authorship
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Authorship) MarshalJSON() ([]byte, error) {
	type a Authorship
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Tool) UnmarshalJSON(b []byte) error {
	type a Tool
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Tool) MarshalJSON() ([]byte, error) { type a Tool; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Translation) UnmarshalJSON(b []byte) error {
	type a Translation
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Translation) MarshalJSON() ([]byte, error) {
	type a Translation
	return encodeKeeping((*a)(&x), x.Unknown)
}
