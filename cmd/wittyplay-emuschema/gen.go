// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/emulators"
)

// option is one setting as an extractor found it in an emulator's source, before it's made into
// an emulators.Setting.
type option struct {
	File, Format string
	Path         []string
	Type         string // bool, int, float, string or enum
	Default      any    // bool, float64 or string; an enum's is the value of one of Choices
	Choices      []emulators.Choice
	// Values translates the setting's value to the file's spelling (a toggle written "True").
	Values map[string]string
	Group  string
	// FileType is how a typed file (TOML, YAML, JSON) holds the value, when the setting's kind
	// doesn't say: an enum written as a string or as a number.
	FileType string
	// Label, when the last part of Path doesn't make a good one.
	Label string
	// Scope, Advanced and Fixed, when an extractor knows better than the words in the name.
	Scope    string
	Advanced bool
	Fixed    bool
	Min, Max float64
}

// Words in a setting's path or group that make it one a person tunes for themselves, and per
// game: how it looks, how fast it runs, how it sounds.
var userWords = words(`graphics gfx video gs renderer render rendering enhancements enhancement hacks hack
	speedhacks speed audio sound spu2 volume framerate fps frameskip frame skip upscale upscaling
	resolution internal texture textures filter filtering shader shaders aspect widescreen wide
	vsync anisotropy anisotropic msaa ssaa antialiasing aa fxaa postprocessing post sharpness
	sharpening scaling scale scaler overclock clock cpu gpu emulation stretch crop bilinear
	smoothing stereoscopy stereo color colour gamma brightness contrast saturation latency
	interlace deinterlace mipmap mipmapping blending blend dithering depth fog lighting bloom
	quality cas tv crt fastforward turbo limiter limit accuracy precise accurate threads
	threaded sync jit recompiler interpreter mmu cache fastmem dsp cycle cycles timing vu ee iop
	fpu clamping rounding round mode hle lle`)

// Words that make a setting advanced: for debugging, logging, development or the emulator's
// own window and lists rather than the game.
var advancedWords = words(`debug debugging debugger log logging logs dump dumping dumps trace
	tracing profiler profiling profile validation verbose dev developer devtools test testing
	experimental fifo breakpoint breakpoints disasm disassembly memview watch symbol symbols
	column columns gamelist list sort window geometry position pos toolbar statusbar theme
	ui interface icon icons tooltip tooltips confirm prompt hotkey hotkeys shortcut tips
	recompiler jitoff off overlay stats statistics wireframe validation layer capture
	screenshot screenshots png recording record movie replay tas savestate`)

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func hasWord(set map[string]bool, names ...string) bool {
	for _, n := range names {
		for _, w := range strings.FieldsFunc(emulators.SettingLabel(n), func(r rune) bool { return r == ' ' || r == ',' }) {
			if set[strings.ToLower(w)] {
				return true
			}
		}
	}
	return false
}

// groupLabel is a section in words: "EmuCore/GS" is "Emu core, GS".
func groupLabel(parts ...string) string {
	var out []string
	for _, p := range parts {
		for _, q := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '.' }) {
			if l := emulators.SettingLabel(q); l != "" {
				out = append(out, l)
			}
		}
	}
	return strings.Join(out, ", ")
}

