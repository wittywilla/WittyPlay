// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"errors"
	"strings"
)

// INI, as PCSX2, Dolphin, PPSSPP and Flycast write it: "[Section]" lines, then "Key = value"
// lines, with ";" or "#" starting a comment. Values are text.

// iniSection says whether a line is a section header, and which.
func iniSection(l string) (string, bool) {
	t := strings.TrimSpace(l)
	if len(t) >= 2 && t[0] == '[' && t[len(t)-1] == ']' {
		return t[1 : len(t)-1], true
	}
	return "", false
}

// iniKey is the key a line sets, if it sets one.
func iniKey(l string) (key, value string, ok bool) {
	t := strings.TrimSpace(l)
	if t == "" || t[0] == ';' || t[0] == '#' || t[0] == '[' {
		return "", "", false
	}
	k, v, ok := strings.Cut(l, "=")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(k), strings.TrimSpace(v), true
}

func checkINIEdit(e Edit) error {
	sec, key := e.Path[0], e.Path[1]
	if strings.ContainsAny(sec, "[]") || strings.TrimSpace(sec) != sec {
		return errors.New("configfile: an INI section can't hold brackets or start or end with a space")
	}
	if strings.Contains(key, "=") || strings.TrimSpace(key) != key || strings.ContainsAny(key[:1], ";#[") {
		return errors.New("configfile: an INI key can't hold = or look like a comment or a section")
	}
	return nil
}

func applyINI(old []byte, edits []Edit) ([]byte, error) {
	for _, e := range edits {
		if err := checkINIEdit(e); err != nil {
			return nil, err
		}
	}
	lines, crlf := splitLines(old)
	for _, e := range edits {
		lines = setINI(lines, e.Path[0], e.Path[1], plainText(e.Value))
	}
	return joinLines(lines, crlf), nil
}

// setINI sets one key in the first section of that name, adding the section or the key when
// it isn't there. A new key goes after the section's last key, before the blank lines that end
// it, so the file still reads the way the emulator laid it out.
func setINI(lines []string, section, key, value string) []string {
	start, end := -1, len(lines) // the section's lines are (start, end)
	for i, l := range lines {
		if name, ok := iniSection(l); ok {
			if start >= 0 {
				end = i
				break
			}
			if name == section {
				start = i
			}
		}
	}
	line := key + " = " + value
	if start < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		return append(lines, "["+section+"]", line)
	}
	for i := start + 1; i < end; i++ {
		if k, _, ok := iniKey(lines[i]); ok && k == key {
			lines[i] = line
			return lines
		}
	}
	at := end
	for at > start+1 && strings.TrimSpace(lines[at-1]) == "" {
		at--
	}
	return insertLines(lines, at, line)
}

func entriesINI(data []byte) []Entry {
	lines, _ := splitLines(data)
	var out []Entry
	section, seen := "", false
	for _, l := range lines {
		if name, ok := iniSection(l); ok {
			section, seen = name, true
			continue
		}
		if !seen {
			continue // a key before any section belongs to none this reads
		}
		if k, v, ok := iniKey(l); ok && k != "" {
			out = append(out, Entry{Path: []string{section, k}, Value: v})
		}
	}
	return out
}
