// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Cemu writes settings.xml in CemuConfig::Save, element by element, from members of CemuConfig
// declared in CemuConfig.h:
//
//	auto graphic = config.set("Graphic");
//	graphic.set("VSync", vsync);              // ConfigValue<int> vsync{ 0 };
//
// The element path is the chain of set() calls from <content>; the type and default come from
// the member's declaration. Lists (recent files, game paths, the game cache, graphic packs) are
// written in loops and aren't single settings.
var cemuFiles = []string{"src/config/CemuConfig.cpp", "src/config/CemuConfig.h"}

const cemuXML = "settings.xml"

var (
	cemuSave    = regexp.MustCompile(`void\s+CemuConfig::Save\s*\(\s*XMLConfigParser&\s*parser\s*\)`)
	cemuParent  = regexp.MustCompile(`^\s*auto\s+(\w+)\s*=\s*(\w+)\.set\s*\(\s*"([^"]+)"\s*\)\s*;`)
	cemuSet     = regexp.MustCompile(`^\s*(\w+)\.set\s*(?:<[^>]*>)?\s*\(\s*"([^"]+)"\s*,\s*(.+)\)\s*;`)
	cemuWrapper = regexp.MustCompile(`boost::nowide::narrow\(([^)]*)\)|\.c_str\(\)|\.GetValue\(\)|\(\s*(?:sint32|sint64|uint32|uint64|bool)\s*\)|_pathToUtf8\(([^)]*)\)`)
	cemuWrapped = regexp.MustCompile(`^(?:ConfigValue|ConfigValueBounds|ConfigValueAtomic)\s*<\s*([\w:]+)\s*>$`)
)

func extractCemu(files map[string]string) ([]option, error) {
	cpp := preprocess(files["src/config/CemuConfig.cpp"])
	h := preprocess(files["src/config/CemuConfig.h"])
	consts := parseConsts(h, nil)
	enums := parseEnums(h, consts)
	for name, list := range enums {
		for _, e := range list {
			v := strconv.FormatInt(e.Value, 10)
			consts[e.Name], consts[name+"::"+e.Name] = v, v
		}
	}
	loc := cemuSave.FindStringIndex(cpp)
	if loc == nil {
		return nil, fmt.Errorf("no CemuConfig::Save")
	}
	body := braceBody(cpp, loc[1])
	root := structBody(h, "CemuConfig")
	if root == "" {
		return nil, fmt.Errorf("no struct CemuConfig")
	}
	paths := map[string][]string{}
	if m := regexp.MustCompile(`auto\s+(\w+)\s*=\s*parser\.set\s*\(\s*"([^"]+)"\s*\)`).FindStringSubmatch(body); m != nil {
		paths[m[1]] = []string{m[2]}
	}
	var out []option
	for _, line := range strings.Split(body, "\n") {
		if m := cemuParent.FindStringSubmatch(line); m != nil {
			if p, ok := paths[m[2]]; ok {
				paths[m[1]] = append(append([]string(nil), p...), m[3])
			}
			continue
		}
		m := cemuSet.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		parent, ok := paths[m[1]]
		if !ok || m[2] == "Entry" || strings.Contains(strings.Join(parent, "/"), "Entry") {
			continue // a list's entry, written in a loop
		}
		member := strings.TrimSpace(cemuWrapper.ReplaceAllString(m[3], "$1$2"))
		typ, init := cemuMember(root, h, member)
		if typ == "" {
			continue
		}
		path := append(append([]string(nil), parent...), m[2])
		o := option{File: cemuXML, Format: "xml", Path: path, Group: groupLabel(strings.Join(path[1:len(path)-1], "."))}
		if o.Group == "" {
			o.Group = "General"
		}
		switch {
		case typ == "bool":
			o.Type, o.Default = "bool", false
			if init != "" {
				b, ok := boolean(init, consts)
				if !ok {
					continue
				}
				o.Default = b
			}
		case isIntType(typ) || typ == "sint32" || typ == "sint64" || typ == "uint16" || typ == "uint32" || typ == "uint64":
			o.Type, o.Default = "int", 0.0
			if init != "" {
				f, ok := number(init, consts)
				if !ok {
					continue // a default worked out at run time (the system's language)
				}
				o.Default = f
			}
		case typ == "float" || typ == "double":
			o.Type, o.Default = "float", 0.0
			if init != "" {
				f, ok := number(init, consts)
				if !ok {
					continue
				}
				o.Default = f
			}
		case typ == "std::string" || typ == "std::wstring":
			o.Type, o.Default = "string", ""
			if init != "" {
				s, ok := cString(strings.TrimPrefix(init, "L"))
				if !ok {
					continue
				}
				o.Default = s
			}
		default:
			list, ok := enums[lastName(typ)]
			if !ok {
				continue // an array (a device's id), a map
			}
			// Cemu writes an enum as its number.
			o.Type, o.Choices = "enum", enumChoices(list, false)
			f, ok := number(init, enumConsts(consts, lastName(typ), list))
			if !ok {
				continue
			}
			o.Default = strconv.FormatInt(int64(f), 10)
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// cemuMember finds a member's type and initialiser. "overlay.position" is looked for in the
// anonymous struct declared as overlay; a plain name in CemuConfig itself.
func cemuMember(root, h, member string) (typ, init string) {
	scope := root
	if owner, name, ok := strings.Cut(member, "."); ok {
		m := regexp.MustCompile(`\}\s*` + regexp.QuoteMeta(owner) + `\s*(?:\{\s*\})?\s*;`).FindStringIndex(root)
		if m == nil {
			return "", ""
		}
		// Walk back to the brace that opens that struct.
		depth := 0
		start := -1
		for i := m[0]; i >= 0; i-- {
			switch root[i] {
			case '}':
				depth++
			case '{':
				depth--
				if depth == 0 {
					start = i
				}
			}
			if start >= 0 {
				break
			}
		}
		if start < 0 {
			return "", ""
		}
		scope, member = root[start+1:m[0]], name
	}
	if strings.ContainsAny(member, ".()[] ") {
		return "", ""
	}
	// "sint32 tv_volume = 50, pad_volume = 0;" declares several: find the name, then the type
	// at the start of its statement.
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(member) + `\s*(?:=\s*([^,;{]+)|\{([^}]*)\})?\s*[,;]`)
	for _, loc := range re.FindAllStringSubmatchIndex(scope, -1) {
		start := strings.LastIndexAny(scope[:loc[0]], ";{}")
		stmt := strings.TrimSpace(scope[start+1 : loc[0]])
		fields := strings.Fields(stmt)
		if len(fields) == 0 {
			continue
		}
		t := fields[0]
		if len(fields) > 1 && strings.HasPrefix(fields[1], "<") {
			t = strings.Join(fields[:2], "")
		}
		if strings.Contains(t, "(") || t == "return" {
			continue
		}
		if w := cemuWrapped.FindStringSubmatch(t); w != nil {
			t = w[1]
		}
		if loc[2] >= 0 {
			init = strings.TrimSpace(scope[loc[2]:loc[3]])
		} else if loc[4] >= 0 {
			// A bounded value's braces hold the default first, then its bounds.
			init = strings.TrimSpace(splitTop(scope[loc[4]:loc[5]], ',')[0])
		}
		return t, init
	}
	return "", ""
}
