// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/emulators/configfile"
)

// TestGeneratedSchemas: every generated schema holds up as the manifest's own settings do, has
// as many options as the emulator's source declares (at least these many offered), and can be
// written into an empty settings folder and read back.
//
// The minimums are what the sources at the pinned versions hold, less what's fixed (paths,
// network, the updater) and what this can't place (defaults worked out at run time).
func TestGeneratedSchemas(t *testing.T) {
	for id, min := range map[string]int{
		"dolphin": 150, "pcsx2": 150, "ppsspp": 150, "rpcs3": 150,
		"xemu": 30, "melonds": 30, "flycast": 50, "cemu": 30,
		"ryujinx": 60, "lichii": 180,
	} {
		t.Run(id, func(t *testing.T) {
			list := GeneratedSettings(id)
			if err := checkSettings(list); err != nil {
				t.Fatal(err)
			}
			if n := len(Offered(list)); n < min {
				t.Errorf("%d settings offered, want at least %d", n, min)
			}
			if GeneratedVersion(id) == "" {
				t.Error("no version")
			}
			for _, s := range Offered(list) {
				if s.Apply == nil || s.Apply.File == nil {
					t.Fatalf("%s: no file target", s.Key)
				}
				if Sensitive(append(slices.Clone(s.Apply.File.Path), s.Group)...) {
					t.Errorf("%s (%v) looks like a path or the network but is offered", s.Key, s.Apply.File.Path)
				}
			}
			// Every default, written to a new folder, reads back as written.
			dir := t.TempDir()
			plan := Plan(list, Effective(list, nil, nil, nil))
			if err := plan.WriteFiles(dir); err != nil {
				t.Fatal(err)
			}
			for file, fe := range plan.Edits {
				b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
				if err != nil {
					t.Fatal(err)
				}
				got, err := configfile.Entries(fe.Format, b)
				if err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				for _, e := range fe.Edits {
					i := slices.IndexFunc(got, func(en configfile.Entry) bool { return slices.Equal(en.Path, e.Path) })
					if i < 0 || !configfile.SameValue(fe.Format, got[i].Value, e.Value) {
						t.Errorf("%s %v: wrote %#v", file, e.Path, e.Value)
					}
				}
			}
		})
	}
	if GeneratedSettings("nothing") != nil {
		t.Error("a schema for an emulator that has none")
	}
}

func TestFullSchema(t *testing.T) {
	gen := GeneratedSettings("pcsx2")
	var renderer Setting
	for _, s := range gen {
		if f := s.Apply.File; f != nil && slices.Equal(f.Path, []string{"EmuCore/GS", "Renderer"}) {
			renderer = s
		}
	}
	if renderer.Key == "" {
		t.Fatal("PCSX2's renderer isn't in its schema")
	}
	curated := []Setting{
		// The same value, by another key and another kind of target: the curated one stands.
		{Key: "renderer", Kind: KindChoice, Label: "Graphics backend", Default: "vulkan", Scope: ScopeAdmin,
			Choices: []Choice{{Value: "vulkan", Label: "Vulkan"}},
			Apply:   &Apply{INI: &INITarget{File: renderer.Apply.File.File, Section: "EmuCore/GS", Key: "Renderer", Values: map[string]string{"vulkan": "14"}}}},
		// The same key as a generated one: the curated one stands.
		{Key: gen[0].Key, Kind: KindToggle, Label: "Mine", Default: true, Scope: ScopeUser},
	}
	full := FullSchema(curated, "pcsx2")
	if err := checkSettings(full); err != nil {
		t.Fatal(err)
	}
	if len(full) != len(gen)-2+2 {
		t.Errorf("%d settings, want %d", len(full), len(gen))
	}
	if slices.ContainsFunc(full, func(s Setting) bool { return s.Key == renderer.Key }) {
		t.Error("the generated renderer is offered beside the curated one")
	}
	if i := slices.IndexFunc(full, func(s Setting) bool { return s.Key == gen[0].Key }); full[i].Label != "Mine" {
		t.Error("a generated setting replaced a curated one")
	}
}

