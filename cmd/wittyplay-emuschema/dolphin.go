// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Dolphin declares each setting as a Config::Info: its system (which file), section, key and
// default, as in
//
//	const Info<bool> GFX_WIDESCREEN_HACK{{System::GFX, "Settings", "wideScreenHack"}, false};
//
// Enums are kept in the INI files as their numbers. Input (the pad and Wii remote files) isn't
// read: WittyPlay drives the emulator's controllers itself.
var dolphinFiles = []string{
	"Source/Core/Core/Config/MainSettings.cpp",
	"Source/Core/Core/Config/GraphicsSettings.cpp",
	"Source/Core/VideoCommon/VideoConfig.h",
	"Source/Core/Core/Config/MainSettings.h",
	"Source/Core/Core/PowerPC/PowerPC.h",
	"Source/Core/AudioCommon/Enums.h",
	"Source/Core/Core/HW/EXI/EXI_Device.h",
	"Source/Core/Core/HW/SI/SI_Device.h",
	"Source/Core/Core/HW/HSP/HSP_Device.h",
	"Source/Core/DiscIO/Enums.h",
	"Source/Core/Core/HW/Memmap.h",
}

// With -u <dir>, Dolphin keeps its settings in <dir>/Config.
var dolphinSystems = map[string]string{"Main": "Config/Dolphin.ini", "GFX": "Config/GFX.ini"}

// Defaults Dolphin works out when it runs, as a Linux x86-64 build works them out (each from the
// function named, at this tag).
var dolphinRuntimeDefaults = map[string]string{
	"PowerPC::DefaultCPUCore()":                       "1", // PowerPC.cpp: JIT64 on x86-64
	"AudioCommon::GetDefaultDPL2Quality()":            "2", // AudioCommon.cpp: DPL2Quality::High
	"GetDefaultRegion()":                              "",  // from the system's locale: not offered
	"AudioCommon::GetDefaultSoundBackend()":           "",  // depends on the build: not offered
	"VideoBackendBase::GetDefaultBackendConfigName()": "",
}

var dolphinInfo = regexp.MustCompile(`(?s)Info<([\w:]+)>\s*(?:\w+\s*)?\{\s*\{\s*(?:Config::)?System::(\w+)\s*,\s*"([^"]*)"\s*,\s*"([^"]*)"\s*\}\s*,\s*((?:[^{}]|\{\})*?)\s*\}\s*\}?\s*[;,]`)

func dolphinPrefix(o option) string {
	if o.File == dolphinSystems["GFX"] {
		return "gfx"
	}
	return "main"
}

func extractDolphin(files map[string]string) ([]option, error) {
	consts := map[string]string{}
	var headers strings.Builder
	for _, p := range dolphinFiles {
		if strings.HasSuffix(p, ".h") {
			h := preprocess(files[p])
			parseConsts(h, consts)
			headers.WriteString(h + "\n")
		}
	}
	enums := parseEnums(headers.String(), consts)
	var out []option
	for _, p := range dolphinFiles[:2] {
		src := preprocess(files[p])
		parseConsts(src, consts)
		opts, err := dolphinOptions(src, consts, enums)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, opts...)
	}
	return out, nil
}

func dolphinOptions(src string, consts map[string]string, enums map[string][]enumerator) ([]option, error) {
	var out []option
	for _, m := range dolphinInfo.FindAllStringSubmatch(src, -1) {
		typ, system, section, key, def := m[1], m[2], m[3], m[4], strings.TrimSpace(m[5])
		file, ok := dolphinSystems[system]
		if !ok {
			continue
		}
		o := option{File: file, Format: "ini", Path: []string{section, key}, Group: dolphinGroup(system, section)}
		if rd, ok := dolphinRuntimeDefaults[def]; ok {
			if rd == "" {
				continue
			}
			def = rd
		}
		switch typ {
		case "bool":
			o.Type = "bool"
			if b, ok := boolean(def, consts); ok {
				o.Default = b
			}
			// Dolphin writes and reads "True" and "False".
			o.Values = map[string]string{"true": "True", "false": "False"}
		case "int", "u32", "s32", "u64", "s16", "u16", "u8", "s8":
			o.Type = "int"
			if f, ok := number(def, consts); ok {
				o.Default = f
			}
		case "float", "double":
			o.Type = "float"
			if f, ok := number(def, consts); ok {
				o.Default = f
			}
		case "std::string":
			o.Type = "string"
			if s, ok := cString(def); ok {
				o.Default = s
			}
		default:
			name := typ[strings.LastIndex(typ, ":")+1:]
			list, ok := enums[name]
			if !ok || len(list) == 0 {
				return nil, fmt.Errorf("%s/%s: no enum %s", section, key, typ)
			}
			o.Type = "enum"
			o.Choices = enumChoices(list, false)
			local := map[string]string{}
			for k, v := range consts {
				local[k] = v
			}
			for _, e := range list {
				local[name+"::"+e.Name] = strconv.FormatInt(e.Value, 10)
				local[e.Name] = strconv.FormatInt(e.Value, 10)
			}
			if f, ok := number(def, local); ok {
				o.Default = strconv.FormatInt(int64(f), 10)
			}
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no settings found")
	}
	return out, nil
}

func dolphinGroup(system, section string) string {
	if system == "GFX" {
		return groupLabel("Graphics", section)
	}
	return groupLabel(section)
}