// settings makes options into settings: keys, labels, kinds, scopes, and the sensitive ones
// fixed. keyPrefix gives each option's key its first part (or none). Options it can't make a
// setting of (no default it can tell, two at the same place) are given back with why.
func settings(opts []option, keyPrefix func(option) string) ([]emulators.Setting, []string) {
	var out []emulators.Setting
	var skipped []string
	taken := map[string]bool{}
	at := map[string]bool{}
	for _, o := range opts {
		where := o.File + ":" + strings.Join(o.Path, "/")
		if at[where] {
			continue // the same value declared twice (for two platforms, say): the first stands
		}
		at[where] = true
		if o.Format != "ini" && o.FileType == "" {
			// A typed file: say how it holds the value, so a choice of "64" isn't written as
			// a number where the emulator reads text, or the other way round.
			o.FileType = o.Type
			if o.Type == "enum" {
				o.FileType = "int"
				for _, c := range o.Choices {
					if _, err := strconv.ParseInt(c.Value, 10, 64); err != nil {
						o.FileType = "string"
					}
				}
			}
		}
		s := emulators.Setting{
			Label: o.Label,
			Group: o.Group,
			Apply: &emulators.Apply{File: &emulators.FileTarget{File: o.File, Format: o.Format, Path: slices.Clone(o.Path), Values: o.Values, Type: o.FileType}},
			Min:   o.Min, Max: o.Max,
		}
		switch o.Type {
		case "bool":
			b, ok := o.Default.(bool)
			if !ok {
				skipped = append(skipped, where+" (no default)")
				continue
			}
			s.Kind, s.Default = emulators.KindToggle, b
		case "int", "float":
			f, ok := o.Default.(float64)
			if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
				skipped = append(skipped, where+" (no default)")
				continue
			}
			if o.Type == "int" {
				s.Step = 1
			}
			s.Kind, s.Default = emulators.KindNumber, f
			if (s.Min != 0 || s.Max != 0) && (f < s.Min || f > s.Max) {
				s.Min, s.Max = 0, 0
			}
		case "string":
			str, ok := o.Default.(string)
			if !ok {
				skipped = append(skipped, where+" (no default)")
				continue
			}
			s.Kind, s.Default = emulators.KindText, str
		case "enum":
			str, ok := o.Default.(string)
			if !ok || !slices.ContainsFunc(o.Choices, func(c emulators.Choice) bool { return c.Value == str }) {
				skipped = append(skipped, fmt.Sprintf("%s (default %v isn't one of its choices)", where, o.Default))
				continue
			}
			s.Kind, s.Default, s.Choices = emulators.KindChoice, str, o.Choices
		default:
			skipped = append(skipped, where+" (type "+o.Type+")")
			continue
		}
		if _, err := s.Clean(s.Default); err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (default: %v)", where, err))
			continue
		}
		names := append(slices.Clone(o.Path), o.Group)
		s.Fixed = o.Fixed || emulators.Sensitive(names...)
		if s.Fixed && s.Kind == emulators.KindToggle && hasWord(words("telemetry analytics update updates autoupdate discord achievements cheevos upload online netplay"), names...) {
			// Never on, whatever the emulator's own default: these are the ones that phone out.
			s.Default = false
		}
		switch {
		case o.Scope != "":
			s.Scope = o.Scope
		case !s.Fixed && hasWord(userWords, names...) && !hasWord(advancedWords, o.Path...):
			s.Scope = emulators.ScopeUser
		default:
			s.Scope = emulators.ScopeAdmin
		}
		s.Advanced = o.Advanced || hasWord(advancedWords, names...)
		prefix := ""
		if s.Label == "" {
			s.Label = emulators.SettingLabel(o.Path[len(o.Path)-1])
		}
		if keyPrefix != nil {
			prefix = keyPrefix(o)
		}
		s.Key = emulators.SettingKey(prefix, o.Path, taken)
		out = append(out, s)
	}
	return out, skipped
}

// enumChoices makes an enum's values into choices: the value as the file holds it, the name in
// words.
func enumChoices(list []enumerator, asName bool) []emulators.Choice {
	out := make([]emulators.Choice, 0, len(list))
	seen := map[string]bool{}
	// A prefix every name shares ("renderer3D_" in renderer3D_Software) says nothing.
	prefix, first := "", true
	for _, e := range list {
		if isSentinel(e.Name) {
			continue
		}
		p := e.Name[:strings.LastIndexByte(e.Name, '_')+1]
		if first {
			prefix, first = p, false
		} else if p != prefix {
			prefix = ""
			break
		}
	}
	for _, e := range list {
		if isSentinel(e.Name) {
			continue
		}
		v := strconv.FormatInt(e.Value, 10)
		if asName {
			v = e.Name
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		name := strings.TrimPrefix(e.Name, prefix)
		if prefix == "" {
			name = trimEnumPrefix(name)
		}
		// kOpenGL: a constant's "k" isn't part of its name.
		if len(name) > 1 && name[0] == 'k' && name[1] >= 'A' && name[1] <= 'Z' {
			name = name[1:]
		}
		out = append(out, emulators.Choice{Value: v, Label: emulators.SettingLabel(name)})
	}
	return out
}

// isSentinel says whether an enumerator only counts the others.
func isSentinel(name string) bool {
	l := strings.ToLower(name)
	switch l {
	case "count", "max", "maxcount", "maxvalue", "num", "invalid", "last":
		return true
	}
	return strings.HasSuffix(l, "_count") || strings.HasPrefix(l, "num_") || strings.HasSuffix(l, "_max") ||
		(strings.HasSuffix(l, "count") && strings.ToUpper(name) == name)
}

// trimEnumPrefix drops a prefix every name of an enum shares by convention: SIDEVICE_ in
// SIDEVICE_GC_CONTROLLER.
func trimEnumPrefix(name string) string {
	if i := strings.IndexByte(name, '_'); i > 0 && strings.ToUpper(name) == name && i < len(name)-1 {
		return name[i+1:]
	}
	return name
}

// number reads a default as a number, through the constants the source defines.
func number(expr string, consts map[string]string) (float64, bool) {
	expr = strings.TrimSpace(expr)
	expr = strings.TrimSuffix(strings.TrimPrefix(expr, "{"), "}")
	return evalNum(expr, consts)
}

// boolean reads a default as a bool.
func boolean(expr string, consts map[string]string) (bool, bool) {
	switch strings.TrimSpace(expr) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	f, ok := evalNum(expr, consts)
	return f != 0, ok
}
