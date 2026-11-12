// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package configfile reads and edits the settings files emulators keep, in their own formats:
// INI, TOML, YAML, XML and JSON. An edit changes only the value it names and leaves the rest of
// the file as the emulator wrote it, so a person's own changes, comments and keys this package
// knows nothing about survive.
//
// The files live in a folder the emulator can write to, and an emulator is treated as something
// that may have been taken over, so every reader here expects hostile input: it refuses files
// over MaxSize, never panics, and a value can never add a line, a section or an element of its
// own.
package configfile

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxSize is the largest file this reads or edits. An emulator's own is a few kilobytes.
const MaxSize = 4 << 20

// Edit sets one value. Path names it in the file's own terms (see Apply); Value is a bool, an
// int64, a float64 or a string.
type Edit struct {
	Path  []string
	Value any
}

// Entry is one value found in a file.
type Entry struct {
	Path  []string
	Value any
}

// Formats this package knows.
var Formats = []string{"ini", "toml", "yaml", "xml", "json"}

// Known says whether a format is one of Formats.
func Known(format string) bool {
	switch format {
	case "ini", "toml", "yaml", "xml", "json":
		return true
	}
	return false
}

// Apply makes edits to a file's text and returns the new text. old may be empty: the file is
// then made. Paths are, by format:
//
//   - ini: [section, key]
//   - toml: [table..., key], dotted tables as their parts
//   - yaml: [key, key, ...] through nested block maps
//   - xml: [element, element, ...] from the root element down to the one whose text is set
//   - json: [key, ...] through nested objects
//
// A value that would end up somewhere it can't be written plainly (in a TOML array, under a
// YAML key that holds a list, in an XML element with children of its own) is refused rather
// than guessed at.
func Apply(format string, old []byte, edits []Edit) ([]byte, error) {
	if len(old) > MaxSize {
		return nil, errTooLarge
	}
	for _, e := range edits {
		if err := checkEdit(format, e); err != nil {
			return nil, err
		}
	}
	var out []byte
	var err error
	switch format {
	case "ini":
		out, err = applyINI(old, edits)
	case "toml":
		out, err = applyTOML(old, edits)
	case "yaml":
		out, err = applyYAML(old, edits)
	case "xml":
		out, err = applyXML(old, edits)
	case "json":
		out, err = applyJSON(old, edits)
	default:
		return nil, fmt.Errorf("configfile: no format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if len(out) > MaxSize {
		return nil, errTooLarge
	}
	// Read the result back the way the next start will: an edit that landed somewhere it
	// doesn't read as itself (a hostile file can be laid out to make that happen) is refused
	// rather than written.
	if err := readsBack(format, out, edits); err != nil {
		return nil, err
	}
	return out, nil
}

func readsBack(format string, out []byte, edits []Edit) error {
	got, err := Entries(format, out)
	if err != nil {
		return fmt.Errorf("configfile: the edited file doesn't read: %w", err)
	}
	for i, e := range edits {
		if slices.ContainsFunc(edits[i+1:], func(later Edit) bool { return samePath(later.Path, e.Path) }) {
			continue // a later edit of the same value wins
		}
		j := slices.IndexFunc(got, func(en Entry) bool { return samePath(en.Path, e.Path) })
		if j < 0 || !SameValue(format, got[j].Value, e.Value) {
			return fmt.Errorf("configfile: %s doesn't read back as written", strings.Join(e.Path, "/"))
		}
	}
	return nil
}

// SameValue says whether a value read from a file is the one written: as text for INI (where
// space around it doesn't count) and XML, as the same type and value for the others.
func SameValue(format string, read, written any) bool {
	switch format {
	case "ini":
		r, ok := read.(string)
		return ok && strings.TrimSpace(r) == strings.TrimSpace(plainText(written))
	case "xml":
		r, ok := read.(string)
		return ok && r == plainText(written)
	}
	return read == written
}

// Entries lists the plain values a file holds, in the order they appear. Values are typed where
// the format types them (TOML, YAML, JSON); INI and XML values are text. A list or a table
// written in one value is given as its text.
func Entries(format string, data []byte) ([]Entry, error) {
	if len(data) > MaxSize {
		return nil, errTooLarge
	}
	switch format {
	case "ini":
		return entriesINI(data), nil
	case "toml":
		return entriesTOML(data)
	case "yaml":
		return entriesYAML(data)
	case "xml":
		return entriesXML(data)
	case "json":
		return entriesJSON(data)
	}
	return nil, fmt.Errorf("configfile: no format %q", format)
}

var errTooLarge = fmt.Errorf("configfile: a settings file over %d bytes", MaxSize)

// checkEdit refuses what no format may hold: a part of a path or a value that could start a
// line of its own, a NUL, text that isn't UTF-8, a number that isn't one.
func checkEdit(format string, e Edit) error {
	if len(e.Path) == 0 {
		return errors.New("configfile: an edit with no path")
	}
	if format == "ini" && len(e.Path) != 2 {
		return errors.New("configfile: an INI path is a section and a key")
	}
	if len(e.Path) > 32 {
		return errors.New("configfile: a path that deep")
	}
	for _, p := range e.Path {
		if p == "" || len(p) > 256 || !utf8.ValidString(p) || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return fmt.Errorf("configfile: the name %q can't be written", p)
		}
	}
	switch v := e.Value.(type) {
	case bool, int64:
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("configfile: a number that isn't one")
		}
	case string:
		if strings.ContainsAny(v, "\r\n\x00") || !utf8.ValidString(v) {
			return errors.New("configfile: a value must be one line of text")
		}
	default:
		return fmt.Errorf("configfile: a value of type %T", e.Value)
	}
	return nil
}

// plainText is a value as text, the way INI and XML files hold it.
func plainText(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case string:
		return t
	}
	return ""
}

// floatText is a float as TOML, YAML and JSON need it: always with a point or an exponent, so
// it reads back as a float rather than an integer.
func floatText(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// splitLines splits text into lines without their line ends, and remembers whether the file
// used CRLF so an edit can keep to it.
func splitLines(b []byte) (lines []string, crlf bool) {
	s := string(b)
	// Only a file that ends every line with CRLF is written back that way: a mixed one gets
	// plain line ends, which every emulator here reads.
	n := strings.Count(s, "\n")
	crlf = n > 0 && strings.Count(s, "\r\n") == n
	s = strings.TrimSuffix(s, "\n")
	if s == "" && len(b) <= 1 {
		return nil, crlf
	}
	lines = strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines, crlf
}

func joinLines(lines []string, crlf bool) []byte {
	if len(lines) == 0 {
		return nil
	}
	nl := "\n"
	if crlf {
		nl = "\r\n"
	}
	return []byte(strings.Join(lines, nl) + nl)
}

func samePath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasPrefix(p, prefix []string) bool {
	return len(p) >= len(prefix) && samePath(p[:len(prefix)], prefix)
}

func clonePath(p []string) []string { return append([]string(nil), p...) }

func insertLines(lines []string, at int, add ...string) []string {
	out := make([]string, 0, len(lines)+len(add))
	out = append(out, lines[:at]...)
	out = append(out, add...)
	return append(out, lines[at:]...)
}
