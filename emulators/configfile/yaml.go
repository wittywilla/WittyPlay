// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// YAML, as RPCS3 writes its config.yml: block maps nested by indentation, "Key: value" lines
// whose keys may hold spaces and brackets, values plain or quoted. Lists, block text and flow
// collections ({} and []) are kept as they are and never rewritten. Anything else YAML allows
// (several documents, a value continued on the next line, tabs for indentation) is refused
// rather than half understood, since a line added in the wrong place would change what the
// file means.

type yamlLine struct {
	kind   int // yamlOther, yamlKey or yamlOpaque
	indent int
	path   []string
	// For a key line: where its value sits in the line, and what it is.
	valStart, valEnd int
	val              int // yamlScalar, yamlEmpty or yamlFixed
	quoted           byte
}

const (
	yamlOther  = iota // blank, a comment, a document marker
	yamlKey           // "Key: value" or "Key:"
	yamlOpaque        // part of a list or block text: kept, never read as keys
)

const (
	yamlScalar = iota // a plain or quoted value
	yamlEmpty         // nothing after the colon: a map (or a list) follows, or it's null
	yamlFixed         // block text, a flow collection, an anchor, alias or tag
)

func yamlIndent(l string) (int, error) {
	n := 0
	for n < len(l) && l[n] == ' ' {
		n++
	}
	if n < len(l) && l[n] == '\t' {
		return 0, errors.New("a tab in the indentation")
	}
	return n, nil
}

func scanYAML(lines []string) ([]yamlLine, error) {
	out := make([]yamlLine, len(lines))
	type level struct {
		indent int
		key    string
		val    int
	}
	var stack []level
	opaqueAbove := -1 // lines indented more than this are part of a list item or block text
	content := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || t[0] == '#' {
			continue
		}
		n, err := yamlIndent(l)
		if err != nil {
			return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
		}
		if opaqueAbove >= 0 && n > opaqueAbove {
			out[i] = yamlLine{kind: yamlOpaque, indent: n}
			continue
		}
		opaqueAbove = -1
		if n == 0 && (t == "---" || strings.HasPrefix(t, "--- ") || t[0] == '%') {
			if content {
				return nil, fmt.Errorf("configfile: line %d: more than one document", i+1)
			}
			continue
		}
		if n == 0 && (t == "..." || strings.HasPrefix(t, "... ")) {
			return nil, fmt.Errorf("configfile: line %d: an end of document", i+1)
		}
		content = true
		if t == "-" || strings.HasPrefix(t, "- ") {
			// A list item: it and what's under it are kept as they are. The list belongs to the
			// key above, which must hold nothing else.
			if len(stack) > 0 && stack[len(stack)-1].indent <= n && stack[len(stack)-1].val != yamlEmpty {
				return nil, fmt.Errorf("configfile: line %d: a list under a value", i+1)
			}
			out[i] = yamlLine{kind: yamlOpaque, indent: n}
			opaqueAbove = n
			continue
		}
		for len(stack) > 0 && stack[len(stack)-1].indent >= n {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 && stack[len(stack)-1].val != yamlEmpty {
			return nil, fmt.Errorf("configfile: line %d: a value continued on another line", i+1)
		}
		key, rest, err := yamlKeyAt(l[n:])
		if err != nil {
			return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
		}
		path := make([]string, 0, len(stack)+1)
		for _, s := range stack {
			path = append(path, s.key)
		}
		path = append(path, key)
		yl := yamlLine{kind: yamlKey, indent: n, path: path}
		at := len(l) - len(rest)
		for at < len(l) && l[at] == ' ' {
			at++
		}
		yl.valStart = at
		v := l[at:]
		switch {
		case v == "" || v[0] == '#':
			yl.val, yl.valEnd = yamlEmpty, at
		case v[0] == '|' || v[0] == '>':
			yl.val, yl.valEnd = yamlFixed, len(l)
			opaqueAbove = n
		case v[0] == '[' || v[0] == '{':
			end, err := yamlFlowEnd(v)
			if err != nil {
				return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
			}
			yl.val, yl.valEnd = yamlFixed, at+end
		case v[0] == '&' || v[0] == '*' || v[0] == '!':
			yl.val, yl.valEnd = yamlFixed, len(l)
		case v[0] == '"' || v[0] == '\'':
			_, end, err := yamlQuoted(v)
			if err != nil {
				return nil, fmt.Errorf("configfile: line %d: %v", i+1, err)
			}
			if after := strings.TrimSpace(v[end:]); after != "" && after[0] != '#' {
				return nil, fmt.Errorf("configfile: line %d: something after a quoted value", i+1)
			}
			yl.val, yl.valEnd, yl.quoted = yamlScalar, at+end, v[0]
		default:
			end := len(v)
			if j := strings.Index(v, " #"); j >= 0 {
				end = j
			}
			end = len(strings.TrimRight(v[:end], " "))
			if strings.Contains(v[:end], ": ") || strings.HasSuffix(v[:end], ":") {
				return nil, fmt.Errorf("configfile: line %d: a map on one line", i+1)
			}
			yl.val, yl.valEnd = yamlScalar, at+end
		}
		out[i] = yl
		stack = append(stack, level{n, key, yl.val})
	}
	return out, nil
}

