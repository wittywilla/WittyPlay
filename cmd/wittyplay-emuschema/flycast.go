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

// Flycast declares each setting as an Option in core/cfg/option.cpp, with its key, default and
// section of emu.cfg (an INI file; the section is "config" unless named):
//
//	Option<bool> Widescreen("rend.WideScreen");
//	Option<int> AudioBufferSize("aica.BufferSize", 2822);
//	Option<bool> NetworkEnable("Enable", false, "network");
//
// The [input] section is the controllers, which WittyPlay drives itself, and isn't read.
var flycastFiles = []string{"core/cfg/option.cpp", "core/cfg/option.h", "core/types.h"}

const flycastCFG = "emu.cfg"

var (
	flycastOption = regexp.MustCompile(`(?s)\b(Option<([\w:]+)(?:\s*,\s*(?:true|false))?>|OptionString)\s*(\w*)\s*\(\s*("[^"]*")\s*(?:,\s*((?:[^(),]|\([^()]*\))+?))?\s*(?:,\s*("[^"]*"))?\s*\)`)
	flycastMB     = regexp.MustCompile(`(\d+)_MB\b`)
)

// Where a key with no section of its own is filed on the page, by the part before its dot.
var flycastGroups = map[string]string{"rend": "Rendering", "pvr": "Rendering", "ta": "Rendering", "aica": "Audio", "Dreamcast": "Dreamcast", "Dynarec": "Dynarec", "Debug": "Debug", "Profiler": "Profiler"}

func extractFlycast(files map[string]string) ([]option, error) {
	cpp := preprocess(files["core/cfg/option.cpp"])
	h := preprocess(files["core/cfg/option.h"])
	types := preprocess(files["core/types.h"])
	consts := parseConsts(types, nil)
	enums := parseEnums(types, consts)
	var out []option
	// The header declares two options as classes of their own (the renderer, the volume).
	for _, src := range []string{cpp, h} {
		for _, m := range flycastOption.FindAllStringSubmatch(src, -1) {
			typ, key, def, section := m[2], m[4], strings.TrimSpace(m[5]), m[6]
			if m[1] == "OptionString" {
				typ = "std::string"
			}
			key, _ = cString(key)
			sec := "config"
			if section != "" {
				sec, _ = cString(section)
			}
			if key == "" || sec == "input" {
				continue
			}
			o := option{File: flycastCFG, Format: "ini", Path: []string{sec, key}}
			prefix, name, dotted := strings.Cut(key, ".")
			if !dotted {
				name = key
			}
			o.Label = emulators.SettingLabel(name)
			switch {
			case sec != "config":
				o.Group = groupLabel(sec)
			case dotted && flycastGroups[prefix] != "":
				o.Group = flycastGroups[prefix]
			case dotted:
				o.Group = groupLabel(prefix)
			default:
				o.Group = "General"
			}
			def = flycastMB.ReplaceAllString(def, "($1*1048576)")
			switch typ {
			case "bool":
				o.Type, o.Default = "bool", false
				if def != "" {
					b, ok := boolean(def, consts)
					if !ok {
						continue
					}
					o.Default = b
				}
			case "int", "int64_t", "u32", "s32":
				o.Type, o.Default = "int", 0.0
				if def != "" {
					f, ok := number(def, consts)
					if !ok {
						continue
					}
					o.Default = f
				}
			case "float":
				o.Type, o.Default = "float", 0.0
				if def != "" {
					f, ok := number(def, consts)
					if !ok {
						continue
					}
					o.Default = f
				}
			case "std::string":
				o.Type, o.Default = "string", ""
				if def != "" {
					s, ok := cString(def)
					if !ok {
						continue
					}
					o.Default = s
				}
			default:
				list, ok := enums[lastName(typ)]
				if !ok {
					continue // a list of paths, a controller type
				}
				o.Type, o.Choices = "enum", enumChoices(list, false)
				f, ok := number(def, enumConsts(consts, lastName(typ), list))
				if !ok {
					continue
				}
				o.Default = strconv.FormatInt(int64(f), 10)
			}
			out = append(out, o)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}
