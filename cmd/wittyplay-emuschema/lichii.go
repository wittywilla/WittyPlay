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

// Lichii, a yuzu and Eden fork, declares its settings in the struct Values of
// src/common/settings.h, each with its default, its name in the file and its category:
//
//	SwitchableSetting<bool> use_multi_core{linkage, true, "use_multi_core", Category::Core};
//	SwitchableSetting<u16, true> speed_limit{linkage, 100, 0, 9999, "speed_limit", Category::Core, ...};
//
// A ranged setting gives its bounds between the default and the name; after the category come
// how a frontend shows it, then whether it's saved at all. The Qt frontend's qt-config.ini holds
// a category's settings in the section src/frontend_common/config.cpp reads it under
// (BeginGroup(TranslateCategory(Category::Services)) then ReadCategory(Category::Network)), the
// section's name from TranslateCategory in src/common/settings.cpp with a space written %20
// (Config::AdjustKey). An enum is written as its number (Setting::ToString) and is declared with
// the ENUM macro of src/common/settings_enums.h, numbered from 0.
//
// Config::ReadSettingGeneric reads "<name>\default" first, and only when that is false reads
// "<name>": a missing one counts as true. So each setting comes with a second, fixed one that
// writes "<name>\default = false", or the value written would never be read.
//
// A new qt-config.ini is seeded with a few values of its own (Config::Initialize: the scaling
// filter, the GPU accuracy and so on), which stand in for settings.h's defaults here, since that
// is what a fresh install runs with. Categories the Qt frontend reads from its own settings
// (the UI's, the paths, the shortcuts, multiplayer) aren't read here.
var lichiiFiles = []string{"src/common/settings.h", "src/common/settings_enums.h", "src/common/settings.cpp", "src/frontend_common/config.cpp"}

const lichiiINI = "qt-config.ini"

var (
	lichiiDecl      = regexp.MustCompile(`\b(?:Switchable)?Setting\s*<`)
	lichiiEnumMacro = regexp.MustCompile(`(?s)\bENUM\(\s*(\w+)\s*,(.*?)\)`)
	lichiiCase      = regexp.MustCompile(`case\s+Category::(\w+)\s*:|return\s+"([^"]*)"\s*;`)
	lichiiGroup     = regexp.MustCompile(`BeginGroup\(\s*Settings::TranslateCategory\(\s*Settings::Category::(\w+)\s*\)\s*\)|ReadCategory\(\s*Settings::Category::(\w+)\s*\)|EndGroup\(\s*\)`)
	lichiiSeed      = regexp.MustCompile(`seed\(\s*Settings::values\.(\w+)\s*,\s*ToString\((.+)\)\s*\)\s*;`)
	lichiiCast      = regexp.MustCompile(`^(?:u8|u16|u32|u64|s32|s64|int)\((.*)\)$`)
)

