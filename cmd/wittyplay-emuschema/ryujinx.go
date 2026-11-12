// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/wittywilla/wittyplay/emulators"
)

// Ryujinx (Ryubing) keeps its settings in Config.json, the class ConfigurationFileFormat written
// by System.Text.Json with a snake_case naming policy (Ryujinx.Common/Utilities/JsonHelper.cs):
//
//	public bool EnableFileLog { get; set; }        // "enable_file_log"
//
// Each property is filled from the running settings in ConfigurationState.ToFileFormat
// (EnableFileLog = Logger.EnableFileLog), whose defaults ConfigurationState.LoadDefault sets
// (Logger.EnableFileLog.Value = true;). A setting LoadDefault doesn't touch starts at its type's
// zero, which is only certain enough to write for a bool. An enum with
// [JsonConverter(typeof(TypedStringEnumConverter<T>))] is written as its member's name, any
// other as its number. Lists (input, game folders, the log filter), the hotkeys and the UI's
// nested objects (columns, window position) aren't single values and aren't read; a property
// ToFileFormat doesn't fill (one kept only for older files) is never written, so it isn't
// either.
var ryujinxFiles = []string{
	"src/Ryujinx/Systems/Configuration/ConfigurationFileFormat.cs",
	"src/Ryujinx/Systems/Configuration/ConfigurationState.cs",
	"src/Ryujinx.Common/Configuration/AntiAliasing.cs",
	"src/Ryujinx.Common/Configuration/AspectRatioExtensions.cs",
	"src/Ryujinx.Common/Configuration/BackendThreading.cs",
	"src/Ryujinx.Common/Configuration/GraphicsBackend.cs",
	"src/Ryujinx.Common/Configuration/GraphicsDebugLevel.cs",
	"src/Ryujinx.Common/Configuration/HideCursorMode.cs",
	"src/Ryujinx.Common/Configuration/MemoryManagerMode.cs",
	"src/Ryujinx.Common/Configuration/Multiplayer/MultiplayerMode.cs",
	"src/Ryujinx.Common/Configuration/ScalingFilter.cs",
	"src/Ryujinx.Common/Configuration/VSyncMode.cs",
	"src/Ryujinx/Systems/Configuration/System/Language.cs",
	"src/Ryujinx/Systems/Configuration/System/Region.cs",
	"src/Ryujinx/Systems/Configuration/AudioBackend.cs",
	"src/Ryujinx.HLE/MemoryConfiguration.cs",
	"src/Ryujinx/Systems/Configuration/UI/UpdaterType.cs",
	"src/Ryujinx/Systems/Configuration/UI/FocusLostType.cs",
}

const ryujinxJSON = "Config.json"

var (
	ryujinxProp      = regexp.MustCompile(`(?m)^\s*public\s+([\w<>\[\],. ]+?)\s+(\w+)\s*\{\s*get;\s*set;\s*\}`)
	ryujinxToFile    = regexp.MustCompile(`ConfigurationFileFormat\s+\w+\s*=\s*new\s*\(\s*\)`)
	ryujinxLoadDef   = regexp.MustCompile(`void\s+LoadDefault\s*\(\s*\)`)
	ryujinxAssign    = regexp.MustCompile(`(?m)^\s*([\w.]+)\.Value\s*=\s*([^;\n]+);\s*$`)
	ryujinxConverter = regexp.MustCompile(`TypedStringEnumConverter<(\w+)>`)
	ryujinxModel     = regexp.MustCompile(`^[\w.]+$`)
)

// ryujinxGroups names the sections of ConfigurationState, which group the settings.
var ryujinxGroups = map[string]string{"Graphics": "Graphics", "Logger": "Logging", "System": "System", "Hid": "Input",
	"UI": "Interface", "Multiplayer": "Multiplayer", "Debug": "Debug", "Hacks": "Hacks"}

