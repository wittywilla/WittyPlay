// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TOML, as melonDS and xemu write it: tables, dotted tables, and "key = value" lines holding
// strings, numbers and booleans. Arrays and inline tables are read as their text and never
// rewritten. This reads line by line rather than building a tree, so whatever it doesn't edit
// stays exactly as it was.

// tomlLine is what one line of a TOML file is.
type tomlLine struct {
	kind  int      // tomlOther, tomlTable or tomlKey
	table []string // the table the line is in (for a header: the one it opens)
	key   []string // a key line's own key, dotted parts split
	// The value's place in the line, for a key line, and whether it's a plain scalar this may
	// rewrite.
	valStart, valEnd int
	scalar           bool
	array            bool // a header for an array of tables: nothing in it is edited
	last             int  // a key line's value's last line: a list may run on
}

const (
	tomlOther = iota // blank, or a comment
	tomlTable
	tomlKey
	tomlMore // the rest of a value begun on a line above
)

// scanTOML reads a file's lines. It refuses one whose strings or arrays don't end, since a
// line added after them would be read as part of them.
func scanTOML(lines []string) ([]tomlLine, error) {
	out := make([]tomlLine, len(lines))
	var table []string
	inArray := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		t := strings.TrimSpace(l)
		if t == "" || t[0] == '#' {
			continue
		}
		if t[0] == '[' {
			arr := strings.HasPrefix(t, "[[")
			inner := t[1:]
			if arr {
				inner = t[2:]
			}
			parts, rest, err := tomlKeyParts(inner)
			if err != nil {
				return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
			}
			closing := "]"
			if arr {
				closing = "]]"
			}
			rest = strings.TrimSpace(rest)
			if !strings.HasPrefix(rest, closing) {
				return nil, fmt.Errorf("configfile: line %d: a table header that doesn't close", i+1)
			}
			if after := strings.TrimSpace(rest[len(closing):]); after != "" && after[0] != '#' {
				return nil, fmt.Errorf("configfile: line %d: something after a table header", i+1)
			}
			table, inArray = parts, arr
			out[i] = tomlLine{kind: tomlTable, table: clonePath(table), array: arr}
			continue
		}
		lead := len(l) - len(strings.TrimLeft(l, " \t"))
		parts, rest, err := tomlKeyParts(l[lead:])
		if err != nil {
			return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
		}
		rest = strings.TrimLeft(rest, " \t")
		if !strings.HasPrefix(rest, "=") {
			return nil, fmt.Errorf("configfile: line %d: a key without a value", i+1)
		}
		valStart := len(l) - len(rest) + 1
		for valStart < len(l) && (l[valStart] == ' ' || l[valStart] == '\t') {
			valStart++
		}
		kl := tomlLine{kind: tomlKey, table: clonePath(table), key: parts, valStart: valStart, array: inArray}
		// A value that runs over more than one line: a multi-line string or an array.
		end, scalar, more, err := tomlValueEnd(l, valStart)
		if err != nil {
			return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
		}
		if more != "" {
			j, err := tomlFindClose(lines, i, valStart, more)
			if err != nil {
				return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
			}
			kl.valEnd, kl.scalar, kl.last = len(l), false, j
			out[i] = kl
			for k := i + 1; k <= j; k++ {
				out[k] = tomlLine{kind: tomlMore, table: clonePath(table)}
			}
			i = j
			continue
		}
		kl.valEnd, kl.scalar, kl.last = end, scalar, i
		out[i] = kl
	}
	return out, nil
}

var tomlBare = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// tomlKeyParts reads a dotted key from the start of s and returns its parts and what follows.
func tomlKeyParts(s string) (parts []string, rest string, err error) {
	for {
		s = strings.TrimLeft(s, " \t")
		var part string
		switch {
		case strings.HasPrefix(s, `"`):
			v, n, err := tomlBasicString(s)
			if err != nil {
				return nil, "", err
			}
			part, s = v, s[n:]
		case strings.HasPrefix(s, "'"):
			end := strings.IndexByte(s[1:], '\'')
			if end < 0 {
				return nil, "", errors.New("a key's quote doesn't close")
			}
			part, s = s[1:1+end], s[2+end:]
		default:
			m := tomlBare.FindString(s)
			if m == "" {
				return nil, "", errors.New("not a key")
			}
			part, s = m, s[len(m):]
		}
		parts = append(parts, part)
		t := strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(t, ".") {
			return parts, s, nil
		}
		s = t[1:]
	}
}