func extractLichii(files map[string]string) ([]option, error) {
	h := preprocess(fileOf(files, "common/settings.h"))
	enumsH := preprocess(fileOf(files, "common/settings_enums.h"))
	settingsCPP := preprocess(fileOf(files, "common/settings.cpp"))
	configCPP := preprocess(fileOf(files, "frontend_common/config.cpp"))
	if h == "" || enumsH == "" || settingsCPP == "" || configCPP == "" {
		return nil, fmt.Errorf("a source file is missing")
	}
	enums := parseEnums(enumsH, nil)
	for _, m := range lichiiEnumMacro.FindAllStringSubmatch(enumsH, -1) {
		var list []enumerator
		for i, name := range strings.Split(m[2], ",") {
			name = strings.TrimSpace(name)
			if !regexp.MustCompile(`^\w+$`).MatchString(name) {
				list = nil
				break
			}
			list = append(list, enumerator{name, int64(i)})
		}
		if list != nil {
			enums[m[1]] = list
		}
	}

	// The section each category is written in.
	names := map[string]string{}
	loc := regexp.MustCompile(`TranslateCategory\s*\(\s*Category\s+category\s*\)`).FindStringIndex(settingsCPP)
	if loc == nil {
		return nil, fmt.Errorf("no TranslateCategory")
	}
	var pending []string
	for _, m := range lichiiCase.FindAllStringSubmatch(braceBody(settingsCPP, loc[1]), -1) {
		if m[1] != "" {
			pending = append(pending, m[1])
			continue
		}
		for _, c := range pending {
			names[c] = m[2]
		}
		pending = nil
	}
	sections := map[string]string{}
	group := ""
	for _, m := range lichiiGroup.FindAllStringSubmatch(configCPP, -1) {
		switch {
		case m[1] != "":
			group = names[m[1]]
		case m[2] != "":
			if _, seen := sections[m[2]]; !seen && group != "" {
				sections[m[2]] = strings.ReplaceAll(strings.ReplaceAll(group, "/", `\`), " ", "%20")
			}
		default:
			group = ""
		}
	}
	if len(sections) == 0 {
		return nil, fmt.Errorf("no categories read in config.cpp")
	}
	seeds := map[string]string{}
	for _, m := range lichiiSeed.FindAllStringSubmatch(configCPP, -1) {
		seeds[m[1]] = strings.TrimSpace(m[2])
	}

	body := structBody(h, "Values")
	if body == "" {
		return nil, fmt.Errorf("no struct Values")
	}
	var out []option
	for _, loc := range lichiiDecl.FindAllStringIndex(body, -1) {
		// The template's arguments, to the matching >.
		depth, end := 1, -1
		for i := loc[1]; i < len(body) && end < 0; i++ {
			switch body[i] {
			case '<':
				depth++
			case '>':
				if depth--; depth == 0 {
					end = i
				}
			}
		}
		if end < 0 {
			continue
		}
		targs := splitTop(body[loc[1]:end], ',')
		typ := strings.TrimSpace(targs[0])
		m := regexp.MustCompile(`^\s*(\w+)\s*\{`).FindStringSubmatch(body[end+1:])
		if m == nil {
			continue
		}
		member := m[1]
		args := splitTop(braceBody(body, end+1), ',')
		for i := range args {
			args[i] = strings.TrimSpace(args[i])
		}
		c := -1
		for i, a := range args {
			if strings.HasPrefix(a, "Category::") {
				c = i
				break
			}
		}
		if c < 2 {
			continue
		}
		key, ok := cString(args[c-1])
		if !ok || key == "" {
			continue
		}
		category := strings.TrimPrefix(args[c], "Category::")
		section, ok := sections[category]
		if !ok {
			continue // read by the Qt frontend itself, or not at all
		}
		if len(args) > c+2 && args[c+2] == "false" {
			continue // never saved
		}
		def := args[1]
		if s, ok := seeds[member]; ok {
			def = s
			if mm := lichiiCast.FindStringSubmatch(def); mm != nil {
				def = mm[1]
			}
		}
		o := option{File: lichiiINI, Format: "ini", Path: []string{section, key}, Group: groupLabel(category)}
		if c == 5 { // linkage, default, minimum, maximum, name
			lo, ok1 := number(args[2], nil)
			hi, ok2 := number(args[3], nil)
			if ok1 && ok2 && hi > lo {
				o.Min, o.Max = lo, hi
			}
		}
		switch {
		case typ == "bool":
			b, ok := boolean(def, nil)
			if !ok {
				continue
			}
			o.Type, o.Default = "bool", b
		case isIntType(typ) || typ == "u64" || typ == "s64" || typ == "s8" || typ == "s16":
			f, ok := number(def, nil)
			if !ok {
				continue // a value worked out at run time
			}
			o.Type, o.Default = "int", f
		case typ == "f32" || typ == "float" || typ == "double":
			f, ok := number(def, nil)
			if !ok {
				continue
			}
			o.Type, o.Default = "float", f
		case typ == "std::string":
			o.Type = "string"
			if def == "std::string()" || def == "std::string{}" || def == "{}" {
				o.Default = ""
			} else if s, ok := cString(def); ok {
				o.Default = s
			} else {
				continue
			}
		default:
			list, ok := enums[lastName(typ)]
			if !ok {
				continue // an array, an optional
			}
			f, ok := number(def, enumConsts(nil, lastName(typ), list))
			if !ok {
				continue
			}
			o.Type, o.Choices, o.Default = "enum", enumChoices(list, false), strconv.FormatInt(int64(f), 10)
			o.Min, o.Max = 0, 0
		}
		// The debugger listens on a port: the sandbox decides what the emulator may reach.
		if strings.Contains(key, "gdb") {
			o.Fixed = true
		}
		// The value is read only with "<name>\default" false beside it.
		companion := option{File: lichiiINI, Format: "ini", Path: []string{section, key + `\default`}, Group: o.Group,
			Type: "bool", Default: false, Fixed: true, Label: emulators.SettingLabel(key) + ": use the default"}
		out = append(out, o, companion)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}
