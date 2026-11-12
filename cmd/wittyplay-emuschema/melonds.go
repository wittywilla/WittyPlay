// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// melonDS keeps its settings in melonDS.toml under dotted names ("3D.Renderer",
// "Instance*.Audio.Volume" for each running instance). Config.cpp lists each setting's type in
// the table that moves an old melonDS.ini over (LegacyFile), the ones whose default isn't zero
// in DefaultInts, DefaultBools, DefaultStrings and DefaultDoubles, and the allowed values of
// some in IntRanges. WittyPlay runs one instance, Instance0, with one window, Window0.
var melondsFiles = []string{
	"src/frontend/qt_sdl/Config.cpp",
	"src/frontend/qt_sdl/EmuInstance.h",
	"src/frontend/ScreenLayout.h",
}

const melondsTOML = "melonDS.toml"

var (
	melondsList   = regexp.MustCompile(`(DefaultList<(int|bool|std::string|double)>\s+\w+|RangeList\s+\w+|LegacyEntry\s+\w+\[\])\s*=\s*\{`)
	melondsLegacy = regexp.MustCompile(`^\{\s*"[^"]*"\s*,\s*(\d+)\s*,\s*"([^"]+)"\s*,\s*(true|false)\s*\}$`)
	melondsPair   = regexp.MustCompile(`(?s)^\{\s*"([^"]+)"\s*,\s*(.+)\}$`)
	melondsRange  = regexp.MustCompile(`(?s)^\{\s*"([^"]+)"\s*,\s*\{\s*([^,]+),\s*([^}]+)\}\s*\}$`)
)

func extractMelonDS(files map[string]string) ([]option, error) {
	cpp := preprocess(files["src/frontend/qt_sdl/Config.cpp"])
	headers := preprocess(files["src/frontend/qt_sdl/EmuInstance.h"]) + "\n" + preprocess(files["src/frontend/ScreenLayout.h"])
	consts := parseConsts(headers, nil)
	enums := parseEnums(headers, consts)
	// Enumerator values by name, and which enum each belongs to.
	owner := map[string][]enumerator{}
	for _, list := range enums {
		for _, e := range list {
			consts[e.Name] = strconv.FormatInt(e.Value, 10)
			owner[e.Name] = list
		}
	}
	type entry struct {
		typ      string
		instance bool
		def      string
		min, max string
	}
	byKey := map[string]*entry{}
	var order []string
	numbered := regexp.MustCompile(`\b(Instance|Window)\d+\b`)
	get := func(k string) *entry {
		// The legacy table names Window0 where the others say Window*: one setting.
		k = numbered.ReplaceAllString(k, "$1*")
		if e, ok := byKey[k]; ok {
			return e
		}
		e := &entry{}
		byKey[k] = e
		order = append(order, k)
		return e
	}
	for _, loc := range melondsList.FindAllStringSubmatchIndex(cpp, -1) {
		decl := cpp[loc[2]:loc[3]]
		body := braceBody(cpp, loc[1]-1)
		for _, item := range splitTop(body, ',') {
			item = strings.TrimSpace(item)
			switch {
			case strings.HasPrefix(decl, "LegacyEntry"):
				m := melondsLegacy.FindStringSubmatch(item)
				if m == nil {
					continue
				}
				typ := map[string]string{"0": "int", "1": "bool", "2": "string", "3": "int"}[m[1]]
				if typ == "" {
					continue // a list (recent ROMs)
				}
				path := m[2]
				if m[3] == "true" && !strings.HasPrefix(path, "Instance") {
					path = "Instance*." + path
				}
				e := get(path)
				e.typ = typ
			case strings.HasPrefix(decl, "RangeList"):
				if m := melondsRange.FindStringSubmatch(item); m != nil {
					e := get(m[1])
					e.min, e.max = strings.TrimSpace(m[2]), strings.TrimSpace(m[3])
				}
			default:
				if m := melondsPair.FindStringSubmatch(item); m != nil {
					e := get(m[1])
					e.def = strings.TrimSpace(m[2])
					e.typ = map[string]string{"int": "int", "bool": "bool", "std::string": "string", "double": "float"}[cpp[loc[4]:loc[5]]]
				}
			}
		}
	}
	var out []option
	for _, k := range order {
		e := byKey[k]
		if e.typ == "" {
			continue // only a range, for a setting declared elsewhere
		}
		if strings.HasPrefix(k, "Instance*.Keyboard") || strings.HasPrefix(k, "Instance*.Joystick") || strings.HasPrefix(k, "Keyboard") || strings.HasPrefix(k, "Joystick") {
			continue // input, which WittyPlay drives itself
		}
		name := strings.NewReplacer("Instance*", "Instance0", "Window*", "Window0", "Camera*", "Camera0").Replace(k)
		path := strings.Split(name, ".")
		group := groupLabel(strings.Join(path[:len(path)-1], "."))
		group = strings.TrimPrefix(strings.TrimPrefix(group, "Instance 0, "), "Instance 0")
		o := option{File: melondsTOML, Format: "toml", Path: path, Type: e.typ, Group: group}
		switch e.typ {
		case "bool":
			b := false
			if e.def != "" {
				b, _ = boolean(e.def, consts)
			}
			o.Default = b
		case "int", "float":
			f := 0.0
			if e.def != "" {
				var ok bool
				if f, ok = number(e.def, consts); !ok {
					continue
				}
			}
			o.Default = f
			if e.min != "" {
				lo, ok1 := number(e.min, consts)
				hi, ok2 := number(e.max, consts)
				if ok1 && ok2 {
					o.Min, o.Max = lo, hi
				}
				// A range that ends at an enum's _MAX - 1 is that enum: offer its names.
				if list := melondsEnumOf(e.max, owner); list != nil && e.typ == "int" {
					o.Type = "enum"
					o.Choices = enumChoices(list, false)
					o.Default = strconv.FormatInt(int64(f), 10)
				}
			}
		case "string":
			s := ""
			if e.def != "" {
				var ok bool
				if s, ok = cString(e.def); !ok {
					continue
				}
			}
			o.Default = s
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// melondsEnumOf finds the enum a range's upper end names ("renderer3D_Max-1").
func melondsEnumOf(max string, owner map[string][]enumerator) []enumerator {
	name := strings.TrimSpace(strings.TrimSuffix(strings.ReplaceAll(max, " ", ""), "-1"))
	return owner[name]
}