func TestDiscover(t *testing.T) {
	known := []Setting{{Key: "a", Kind: KindToggle, Label: "A", Default: false, Scope: ScopeUser,
		Apply: &Apply{File: &FileTarget{File: "Config/GFX.ini", Format: "ini", Path: []string{"Settings", "Known"}}}}}
	ini := "[Settings]\nKnown = True\nNewHack = True\nScale = 2.5\nShaderName = crt\nDumpPath = /tmp/x\nList = [1, 2]\n"
	got := Discover("ini", []byte(ini), known)
	byPath := map[string]Setting{}
	for _, s := range got {
		byPath[strings.Join(s.Apply.File.Path, "/")] = s
	}
	if _, ok := byPath["Settings/Known"]; ok {
		t.Error("a known setting was found again")
	}
	if s := byPath["Settings/NewHack"]; s.Kind != KindToggle || s.Default != true || s.Apply.File.Values["true"] != "True" || s.Label != "New hack" || s.Group != "Settings" || s.Scope != ScopeUser || s.Apply.File.File != "Config/GFX.ini" {
		t.Errorf("a toggle: %+v", s)
	}
	if s := byPath["Settings/Scale"]; s.Kind != KindNumber || s.Default != 2.5 {
		t.Errorf("a number: %+v", s)
	}
	if s := byPath["Settings/ShaderName"]; s.Kind != KindText || s.Fixed {
		t.Errorf("text: %+v", s)
	}
	if s := byPath["Settings/DumpPath"]; !s.Fixed {
		t.Errorf("a path is offered: %+v", s)
	}
	if _, ok := byPath["Settings/List"]; ok {
		t.Error("a list was offered as text")
	}
	if err := checkSettings(append(known, got...)); err != nil {
		t.Error(err)
	}
	// TOML: typed values, and a list that can't be written back is left out.
	tknown := []Setting{{Key: "b", Kind: KindNumber, Label: "B", Default: 1.0, Scope: ScopeUser,
		Apply: &Apply{File: &FileTarget{File: "xemu.toml", Format: "toml", Path: []string{"display", "quality", "surface_scale"}}}}}
	toml := "[display.quality]\nsurface_scale = 2\nnew_option = true\n[net]\nforward = [1]\nremote_addr = '1.2.3.4'\n"
	tgot := Discover("toml", []byte(toml), tknown)
	if len(tgot) != 2 || tgot[0].Kind != KindToggle || !tgot[1].Fixed {
		t.Errorf("toml: %+v", tgot)
	}
	// With no known file in that format, nothing can be written back: nothing is found.
	if got := Discover("yaml", []byte("A: 1\n"), known); got != nil {
		t.Errorf("found with nowhere to write: %+v", got)
	}
}

