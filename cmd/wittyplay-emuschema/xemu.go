// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/emulators/configfile"
)

// xemu describes its settings in config_spec.yml, from which its own build generates the code
// that reads xemu.toml: each setting is a key whose value is its type ("skip_boot_anim: bool"),
// or a map with a type, a default and, for an enum, its values. The path to a setting in the
// spec is its path in xemu.toml. The input section is the controllers, which WittyPlay drives
// itself, and lists (type: array) aren't settings one value can hold.
var xemuFiles = []string{"config_spec.yml"}

const xemuTOML = "xemu.toml"

var xemuTypes = map[string]string{"bool": "bool", "integer": "int", "number": "float", "float": "float", "string": "string", "enum": "enum"}

func extractXemu(files map[string]string) ([]option, error) {
	entries, err := configfile.Entries("yaml", []byte(files["config_spec.yml"]))
	if err != nil {
		return nil, fmt.Errorf("config_spec.yml: %w", err)
	}
	type spec struct {
		path   []string
		typ    string
		def    any
		values []string
	}
	var order []string
	specs := map[string]*spec{}
	var arrays [][]string
	get := func(p []string) *spec {
		k := strings.Join(p, "\x00")
		if s, ok := specs[k]; ok {
			return s
		}
		s := &spec{path: p}
		specs[k] = s
		order = append(order, k)
		return s
	}
	for _, e := range entries {
		last := e.Path[len(e.Path)-1]
		v, isText := e.Value.(string)
		switch {
		case last == "type" && len(e.Path) > 1:
			if v == "array" {
				arrays = append(arrays, e.Path[:len(e.Path)-1])
				continue
			}
			get(e.Path[:len(e.Path)-1]).typ = v
		case last == "default" && len(e.Path) > 1:
			get(e.Path[:len(e.Path)-1]).def = e.Value
		case last == "values" && len(e.Path) > 1:
			get(e.Path[:len(e.Path)-1]).values = flowList(v)
		case isText && xemuTypes[v] != "":
			get(e.Path).typ = v
		}
	}
	var out []option
	for _, k := range order {
		s := specs[k]
		if s.path[0] == "input" || slices.ContainsFunc(arrays, func(a []string) bool { return hasPathPrefix(s.path, a) }) {
			continue
		}
		typ := xemuTypes[s.typ]
		if typ == "" {
			continue
		}
		o := option{File: xemuTOML, Format: "toml", Path: s.path, Type: typ, Group: groupLabel(strings.Join(s.path[:len(s.path)-1], "."))}
		switch typ {
		case "bool":
			b, _ := s.def.(bool)
			o.Default = b
		case "int", "float":
			switch n := s.def.(type) {
			case int64:
				o.Default = float64(n)
			case float64:
				o.Default = n
			default:
				o.Default = 0.0
			}
		case "string":
			str, _ := s.def.(string)
			o.Default = str
		case "enum":
			if len(s.values) == 0 {
				continue
			}
			for _, v := range s.values {
				o.Choices = append(o.Choices, emulators.Choice{Value: v, Label: emulators.SettingLabel(v)})
			}
			// xemu keeps an enum in its file as the value's name.
			o.FileType = "string"
			o.Default = s.values[0]
			if d := fmt.Sprint(s.def); s.def != nil && slices.Contains(s.values, d) {
				o.Default = d
			}
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// flowList reads a one-line YAML list: [a, "b", 'c'].
func flowList(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	var out []string
	for _, item := range splitTop(s[1:len(s)-1], ',') {
		item = strings.TrimSpace(item)
		if len(item) >= 2 && (item[0] == '"' || item[0] == '\'') && item[len(item)-1] == item[0] {
			item = item[1 : len(item)-1]
		}
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func hasPathPrefix(p, prefix []string) bool {
	return len(p) >= len(prefix) && slices.Equal(p[:len(prefix)], prefix)
}
