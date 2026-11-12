// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/emulators"
)

// PCSX2 reads and writes its settings in LoadSave functions, through macros that name the
// member and, sometimes, the key:
//
//	SettingsWrapSection("EmuCore/GS");
//	SettingsWrapBitBoolEx(PCRTCAntiBlur, "pcrtc_antiblur");
//	SettingsWrapIntEnumEx(Renderer, "Renderer");
//
// A member's type comes from its declaration in Config.h, its default from the struct's
// constructor (or the declaration's initialiser). With -datapath <dir>, the file is
// <dir>/PCSX2/inis/PCSX2.ini; the settings folder is that dir.
var pcsx2Files = []string{"pcsx2/Pcsx2Config.cpp", "pcsx2/Config.h"}

const pcsx2INI = "PCSX2/inis/PCSX2.ini"

var (
	pcsx2LoadSave = regexp.MustCompile(`(?m)^void\s+((?:\w+::)*)(LoadSave|LoadSaveCore)\s*\(\s*SettingsWrapper&\s*wrap\s*\)`)
	pcsx2Macro    = regexp.MustCompile(`SettingsWrap(Section|Entry|EntryEx|Bitfield|BitBool|BitfieldEx|BitBoolEx|EnumEx|ParsedEnum|IntEnumEx)\s*\(([^;]*)\)\s*;`)
	pcsx2Literal  = regexp.MustCompile(`wrap\.(Entry|EntryBitBool|EntryBitfield)\s*\(\s*(CURRENT_SETTINGS_SECTION|"[^"]*")\s*,\s*("[^"]*")\s*,\s*([\w.\[\]]+)`)
)

func extractPCSX2(files map[string]string) ([]option, error) {
	cpp := preprocess(files["pcsx2/Pcsx2Config.cpp"])
	h := preprocess(files["pcsx2/Config.h"])
	consts := parseConsts(h, nil)
	parseConsts(cpp, consts)
	enums := parseEnums(h, consts)
	var out []option
	for _, loc := range pcsx2LoadSave.FindAllStringSubmatchIndex(cpp, -1) {
		scope := strings.TrimSuffix(cpp[loc[2]:loc[3]], "::")
		structName := scope[strings.LastIndex(scope, ":")+1:]
		if cpp[loc[4]:loc[5]] == "LoadSaveCore" {
			structName = "Pcsx2Config"
		}
		body := braceBody(cpp, loc[1])
		decl := structBody(h, structName)
		ctor := pcsx2Constructor(cpp, structName)
		out = append(out, pcsx2Options(body, decl, ctor, cpp, consts, enums)...)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// pcsx2Options reads one LoadSave function's body.
func pcsx2Options(body, decl, ctor, cpp string, consts map[string]string, enums map[string][]enumerator) []option {
	var out []option
	section := ""
	// Calls in source order: the macros, and wrap.Entry calls with a key written out.
	type call struct {
		at                 int
		macro, args        string
		literal            bool
		litSection, litKey string
		member             string
		bitBool            bool
	}
	var calls []call
	for _, m := range pcsx2Macro.FindAllStringSubmatchIndex(body, -1) {
		calls = append(calls, call{at: m[0], macro: body[m[2]:m[3]], args: body[m[4]:m[5]]})
	}
	for _, m := range pcsx2Literal.FindAllStringSubmatchIndex(body, -1) {
		sec := body[m[4]:m[5]]
		key, _ := cString(body[m[6]:m[7]])
		c := call{at: m[0], literal: true, litKey: key, member: body[m[8]:m[9]], bitBool: body[m[2]:m[3]] == "EntryBitBool"}
		if sec != "CURRENT_SETTINGS_SECTION" {
			c.litSection, _ = cString(sec)
		}
		calls = append(calls, c)
	}
	sortCalls := func() {
		for i := 1; i < len(calls); i++ {
			for j := i; j > 0 && calls[j].at < calls[j-1].at; j-- {
				calls[j], calls[j-1] = calls[j-1], calls[j]
			}
		}
	}
	sortCalls()
	for _, c := range calls {
		var member, key, kind string
		sec := section
		if c.literal {
			member, key, kind = c.member, c.litKey, "Entry"
			if c.bitBool {
				kind = "BitBool"
			}
			if c.litSection != "" {
				sec = c.litSection
			}
		} else {
			args := splitTop(c.args, ',')
			for i := range args {
				args[i] = strings.TrimSpace(args[i])
			}
			if c.macro == "Section" {
				section, _ = cString(args[0]) // a section made at run time is "": skipped
				continue
			}
			member, key, kind = args[0], args[0], c.macro
			if len(args) > 1 {
				if k, ok := cString(args[1]); ok {
					key = k
				} else {
					continue
				}
			}
			kind = strings.TrimSuffix(kind, "Ex")
			if c.macro == "EnumEx" {
				kind = "EnumEx:" + args[2]
			}
		}
		if sec == "" || key == "" {
			continue
		}
		o := option{File: pcsx2INI, Format: "ini", Path: []string{sec, key}, Group: groupLabel(sec)}
		typ, init := pcsx2Member(decl, member)
		def, hasDef := pcsx2Assigned(ctor, member)
		if !hasDef {
			def, hasDef = init, init != ""
		}
		switch {
		case kind == "BitBool":
			o.Type = "bool"
			if !hasDef {
				def = "false" // the constructor clears the bitset first
			}
			o.Default, _ = boolean(def, consts)
		case kind == "Bitfield" || kind == "IntEnum":
			if list, ok := enums[lastName(typ)]; ok && kind == "IntEnum" {
				o.Type, o.Choices = "enum", enumChoices(list, false)
				if f, ok := number(def, enumConsts(consts, lastName(typ), list)); ok {
					o.Default = strconv.FormatInt(int64(f), 10)
				}
				break
			}
			o.Type = "int"
			if !hasDef {
				def = "0"
			}
			if f, ok := number(def, consts); ok {
				o.Default = f
			}
		case strings.HasPrefix(kind, "EnumEx:"):
			names := pcsx2Names(cpp, strings.TrimPrefix(kind, "EnumEx:"))
			list := enums[lastName(typ)]
			if len(names) == 0 || len(list) == 0 {
				continue
			}
			o.Type = "enum"
			for _, n := range names {
				o.Choices = append(o.Choices, emulators.Choice{Value: n, Label: n})
			}
			if f, ok := number(def, enumConsts(consts, lastName(typ), list)); ok && int(f) >= 0 && int(f) < len(names) {
				o.Default = names[int(f)]
			}
		case kind == "Entry":
			switch {
			case typ == "bool":
				o.Type = "bool"
				o.Default, _ = boolean(def, consts)
			case typ == "float" || typ == "double":
				o.Type = "float"
				if f, ok := number(def, consts); ok {
					o.Default = f
				}
			case typ == "std::string":
				o.Type = "string"
				o.Default = ""
				if hasDef {
					s, ok := cString(def)
					if !ok {
						continue // worked out at run time (a file name from the BIOS, say)
					}
					o.Default = s
				}
			case isIntType(typ):
				o.Type = "int"
				if !hasDef {
					def = "0"
				}
				if f, ok := number(def, consts); ok {
					o.Default = f
				}
			default:
				continue // a member this can't place: a local, a struct of its own
			}
		default:
			continue // ParsedEnum: names that come from functions, not a list
		}
		out = append(out, o)
	}
	return out
}

func isIntType(t string) bool {
	switch t {
	case "int", "u8", "u16", "u32", "u64", "s8", "s16", "s32", "s64", "uint", "unsigned", "size_t", "int32_t", "uint32_t", "uint8_t":
		return true
	}
	return false
}

func lastName(t string) string { return t[strings.LastIndex(t, ":")+1:] }

// enumConsts adds an enum's names to the constants, as Name and Type::Name.
func enumConsts(consts map[string]string, typ string, list []enumerator) map[string]string {
	local := make(map[string]string, len(consts)+2*len(list))
	for k, v := range consts {
		local[k] = v
	}
	for _, e := range list {
		v := strconv.FormatInt(e.Value, 10)
		local[e.Name], local[typ+"::"+e.Name] = v, v
	}
	return local
}

// braceBody is the text inside the braces that open at or after from.
func braceBody(s string, from int) string {
	start := strings.IndexByte(s[from:], '{')
	if start < 0 {
		return ""
	}
	start += from
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start+1 : i]
			}
		}
	}
	return s[start+1:]
}