func TestFileTargets(t *testing.T) {
	list := []Setting{
		{Key: "res", Kind: KindNumber, Label: "Resolution scale", Default: 100.0, Step: 1, Scope: ScopeUser,
			Apply: &Apply{File: &FileTarget{File: "config.yml", Format: "yaml", Path: []string{"Video", "Resolution Scale"}}}},
		{Key: "rate", Kind: KindNumber, Label: "Rate", Default: 60.0, Scope: ScopeUser,
			Apply: &Apply{File: &FileTarget{File: "config.yml", Format: "yaml", Path: []string{"Video", "Rate"}}}},
		{Key: "renderer", Kind: KindChoice, Label: "Renderer", Default: "Vulkan", Scope: ScopeAdmin, Choices: []Choice{{Value: "Vulkan", Label: "Vulkan"}, {Value: "OpenGL", Label: "OpenGL"}},
			Apply: &Apply{File: &FileTarget{File: "config.yml", Format: "yaml", Path: []string{"Video", "Renderer"}}}},
		{Key: "mem", Kind: KindChoice, Label: "Memory", Default: "64", Scope: ScopeAdmin, Choices: []Choice{{Value: "64", Label: "64 MB"}, {Value: "128", Label: "128 MB"}},
			Apply: &Apply{File: &FileTarget{File: "xemu.toml", Format: "toml", Path: []string{"sys", "mem_limit"}, Type: "string"}}},
		{Key: "level", Kind: KindChoice, Label: "Level", Default: "2", Scope: ScopeUser, Choices: []Choice{{Value: "1", Label: "One"}, {Value: "2", Label: "Two"}},
			Apply: &Apply{File: &FileTarget{File: "xemu.toml", Format: "toml", Path: []string{"sys", "level"}}}},
		{Key: "wide", Kind: KindToggle, Label: "Wide", Default: false, Scope: ScopeUser,
			Apply: &Apply{File: &FileTarget{File: "Config/GFX.ini", Format: "ini", Path: []string{"Settings", "wideScreenHack"}, Values: map[string]string{"true": "True", "false": "False"}}}},
		{Key: "api", Kind: KindNumber, Label: "API", Default: 1.0, Step: 1, Scope: ScopeAdmin,
			Apply: &Apply{File: &FileTarget{File: "settings.xml", Format: "xml", Path: []string{"content", "Graphic", "api"}}}},
	}
	if err := checkSettings(list); err != nil {
		t.Fatal(err)
	}
	eff := Effective(list, Values{"renderer": "OpenGL"}, Values{"res": 150.0, "wide": true}, nil)
	plan := Plan(list, eff)
	dir := t.TempDir()
	// An INI file the emulator wrote, with keys of its own.
	if err := os.MkdirAll(filepath.Join(dir, "Config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Config", "GFX.ini"), []byte("[Settings]\nMSAA = 4\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"config.yml":     "Video:\n  Resolution Scale: 150\n  Rate: 60.0\n  Renderer: OpenGL\n",
		"xemu.toml":      "[sys]\nmem_limit = \"64\"\nlevel = 2\n",
		"Config/GFX.ini": "[Settings]\nMSAA = 4\nwideScreenHack = True\n",
		"settings.xml":   "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<content>\n    <Graphic>\n        <api>1</api>\n    </Graphic>\n</content>\n",
	} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != want {
			t.Errorf("%s:\n%s\nwant:\n%s", file, b, want)
		}
	}
	// One value the file holds as a list: the others are still written.
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("Video:\n  Rate: [1, 2]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "config.yml")); !strings.Contains(string(b), "Rate: [1, 2]") || !strings.Contains(string(b), "Renderer: OpenGL") {
		t.Errorf("a refused value stopped the rest: %s", b)
	}
	// A file this can't read at all is left as it is, and the start is refused.
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte("Video:\n\t- what\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(dir); err == nil {
		t.Error("a file this can't read was written over")
	}
	// A file named as two formats.
	mixed := Applied{Files: map[string][]INIEdit{"a.cfg": {{"S", "k", "v"}}}, Edits: map[string]FileEdits{"a.cfg": {Format: "toml", Edits: []configfile.Edit{{Path: []string{"k"}, Value: "v"}}}}}
	if err := mixed.WriteFiles(t.TempDir()); err == nil {
		t.Error("a file written as both INI and TOML")
	}
}

