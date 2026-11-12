// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/wittywilla/wittyplay/emulators"
)

// RPCS3 declares its settings as a tree of cfg::node structs in system_config.h; each node's
// name is a map in config.yml and each entry a key in it:
//
//	struct node_core : cfg::node
//	{
//		node_core(cfg::node* _this) : cfg::node(_this, "Core") {}
//		cfg::_int<1, 8> ppu_threads{ this, "PPU Threads", 2 };
//		cfg::_enum<ppu_decoder_type> ppu_decoder{ this, "PPU Decoder", ppu_decoder_type::llvm };
//
// An enum is written in the file as the text its formatter in system_config_types.cpp gives
// ("Recompiler (LLVM)"). Lists, maps and the log levels aren't single values and aren't read.
// The "Input/Output" node is the emulated controllers and devices; WittyPlay drives input itself,
// but the node also holds handlers a person may choose, so it is read and kept for the
// administrator.
var rpcs3Files = []string{"rpcs3/Emu/system_config.h", "rpcs3/Emu/system_config_types.h", "rpcs3/Emu/system_config_types.cpp"}

const rpcs3YAML = "config.yml"

var (
	rpcs3Struct = regexp.MustCompile(`^\s*struct\s+(\w+)\s*:\s*(?:public\s+)?cfg::node\b`)
	rpcs3Name   = regexp.MustCompile(`cfg::node\s*\(\s*_this\s*,\s*"([^"]+)"\s*\)`)
	rpcs3Alias  = regexp.MustCompile(`struct\s+(\w+)\s*:\s*public\s+cfg::_enum<(\w+)>`)
	rpcs3Entry  = regexp.MustCompile(`^\s*(cfg::\w+|\w+)\s*(?:<([^>]*)>)?\s+\w+\s*\{\s*this\s*,\s*"([^"]+)"\s*(?:,(.*))?\}\s*;`)
	rpcs3Case   = regexp.MustCompile(`case\s+(?:\w+::)*(\w+)\s*:\s*return\s+"([^"]*)"`)
)

func extractRPCS3(files map[string]string) ([]option, error) {
	h := preprocess(files["rpcs3/Emu/system_config.h"])
	typesH := preprocess(files["rpcs3/Emu/system_config_types.h"])
	typesCPP := preprocess(files["rpcs3/Emu/system_config_types.cpp"])
	consts := parseConsts(typesH, nil)
	enums := parseEnums(typesH, consts)
	aliases := map[string]string{}
	for _, m := range rpcs3Alias.FindAllStringSubmatch(h, -1) {
		aliases[m[1]] = m[2]
	}
	type level struct {
		depth int
		name  string
	}
	var stack []level
	depth := 0
	var out []option
	for _, line := range strings.Split(h, "\n") {
		if rpcs3Struct.MatchString(line) {
			stack = append(stack, level{depth: depth + 1})
		}
		if m := rpcs3Name.FindStringSubmatch(line); m != nil && len(stack) > 0 {
			stack[len(stack)-1].name = m[1]
		}
		if m := rpcs3Entry.FindStringSubmatch(line); m != nil && len(stack) > 0 {
			var path []string
			for _, l := range stack {
				if l.name != "" { // the root, cfg_root, is the file itself
					path = append(path, l.name)
				}
			}
			if o, ok := rpcs3Option(m[1], m[2], m[3], m[4], append(path, m[3]), aliases, enums, typesCPP, consts); ok {
				out = append(out, o)
			}
		}
		before := depth
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		// A struct's line comes before its brace: only a closing brace ends one.
		for depth < before && len(stack) > 0 && depth < stack[len(stack)-1].depth {
			stack = stack[:len(stack)-1]
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

func rpcs3Option(kind, targs, key, rest string, path []string, aliases map[string]string, enums map[string][]enumerator, typesCPP string, consts map[string]string) (option, bool) {
	o := option{File: rpcs3YAML, Format: "yaml", Path: path, Group: strings.Join(path[:len(path)-1], ", ")}
	args := splitTop(rest, ',')
	def := ""
	if len(args) > 0 {
		def = strings.TrimSpace(args[0])
	}
	if alias, ok := aliases[kind]; ok {
		kind, targs = "cfg::_enum", alias
	}
	switch kind {
	case "cfg::_bool":
		o.Type, o.Default = "bool", false
		if def != "" {
			b, ok := boolean(def, consts)
			if !ok {
				return o, false
			}
			o.Default = b
		}
	case "cfg::_int", "cfg::uint", "cfg::uint64", "cfg::_float":
		o.Type = "int"
		if kind == "cfg::_float" {
			o.Type = "float"
		}
		lo, hi := 0.0, 0.0
		if targs != "" {
			bounds := splitTop(targs, ',')
			if len(bounds) == 2 {
				var ok1, ok2 bool
				lo, ok1 = number(bounds[0], consts)
				hi, ok2 = number(bounds[1], consts)
				if ok1 && ok2 && hi-lo < 1e15 {
					o.Min, o.Max = lo, hi
				}
			}
		}
		// Without a default, RPCS3 starts at 0 held to the range.
		f := math.Min(hi, math.Max(lo, 0))
		if def != "" {
			var ok bool
			if f, ok = number(def, consts); !ok {
				return o, false
			}
		}
		o.Default = f
	case "cfg::string":
		o.Type, o.Default = "string", ""
		if def != "" {
			s, ok := cString(def)
			if !ok {
				return o, false // made at run time: a random system name
			}
			o.Default = s
		}
	case "cfg::_enum":
		list, ok := enums[targs]
		if !ok {
			return o, false // an enum of the PS3's own (a licence area) not declared here
		}
		names := rpcs3Formats(typesCPP, targs)
		if len(names) == 0 {
			return o, false
		}
		o.Type = "enum"
		for _, e := range list {
			if n, ok := names[e.Name]; ok {
				o.Choices = append(o.Choices, emulators.Choice{Value: n, Label: n})
			}
		}
		name := def[strings.LastIndex(def, ":")+1:]
		v, ok := names[name]
		if !ok {
			return o, false
		}
		o.Default = v
		o.FileType = "string"
	default:
		return o, false // a list, a map, the log levels
	}
	return o, true
}

// rpcs3Formats is the text RPCS3 writes for each value of an enum, from its formatter.
func rpcs3Formats(cpp, typ string) map[string]string {
	loc := regexp.MustCompile(`fmt_class_string<` + regexp.QuoteMeta(typ) + `>::format`).FindStringIndex(cpp)
	if loc == nil {
		return nil
	}
	body := braceBody(cpp, loc[1])
	out := map[string]string{}
	for _, m := range rpcs3Case.FindAllStringSubmatch(body, -1) {
		if _, dup := out[m[1]]; !dup {
			out[m[1]] = m[2]
		}
	}
	return out
}
