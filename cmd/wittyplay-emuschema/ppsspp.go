// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PPSSPP lists its settings in tables of ConfigSetting, one table per section of ppsspp.ini:
//
//	static const ConfigSetting graphicsSettings[] = {
//		ConfigSetting("FrameSkip", SETTING(g_Config, iFrameSkip), 0, CfgFlag::PER_GAME),
//
// and g_sectionMeta says which table is which section. A member's first letter says its type
// (b, i, f, s, u). The touch-screen and gesture tables are input and aren't read; nor are
// settings marked DONT_SAVE, which never reach the file.
var ppssppFiles = []string{"Core/Config.cpp", "Core/Config.h", "Core/ConfigValues.h", "Core/HLE/sceUtility.h"}

const ppssppINI = "PSP/SYSTEM/ppsspp.ini"

var (
	ppssppTable   = regexp.MustCompile(`static\s+const\s+ConfigSetting\s+(\w+)\s*\[\]\s*=\s*\{`)
	ppssppMeta    = regexp.MustCompile(`\{\s*&g_Config(?:\.(\w+)(?:\[\d+\])?)?\s*,\s*(\w+)\s*,\s*ARRAY_SIZE\(\w+\)\s*,\s*"([^"]+)"`)
	ppssppSetting = regexp.MustCompile(`\bConfigSetting\s*\(`)
	ppssppFunc    = regexp.MustCompile(`(?s)static\s+(?:int|bool|float|std::string|uint32_t|u32)\s+(\w+)\s*\(\s*\)\s*\{`)
	ppssppReturn  = regexp.MustCompile(`return\s+([^;]+);`)
)

// Defaults PPSSPP works out when it runs, as it does on the machines WittyPlay gives it: more
// than one core (DefaultSasThread), and no VR headset (IsVREnabled).
var ppssppRuntime = map[string]string{"DefaultSasThread": "true", "IsVREnabled()": "false"}

// Tables of input, which WittyPlay drives itself.
var ppssppSkipTables = map[string]bool{"touchControlSettings": true, "gestureControlSettings": true}

func extractPPSSPP(files map[string]string) ([]option, error) {
	cpp := preprocess(files["Core/Config.cpp"])
	h := preprocess(files["Core/Config.h"])
	values := preprocess(files["Core/ConfigValues.h"])
	consts := parseConsts(values, nil)
	parseConsts(preprocess(files["Core/HLE/sceUtility.h"]), consts)
	for k, v := range ppssppRuntime {
		consts[k] = v
	}
	parseConsts(h, consts)
	for name, list := range parseEnums(values+"\n"+h, consts) {
		for _, e := range list {
			v := strconv.FormatInt(e.Value, 10)
			consts[e.Name], consts[name+"::"+e.Name] = v, v
		}
	}
	// The functions some defaults come from, as a Linux build compiles them: their first return.
	funcs := map[string]string{}
	for _, loc := range ppssppFunc.FindAllStringSubmatchIndex(cpp, -1) {
		body := braceBody(cpp, loc[1]-1)
		if m := ppssppReturn.FindStringSubmatch(body); m != nil && !strings.Contains(body, "?") {
			funcs[cpp[loc[2]:loc[3]]] = strings.TrimSpace(m[1])
		}
	}
	tables := map[string]string{}
	for _, loc := range ppssppTable.FindAllStringSubmatchIndex(cpp, -1) {
		tables[cpp[loc[2]:loc[3]]] = braceBody(cpp, loc[1]-1)
	}
	var out []option
	for _, m := range ppssppMeta.FindAllStringSubmatch(cpp, -1) {
		block, table, section := m[1], m[2], m[3]
		if ppssppSkipTables[table] {
			continue
		}
		body, ok := tables[table]
		if !ok {
			return nil, fmt.Errorf("no table %s", table)
		}
		decl := ""
		if block != "" {
			decl = structBody(h, ppssppBlockType(h, block))
		}
		out = append(out, ppssppOptions(body, section, decl, consts, funcs)...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// ppssppBlockType is the struct type of a g_Config member such as displayLayoutLandscape.
func ppssppBlockType(h, member string) string {
	m := regexp.MustCompile(`(\w+)\s+` + regexp.QuoteMeta(member) + `\b`).FindStringSubmatch(h)
	if m == nil {
		return ""
	}
	return m[1]
}

func ppssppOptions(body, section, decl string, consts, funcs map[string]string) []option {
	var out []option
	for _, loc := range ppssppSetting.FindAllStringIndex(body, -1) {
		args := splitTop(parenBody(body, loc[1]-1), ',')
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}
		if len(args) < 3 {
			continue
		}
		key, ok := cString(args[0])
		if !ok || !strings.HasPrefix(args[1], "SETTING(") {
			continue // several keys for one value (a touch button's place), or an array
		}
		flags := args[len(args)-1]
		if strings.Contains(flags, "DONT_SAVE") || len(args) > 4 {
			continue // never saved, or saved through a translator of its own
		}
		member := strings.TrimSpace(strings.TrimSuffix(args[1][strings.LastIndex(args[1], ",")+1:], ")"))
		def := ""
		if len(args) == 4 {
			def = args[2]
		} else if decl != "" {
			_, def = pcsx2Member(decl, member)
		}
		if strings.HasPrefix(def, "&") {
			name := strings.TrimPrefix(def, "&")
			def = funcs[name]
			if v, ok := ppssppRuntime[name]; ok {
				def = v
			}
		}
		if v, ok := ppssppRuntime[def]; ok {
			def = v
		}
		o := option{File: ppssppINI, Format: "ini", Path: []string{section, key}, Group: groupLabel(section)}
		switch member[0] {
		case 'b':
			o.Type = "bool"
			if b, ok := boolean(def, consts); ok {
				o.Default = b
			}
			// PPSSPP writes "True" and "False".
			o.Values = map[string]string{"true": "True", "false": "False"}
		case 'i', 'u':
			o.Type = "int"
			if f, ok := number(def, consts); ok {
				o.Default = f
			}
		case 'f':
			o.Type = "float"
			if f, ok := number(def, consts); ok {
				o.Default = f
			}
		case 's':
			o.Type = "string"
			if s, ok := cString(def); ok {
				o.Default = s
			}
		default:
			continue // a list (v), kept in its own way
		}
		out = append(out, o)
	}
	return out
}

// parenBody is the text inside the parenthesis that opens at from.
func parenBody(s string, from int) string {
	depth := 0
	inStr := false
	for i := from; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' {
				i++
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[from+1 : i]
			}
		}
	}
	return ""
}