// tomlBasicString reads a "double-quoted" string at the start of s: its value and its length.
func tomlBasicString(s string) (string, int, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), i + 1, nil
		case '\\':
			if i+1 >= len(s) {
				return "", 0, errors.New("a string that doesn't close")
			}
			i++
			switch s[i] {
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case 'u', 'U':
				n := 4
				if s[i] == 'U' {
					n = 8
				}
				if i+n >= len(s) {
					return "", 0, errors.New("a short escape")
				}
				r, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
				if err != nil {
					return "", 0, errors.New("a bad escape")
				}
				b.WriteRune(rune(r))
				i += n
			default:
				return "", 0, errors.New("a bad escape")
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, errors.New("a string that doesn't close")
}

// tomlValueEnd finds where the value starting at l[at] ends on its line. scalar says it's a
// string, number or boolean; more, when not "", is what closes a value that goes on past this
// line.
func tomlValueEnd(l string, at int) (end int, scalar bool, more string, err error) {
	s := l[at:]
	switch {
	case s == "":
		return 0, false, "", errors.New("a key without a value")
	case strings.HasPrefix(s, `"""`), strings.HasPrefix(s, "'''"):
		q := s[:3]
		if i := strings.Index(s[3:], q); i >= 0 {
			return at + 3 + i + 3, false, "", nil
		}
		return len(l), false, q, nil
	case s[0] == '"':
		_, n, err := tomlBasicString(s)
		if err != nil {
			return 0, false, "", err
		}
		return at + n, true, "", nil
	case s[0] == '\'':
		i := strings.IndexByte(s[1:], '\'')
		if i < 0 {
			return 0, false, "", errors.New("a string that doesn't close")
		}
		return at + i + 2, true, "", nil
	case s[0] == '[' || s[0] == '{':
		depth, i, err := tomlBrackets(s, 0)
		if err != nil {
			return 0, false, "", err
		}
		if depth > 0 {
			if s[0] == '{' {
				return 0, false, "", errors.New("an inline table over more than one line")
			}
			return len(l), false, "]", nil
		}
		return at + i, false, "", nil
	}
	// A bare value: a number, a boolean or a date, up to a comment or the end.
	end = len(l)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		end = at + i
	}
	for end > at && (l[end-1] == ' ' || l[end-1] == '\t') {
		end--
	}
	return end, true, "", nil
}

// tomlBrackets walks s from i, counting brackets outside strings, until they balance or the
// line ends. It returns the depth left and where it stopped.
func tomlBrackets(s string, depth int) (int, int, error) {
	i := 0
	for i < len(s) {
		c := s[i]
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth <= 0 {
				return 0, i + 1, nil
			}
		case '#':
			return depth, len(s), nil
		case '"', '\'':
			if strings.HasPrefix(s[i:], `"""`) || strings.HasPrefix(s[i:], "'''") {
				return 0, 0, errors.New("a multi-line string inside a list")
			}
			if c == '"' {
				_, n, err := tomlBasicString(s[i:])
				if err != nil {
					return 0, 0, err
				}
				i += n
				continue
			}
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return 0, 0, errors.New("a string that doesn't close")
			}
			i += j + 2
			continue
		}
		i++
	}
	return depth, i, nil
}

// tomlFindClose finds the line that closes a value begun on line i.
func tomlFindClose(lines []string, i, valStart int, closing string) (int, error) {
	if closing == "]" {
		// Count again from the value's start, carrying the depth across lines.
		depth, _, err := tomlBrackets(lines[i][valStart:], 0)
		if err != nil {
			return 0, err
		}
		for j := i + 1; j < len(lines); j++ {
			d, _, err := tomlBrackets(lines[j], depth)
			if err != nil {
				return 0, err
			}
			if d <= 0 {
				return j, nil
			}
			depth = d
		}
		return 0, errors.New("a list that doesn't close")
	}
	for j := i + 1; j < len(lines); j++ {
		if strings.Contains(lines[j], closing) {
			return j, nil
		}
	}
	return 0, errors.New("a string that doesn't close")
}

// tomlScalar reads a plain value's text as a bool, an int64, a float64 or a string. Anything
// else (a date, say) is given as its text.
func tomlScalar(s string) any {
	switch {
	case s == "true":
		return true
	case s == "false":
		return false
	case strings.HasPrefix(s, `"`):
		if v, n, err := tomlBasicString(s); err == nil && n == len(s) {
			return v
		}
	case strings.HasPrefix(s, "'") && len(s) >= 2:
		return s[1 : len(s)-1]
	}
	clean := strings.ReplaceAll(s, "_", "")
	if n, err := strconv.ParseInt(clean, 0, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil && !strings.HasPrefix(clean, "0x") {
		return f
	}
	switch clean {
	case "inf", "+inf", "-inf", "nan", "+nan", "-nan":
		return s
	}
	return s
}

func tomlKeyText(k string) string {
	if tomlBare.FindString(k) == k {
		return k
	}
	return tomlQuote(k)
}

func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlValueText(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return floatText(t)
	case string:
		return tomlQuote(t)
	}
	return `""`
}