func TestFileTargetsAreChecked(t *testing.T) {
	target := func(f FileTarget) Setting {
		return Setting{Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{File: &f}}
	}
	for name, s := range map[string]Setting{
		"a path out":         target(FileTarget{File: "../x.ini", Format: "ini", Path: []string{"s", "k"}}),
		"an absolute path":   target(FileTarget{File: "/etc/x.ini", Format: "ini", Path: []string{"s", "k"}}),
		"a strange name":     target(FileTarget{File: "a;b.ini", Format: "ini", Path: []string{"s", "k"}}),
		"no format":          target(FileTarget{File: "a.ini", Path: []string{"s", "k"}}),
		"an unknown format":  target(FileTarget{File: "a.csv", Format: "csv", Path: []string{"k"}}),
		"no path":            target(FileTarget{File: "a.toml", Format: "toml"}),
		"an empty part":      target(FileTarget{File: "a.toml", Format: "toml", Path: []string{"a", ""}}),
		"a long part":        target(FileTarget{File: "a.toml", Format: "toml", Path: []string{strings.Repeat("a", 129)}}),
		"a line break":       target(FileTarget{File: "a.toml", Format: "toml", Path: []string{"a\nb"}}),
		"an INI path of 3":   target(FileTarget{File: "a.ini", Format: "ini", Path: []string{"s", "k", "x"}}),
		"an INI key with =":  target(FileTarget{File: "a.ini", Format: "ini", Path: []string{"s", "k=v"}}),
		"an XML name":        target(FileTarget{File: "a.xml", Format: "xml", Path: []string{"content", "a b"}}),
		"a value with a NUL": target(FileTarget{File: "a.ini", Format: "ini", Path: []string{"s", "k"}, Values: map[string]string{"x": "a\x00"}}),
		"an unknown type":    target(FileTarget{File: "a.toml", Format: "toml", Path: []string{"k"}, Type: "date"}),
		"two targets": {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{
			File: &FileTarget{File: "a.toml", Format: "toml", Path: []string{"k"}}, Env: "SOME_VAR"}},
	} {
		if checkSettings([]Setting{s}) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := checkSettings([]Setting{target(FileTarget{File: "Config/GFX.ini", Format: "ini", Path: []string{"Settings", "Shader Name (x)"}})}); err != nil {
		t.Errorf("a good target: %v", err)
	}
}

func TestSettingLabel(t *testing.T) {
	for key, want := range map[string]string{
		"wideScreenHack":     "Wide screen hack",
		"upscale_multiplier": "Upscale multiplier",
		"EFBAccessEnable":    "EFB access enable",
		"SkipIPL":            "Skip IPL",
		"MSAA":               "MSAA",
		"PPU Decoder":        "PPU decoder",
		"AdapterRumble1":     "Adapter rumble 1",
		"3D":                 "3D",
	} {
		if got := SettingLabel(key); got != want {
			t.Errorf("%q: %q, want %q", key, got, want)
		}
	}
}

func TestSettingKey(t *testing.T) {
	taken := map[string]bool{}
	for _, tc := range []struct {
		prefix string
		path   []string
		want   string
	}{
		{"gfx", []string{"Settings", "wideScreenHack"}, "gfx.settings.wideScreenHack"},
		{"", []string{"Video", "Resolution Scale"}, "video.resolutionScale"},
		{"", []string{"3D", "Renderer"}, "k3d.renderer"},
		{"", []string{"Video", "Resolution Scale"}, "video.resolutionScale2"},
	} {
		if got := SettingKey(tc.prefix, tc.path, taken); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.path, got, tc.want)
		}
	}
	long := SettingKey("", []string{strings.Repeat("Very long name ", 6)}, taken)
	if len(long) > 48 || !settingKey.MatchString(long) {
		t.Errorf("a long key: %q", long)
	}
}

// PCSX2's multitap is how players 3 and 4 plug in. Its setting is named for a port, which the
// network filter once took for a network port and hid, so four players could never play.
func TestPCSX2MultitapIsOffered(t *testing.T) {
	for _, key := range []string{"pad.multitapPort1", "pad.multitapPort2"} {
		found := false
		for _, s := range Offered(GeneratedSettings("pcsx2")) {
			if s.Key == key {
				found = true
				if s.Default != false || s.Help == "" {
					t.Errorf("%s: %+v", key, s)
				}
			}
		}
		if !found {
			t.Errorf("%s isn't offered", key)
		}
	}
	if Sensitive("Pad", "MultitapPort1") {
		t.Error("a multitap's port taken for a network port")
	}
	if !Sensitive("Net", "Port") || !Sensitive("ServerPort") {
		t.Error("a network port let through")
	}
}