func extractRyujinx(files map[string]string) ([]option, error) {
	format := stripComments(fileOf(files, "Configuration/ConfigurationFileFormat.cs"))
	state := stripComments(fileOf(files, "Configuration/ConfigurationState.cs"))
	if format == "" || state == "" {
		return nil, fmt.Errorf("no ConfigurationFileFormat.cs or ConfigurationState.cs")
	}
	var enumSrc strings.Builder
	for p, s := range files {
		if !strings.HasSuffix(p, "ConfigurationFileFormat.cs") && !strings.HasSuffix(p, "ConfigurationState.cs") {
			enumSrc.WriteString(stripComments(s) + "\n")
		}
	}
	enums := parseEnums(enumSrc.String(), nil)
	named := map[string]bool{}
	for _, m := range ryujinxConverter.FindAllStringSubmatch(enumSrc.String(), -1) {
		named[m[1]] = true
	}

	// Which running setting each property is written from.
	loc := ryujinxToFile.FindStringIndex(state)
	if loc == nil {
		return nil, fmt.Errorf("no ToFileFormat")
	}
	from := map[string]string{}
	for _, item := range splitTop(braceBody(state, loc[1]), ',') {
		name, expr, ok := strings.Cut(item, "=")
		expr = strings.TrimSpace(expr)
		if !ok || !ryujinxModel.MatchString(expr) {
			continue // a nested object of the UI's, or a value worked out from a list
		}
		from[strings.TrimSpace(name)] = expr
	}
	// Their defaults.
	loc = ryujinxLoadDef.FindStringIndex(state)
	if loc == nil {
		return nil, fmt.Errorf("no LoadDefault")
	}
	defaults := map[string]string{}
	for _, m := range ryujinxAssign.FindAllStringSubmatch(braceBody(state, loc[1]), -1) {
		defaults[m[1]] = strings.TrimSpace(m[2])
	}

	var out []option
	for _, m := range ryujinxProp.FindAllStringSubmatch(format, -1) {
		typ, prop := strings.TrimSpace(m[1]), m[2]
		model, ok := from[prop]
		if !ok {
			continue // not written: kept only so older files still read
		}
		key := ryujinxSnake(prop)
		group := "General"
		if section, _, ok := strings.Cut(model, "."); ok {
			if g, ok := ryujinxGroups[section]; ok {
				group = g
			}
		}
		o := option{File: ryujinxJSON, Format: "json", Path: []string{key}, Group: group}
		def, hasDef := defaults[model]
		switch typ {
		case "bool":
			o.Type, o.Default = "bool", false
			if hasDef {
				b, ok := boolean(def, nil)
				if !ok || (def != "true" && def != "false") {
					continue // worked out at run time (by the host's OS)
				}
				o.Default = b
			}
		case "int", "long", "uint", "ulong", "ushort", "short", "byte", "float", "double":
			o.Type = "int"
			if typ == "float" || typ == "double" {
				o.Type = "float"
			}
			if !hasDef {
				continue // its zero isn't known to be a default anyone chose
			}
			f, ok := number(def, nil)
			if !ok {
				continue
			}
			o.Default = f
		case "string":
			if !hasDef {
				continue
			}
			o.Type = "string"
			if def == "string.Empty" {
				o.Default = ""
			} else if s, ok := cString(def); ok {
				o.Default = s
			} else {
				continue
			}
		default:
			list, ok := enums[typ]
			if !ok || !hasDef {
				continue // a list, an object, an array
			}
			name := def[strings.LastIndexByte(def, '.')+1:]
			if !strings.HasPrefix(def, typ+".") {
				continue // made at run time (the graphics backend, by what the host has)
			}
			o.Type, o.Choices = "enum", ryujinxChoices(list, named[typ])
			o.Default = ""
			for _, e := range list {
				if e.Name == name {
					o.Default = name
					if !named[typ] {
						o.Default = strconv.FormatInt(e.Value, 10)
					}
				}
			}
		}
		// The local wireless and online play, and the debugger: the sandbox decides what the
		// emulator may reach.
		l := strings.ToLower(key)
		if strings.Contains(l, "multiplayer") || strings.Contains(l, "ldn") || strings.Contains(l, "gdb") {
			o.Fixed = true
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

// ryujinxSnake names a property the way Ryujinx's SnakeCaseNamingPolicy does: an underscore
// before a capital that follows a lower-case letter or a digit, then all lower case
// ("EnableMacroHLE" is enable_macro_hle, "VSyncMode" vsync_mode).
func ryujinxSnake(name string) string {
	var b strings.Builder
	rs := []rune(name)
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i != 0 && !unicode.IsUpper(rs[i-1]) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ryujinxChoices makes an enum's members into choices: written as the name or the number, and
// labelled from the name less the part every member shares ("MemoryConfiguration" of
// MemoryConfiguration4GiB).
func ryujinxChoices(list []enumerator, asName bool) []emulators.Choice {
	var names []string
	for _, e := range list {
		if !isSentinel(e.Name) {
			names = append(names, e.Name)
		}
	}
	prefix := ""
	if len(names) > 1 {
		prefix = names[0]
		for _, n := range names[1:] {
			for !strings.HasPrefix(n, prefix) {
				prefix = prefix[:len(prefix)-1]
			}
		}
		// Only whole words: back off to the last capital.
		for prefix != "" && (len(prefix) >= len(names[0]) || !unicode.IsUpper(rune(names[0][len(prefix)])) && !unicode.IsDigit(rune(names[0][len(prefix)]))) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	var out []emulators.Choice
	for _, e := range list {
		if isSentinel(e.Name) {
			continue
		}
		v := strconv.FormatInt(e.Value, 10)
		if asName {
			v = e.Name
		}
		label := strings.TrimPrefix(e.Name, prefix)
		if label == "" {
			label = e.Name
		}
		out = append(out, emulators.Choice{Value: v, Label: ryujinxLabel(label)})
	}
	return out
}

// ryujinxLabel puts a member's name in words, keeping numbers and capitals with the word they
// belong to: "Fixed16x9" is "Fixed 16x9", "SDL2" stays, "HostMappedUnsafe" "Host mapped unsafe", "4GiB" stays.
func ryujinxLabel(name string) string {
	rs := []rune(name)
	var words []string
	start := 0
	for i := 1; i < len(rs); i++ {
		prev, cur := rs[i-1], rs[i]
		nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
		if (unicode.IsLower(prev) && unicode.IsDigit(cur) && i > 1 && unicode.IsLetter(rs[i-2])) ||
			(unicode.IsLower(prev) && unicode.IsUpper(cur) && nextLower) ||
			(unicode.IsUpper(prev) && unicode.IsUpper(cur) && nextLower && i-1 > start) {
			words = append(words, string(rs[start:i]))
			start = i
		}
	}
	words = append(words, string(rs[start:]))
	for i := 1; i < len(words); i++ {
		w := []rune(words[i])
		if len(w) > 1 && unicode.IsUpper(w[0]) && strings.ToLower(string(w[1:])) == string(w[1:]) {
			words[i] = strings.ToLower(words[i])
		}
	}
	return strings.Join(words, " ")
}