func tomlDotted(parts []string) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = tomlKeyText(p)
	}
	return strings.Join(out, ".")
}

func applyTOML(old []byte, edits []Edit) ([]byte, error) {
	lines, crlf := splitLines(old)
	for _, e := range edits {
		scan, err := scanTOML(lines)
		if err != nil {
			return nil, err
		}
		lines, err = setTOML(lines, scan, e)
		if err != nil {
			return nil, err
		}
	}
	return joinLines(lines, crlf), nil
}

// fullPath is where a key line's value lives: its table, then its own dotted key.
func (t tomlLine) fullPath() []string {
	return append(clonePath(t.table), t.key...)
}

func setTOML(lines []string, scan []tomlLine, e Edit) ([]string, error) {
	want := e.Path
	val := tomlValueText(e.Value)
	for i, t := range scan {
		if t.kind != tomlKey || t.array {
			continue
		}
		p := t.fullPath()
		if samePath(p, want) {
			if !t.scalar {
				return nil, fmt.Errorf("configfile: %s holds a list or a table, which this doesn't rewrite", strings.Join(want, "."))
			}
			l := lines[i]
			lines[i] = l[:t.valStart] + val + l[t.valEnd:]
			return lines, nil
		}
		if hasPrefix(want, p) || hasPrefix(p, want) {
			// The path runs through a plain value, or names a table of values.
			return nil, fmt.Errorf("configfile: %s isn't a plain value here", strings.Join(want, "."))
		}
	}
	parent := want[:len(want)-1]
	for _, t := range scan {
		if t.kind == tomlTable && samePath(t.table, want) {
			return nil, fmt.Errorf("configfile: %s is a table", strings.Join(want, "."))
		}
		if t.kind == tomlTable && t.array && hasPrefix(want, t.table) {
			return nil, fmt.Errorf("configfile: %s is in a list of tables", strings.Join(want, "."))
		}
	}
	// The table is there: the key goes after its last key.
	if at, ok := tomlTableEnd(scan, parent); ok {
		return insertLines(lines, at, tomlKeyText(want[len(want)-1])+" = "+val), nil
	}
	// A table made by dotted keys inside another (display.quality.x = 1 under [display]): a
	// header for it now would define it twice, so the key goes beside the others, dotted too.
	for i := len(scan) - 1; i >= 0; i-- {
		t := scan[i]
		if t.kind != tomlKey || t.array {
			continue
		}
		if p := t.fullPath(); len(p) > len(parent) && hasPrefix(p, parent) && len(t.table) <= len(parent) {
			key := tomlDotted(want[len(t.table):])
			return insertLines(lines, t.last+1, key+" = "+val), nil
		}
	}
	if len(parent) == 0 {
		at := 0
		for at < len(scan) && scan[at].kind != tomlTable {
			at++
		}
		for at > 0 && strings.TrimSpace(lines[at-1]) == "" {
			at--
		}
		return insertLines(lines, at, tomlKeyText(want[0])+" = "+val), nil
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	return append(lines, "["+tomlDotted(parent)+"]", tomlKeyText(want[len(want)-1])+" = "+val), nil
}

// tomlTableEnd is where a new key for a table's header goes: after the table's last key,
// before the blank lines and comments that end it. ok is false when the file has no header for
// it.
func tomlTableEnd(scan []tomlLine, table []string) (int, bool) {
	if len(table) == 0 {
		return 0, false
	}
	start := -1
	for i, t := range scan {
		if t.kind == tomlTable && !t.array && samePath(t.table, table) {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, false
	}
	end := len(scan)
	for i := start + 1; i < len(scan); i++ {
		if scan[i].kind == tomlTable {
			end = i
			break
		}
	}
	for end > start+1 && scan[end-1].kind == tomlOther {
		end--
	}
	return end, true
}

func entriesTOML(data []byte) ([]Entry, error) {
	lines, _ := splitLines(data)
	scan, err := scanTOML(lines)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for i, t := range scan {
		if t.kind != tomlKey || t.array {
			continue
		}
		raw := lines[i][t.valStart:t.valEnd]
		var v any = raw
		if t.scalar {
			v = tomlScalar(raw)
		}
		out = append(out, Entry{Path: t.fullPath(), Value: v})
	}
	return out, nil
}