// yamlKeyAt reads "Key:" at the start of s and returns the key and what follows the colon.
func yamlKeyAt(s string) (string, string, error) {
	if s == "" {
		return "", "", errors.New("not a key")
	}
	if s[0] == '"' || s[0] == '\'' {
		k, end, err := yamlQuoted(s)
		if err != nil {
			return "", "", err
		}
		rest := strings.TrimLeft(s[end:], " ")
		if !strings.HasPrefix(rest, ":") || (len(rest) > 1 && rest[1] != ' ') {
			return "", "", errors.New("a quoted key without a colon")
		}
		return k, rest[1:], nil
	}
	if strings.ContainsRune("?:,[]{}#&*!|>%@`-", rune(s[0])) {
		return "", "", errors.New("a line this doesn't read")
	}
	i := 0
	for {
		j := strings.IndexByte(s[i:], ':')
		if j < 0 {
			return "", "", errors.New("a line that isn't a key")
		}
		i += j
		if i+1 == len(s) || s[i+1] == ' ' {
			break
		}
		i++
	}
	key := strings.TrimRight(s[:i], " ")
	if strings.Contains(key, " #") || key == "" {
		return "", "", errors.New("a line that isn't a key")
	}
	return key, s[i+1:], nil
}

// yamlQuoted reads a quoted scalar at the start of s: its value and its length.
func yamlQuoted(s string) (string, int, error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		if q == '\'' {
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), i + 1, nil
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '"':
			return b.String(), i + 1, nil
		case '\\':
			if i+1 >= len(s) {
				return "", 0, errors.New("a string that doesn't close")
			}
			i++
			switch e := s[i]; e {
			case '0':
				b.WriteByte(0)
			case 'a':
				b.WriteByte('\a')
			case 'b':
				b.WriteByte('\b')
			case 't', '\t':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'v':
				b.WriteByte('\v')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case 'e':
				b.WriteByte(0x1b)
			case ' ', '"', '/', '\\':
				b.WriteByte(e)
			case 'N':
				b.WriteString("\u0085")
			case '_':
				b.WriteString("\u00a0")
			case 'L':
				b.WriteString("\u2028")
			case 'P':
				b.WriteString("\u2029")
			case 'x', 'u', 'U':
				n := map[byte]int{'x': 2, 'u': 4, 'U': 8}[e]
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

// yamlFlowEnd finds the end of a flow collection that must close on its own line.
func yamlFlowEnd(v string) (int, error) {
	depth := 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				if after := strings.TrimSpace(v[i+1:]); after != "" && after[0] != '#' {
					return 0, errors.New("something after a list")
				}
				return i + 1, nil
			}
		case '"', '\'':
			_, n, err := yamlQuoted(v[i:])
			if err != nil {
				return 0, err
			}
			i += n - 1
		}
	}
	return 0, errors.New("a list over more than one line")
}

var (
	yamlInt   = regexp.MustCompile(`^[-+]?[0-9]+$`)
	yamlFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`)
)

// yamlPlainValue types a plain scalar as YAML's core schema does.
func yamlPlainValue(s string) any {
	switch s {
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	case "null", "Null", "NULL", "~", "":
		return nil
	}
	if yamlInt.MatchString(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n
		}
	}
	if yamlFloat.MatchString(s) {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return s
}

// yamlPlainSafe says whether a string can be written without quotes and read back as the same
// string, by YAML 1.1 readers as well as 1.2 ones.
func yamlPlainSafe(s string) bool {
	if s == "" || strings.TrimSpace(s) != s || strings.ContainsRune("-?:,[]{}#&*!|>'\"%@`", rune(s[0])) {
		return false
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") || strings.HasSuffix(s, ":") {
		return false
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u0085' || r == '\u2028' || r == '\u2029' || r == '\ufeff'
	}) >= 0 {
		return false
	}
	if _, isString := yamlPlainValue(s).(string); !isString {
		return false
	}
	switch strings.ToLower(s) {
	case "yes", "no", "on", "off", "y", "n", ".inf", "-.inf", "+.inf", ".nan", "<<", "=":
		return false
	}
	// Numbers in other bases, or with underscores, as YAML 1.1 reads them.
	if t := strings.TrimLeft(s, "+-"); t != "" && t[0] >= '0' && t[0] <= '9' {
		if _, err := strconv.ParseFloat(strings.ReplaceAll(t, "_", ""), 64); err == nil {
			return false
		}
		if _, err := strconv.ParseInt(strings.ReplaceAll(t, "_", ""), 0, 64); err == nil {
			return false
		}
		if strings.Contains(s, ":") { // 1:30 is a number in base 60 to YAML 1.1
			return false
		}
	}
	return true
}

func yamlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20 || r == 0x7f || r == '\u0085' || r == '\u2028' || r == '\u2029' || r == '\ufeff':
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func yamlText(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return floatText(t)
	case string:
		if yamlPlainSafe(t) {
			return t
		}
		return yamlQuote(t)
	}
	return `""`
}

func yamlKeyText(k string) string {
	if yamlPlainSafe(k) && !strings.Contains(k, ":") {
		return k
	}
	return yamlQuote(k)
}

func applyYAML(old []byte, edits []Edit) ([]byte, error) {
	lines, crlf := splitLines(old)
	for _, e := range edits {
		scan, err := scanYAML(lines)
		if err != nil {
			return nil, err
		}
		if lines, err = setYAML(lines, scan, e); err != nil {
			return nil, err
		}
	}
	return joinLines(lines, crlf), nil
}

// yamlBlockEnd is the line after the last one under the key on line i, before the blank lines
// and comments that end it. For i = -1 it's the end of the file.
func yamlBlockEnd(scan []yamlLine, i int) int {
	end := len(scan)
	if i >= 0 {
		for j := i + 1; j < len(scan); j++ {
			if scan[j].kind != yamlOther && scan[j].indent <= scan[i].indent {
				end = j
				break
			}
		}
	}
	for end > i+1 && scan[end-1].kind == yamlOther {
		end--
	}
	return end
}

func setYAML(lines []string, scan []yamlLine, e Edit) ([]string, error) {
	want := e.Path
	name := strings.Join(want, ".")
	parent, parentLine := -1, -1 // the deepest key already there that the path runs through
	for i, y := range scan {
		if y.kind != yamlKey {
			continue
		}
		if samePath(y.path, want) {
			if y.val == yamlFixed {
				return nil, fmt.Errorf("configfile: %s holds a list or block text, which this doesn't rewrite", name)
			}
			if y.val == yamlEmpty {
				if end := yamlBlockEnd(scan, i); end > i+1 {
					return nil, fmt.Errorf("configfile: %s holds a map or a list", name)
				}
			}
			l := lines[i]
			text := yamlText(e.Value)
			if y.val == yamlEmpty {
				// "Key:" with nothing after it: the value goes after the colon, before a comment.
				if c := strings.TrimSpace(l[y.valStart:]); c != "" {
					lines[i] = strings.TrimRight(l[:y.valStart], " ") + " " + text + " " + c
				} else {
					lines[i] = strings.TrimRight(l[:y.valStart], " ") + " " + text
				}
				return lines, nil
			}
			lines[i] = l[:y.valStart] + text + l[y.valEnd:]
			return lines, nil
		}
		if len(y.path) < len(want) && hasPrefix(want, y.path) {
			if y.val != yamlEmpty {
				return nil, fmt.Errorf("configfile: %s runs through a value", name)
			}
			if len(y.path) > parent {
				parent, parentLine = len(y.path), i
			}
		}
	}
	// Add what's missing under the deepest key that's there, indented like its other children.
	at := yamlBlockEnd(scan, parentLine)
	indent := 0
	if parentLine >= 0 {
		indent = scan[parentLine].indent + 2
		for j := parentLine + 1; j < len(scan); j++ {
			if scan[j].kind == yamlKey {
				if scan[j].indent > scan[parentLine].indent {
					indent = scan[j].indent
				}
				break
			}
			if scan[j].kind == yamlOpaque {
				return nil, fmt.Errorf("configfile: %s is under a list", name)
			}
		}
	} else {
		parent = 0
		for _, y := range scan {
			if y.kind == yamlKey {
				indent = y.indent
				break
			}
		}
	}
	step := 2
	var add []string
	for k := parent; k < len(want); k++ {
		line := strings.Repeat(" ", indent+(k-parent)*step) + yamlKeyText(want[k]) + ":"
		if k == len(want)-1 {
			line += " " + yamlText(e.Value)
		}
		add = append(add, line)
	}
	return insertLines(lines, at, add...), nil
}

func entriesYAML(data []byte) ([]Entry, error) {
	lines, _ := splitLines(data)
	scan, err := scanYAML(lines)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for i, y := range scan {
		if y.kind != yamlKey || y.val == yamlEmpty {
			continue
		}
		raw := lines[i][y.valStart:y.valEnd]
		var v any = raw
		switch {
		case y.val == yamlScalar && y.quoted != 0:
			v, _, _ = yamlQuoted(raw)
		case y.val == yamlScalar:
			v = yamlPlainValue(raw)
		}
		if v == nil {
			continue
		}
		out = append(out, Entry{Path: y.path, Value: v})
	}
	return out, nil
}