// structBody is a struct's declaration, found by name.
func structBody(h, name string) string {
	re := regexp.MustCompile(`\b(?:struct|class)\s+` + regexp.QuoteMeta(name) + `\s*(?:final\s*)?(?::[^{;]*)?\{`)
	loc := re.FindStringIndex(h)
	if loc == nil {
		return ""
	}
	return braceBody(h, loc[1]-1)
}

func pcsx2Constructor(cpp, structName string) string {
	re := regexp.MustCompile(`(?m)^(?:\w+::)*` + regexp.QuoteMeta(structName) + `::` + regexp.QuoteMeta(structName) + `\s*\(\s*\)`)
	loc := re.FindStringIndex(cpp)
	if loc == nil {
		return ""
	}
	return braceBody(cpp, loc[1])
}

// pcsx2Member finds a member's type and initialiser in a struct's declaration. A bitfield's
// type is the one its group was declared with.
func pcsx2Member(decl, member string) (typ, init string) {
	name := regexp.QuoteMeta(member)
	if m := regexp.MustCompile(`(?m)^\s*(?:static\s+)?([\w:<>]+)\s+` + name + `\s*(?:=\s*([^;]+)|\{([^};]*)\})?\s*;`).FindStringSubmatch(decl); m != nil {
		init = strings.TrimSpace(m[2] + m[3])
		return m[1], init
	}
	// In a bitfield group: "bool\n A : 1,\n B : 1;" or "u32 A : 4;".
	if loc := regexp.MustCompile(`\b` + name + `\s*:\s*\d+\s*[,;]`).FindStringIndex(decl); loc != nil {
		before := decl[:loc[0]]
		if m := regexp.MustCompile(`(?s).*\b(bool|u8|u16|u32|u64|s8|s16|s32|s64|int|uint)\b`).FindStringSubmatch(before); m != nil {
			return m[1], ""
		}
	}
	return "", ""
}

// pcsx2Assigned finds "member = value;" in a constructor.
func pcsx2Assigned(ctor, member string) (string, bool) {
	m := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(member) + `\s*=\s*([^;]+);`).FindStringSubmatch(ctor)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// pcsx2Names reads a names array: const char* X::AspectRatioNames[...] = {"Stretch", ..., nullptr};
func pcsx2Names(cpp, array string) []string {
	re := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(array) + `\s*\[[^\]]*\]\s*=\s*\{(.*?)\}\s*;`)
	m := re.FindStringSubmatch(cpp)
	if m == nil {
		return nil
	}
	var names []string
	for _, item := range splitTop(m[1], ',') {
		item = strings.TrimSpace(item)
		if item == "nullptr" || item == "NULL" || item == "" {
			continue
		}
		s, ok := cString(item)
		if !ok {
			return nil
		}
		names = append(names, s)
	}
	return names
}
