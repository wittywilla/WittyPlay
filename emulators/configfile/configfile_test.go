// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package configfile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	for _, tc := range []struct {
		name, format, old string
		edits             []Edit
		want              string
	}{
		{
			name:   "ini: a key changed, one added, a section added",
			format: "ini",
			old:    "; comment\n[UI]\nTheme = dark\n\n[EmuCore/GS]\nRenderer = 12\nVsyncEnable = 1\n\n[Folders]\nBios = bios\n",
			edits:  []Edit{{[]string{"EmuCore/GS", "Renderer"}, int64(14)}, {[]string{"EmuCore/GS", "upscale_multiplier"}, 3.5}, {[]string{"New", "On"}, true}},
			want:   "; comment\n[UI]\nTheme = dark\n\n[EmuCore/GS]\nRenderer = 14\nVsyncEnable = 1\nupscale_multiplier = 3.5\n\n[Folders]\nBios = bios\n\n[New]\nOn = true\n",
		},
		{
			name:   "ini: a CRLF file stays CRLF",
			format: "ini",
			old:    "[A]\r\nk = 1\r\n",
			edits:  []Edit{{[]string{"A", "k"}, "2"}},
			want:   "[A]\r\nk = 2\r\n",
		},
		{
			name:   "toml: values typed, comments kept, a table added",
			format: "toml",
			old:    "# melonDS\nLastBIOSFolder = \"x\"\n\n[3D]\nRenderer = 0 # software\nGL = { ScaleFactor = 1 }\nList = [1,\n  2]\n\n[Instance0.Audio]\nVolume = 256\n",
			edits:  []Edit{{[]string{"3D", "Renderer"}, int64(2)}, {[]string{"3D", "Threaded"}, true}, {[]string{"Instance0", "Audio", "Volume"}, int64(128)}, {[]string{"Instance0", "Window0", "Title"}, `a "b"`}, {[]string{"Top"}, 1.0}},
			want:   "# melonDS\nLastBIOSFolder = \"x\"\nTop = 1.0\n\n[3D]\nRenderer = 2 # software\nGL = { ScaleFactor = 1 }\nList = [1,\n  2]\nThreaded = true\n\n[Instance0.Audio]\nVolume = 128\n\n[Instance0.Window0]\nTitle = \"a \\\"b\\\"\"\n",
		},
		{
			name:   "toml: a table made of dotted keys gets a dotted key",
			format: "toml",
			old:    "[display]\nquality.surface_scale = 1\n",
			edits:  []Edit{{[]string{"display", "quality", "vsync"}, false}},
			want:   "[display]\nquality.surface_scale = 1\nquality.vsync = false\n",
		},
		{
			name:   "yaml: nested keys with spaces, a new map made",
			format: "yaml",
			old:    "Core:\n  PPU Decoder: Recompiler (LLVM)\n  PPU Threads: 2 # yes\n  Libraries:\n    - libfoo\nVideo:\n  Renderer: Vulkan\n  Vulkan:\n    Adapter: \"\"\nLog: {}\n",
			edits:  []Edit{{[]string{"Core", "PPU Threads"}, int64(4)}, {[]string{"Video", "Vulkan", "Adapter"}, "GPU: 1"}, {[]string{"Video", "Performance Overlay", "Enabled"}, true}, {[]string{"Audio", "Volume"}, int64(100)}, {[]string{"Video", "Renderer"}, "OpenGL"}},
			want:   "Core:\n  PPU Decoder: Recompiler (LLVM)\n  PPU Threads: 4 # yes\n  Libraries:\n    - libfoo\nVideo:\n  Renderer: OpenGL\n  Vulkan:\n    Adapter: \"GPU: 1\"\n  Performance Overlay:\n    Enabled: true\nLog: {}\nAudio:\n  Volume: 100\n",
		},
		{
			name:   "xml: text set, an element made under its parent",
			format: "xml",
			old:    "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<content>\n    <logflag>0</logflag>\n    <Graphic>\n        <api>1</api>\n        <empty/>\n    </Graphic>\n</content>\n",
			edits:  []Edit{{[]string{"content", "Graphic", "api"}, int64(0)}, {[]string{"content", "Graphic", "VSync"}, "a<b"}, {[]string{"content", "Graphic", "empty"}, true}, {[]string{"content", "Audio", "TVVolume"}, int64(50)}},
			want:   "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<content>\n    <logflag>0</logflag>\n    <Graphic>\n        <api>0</api>\n        <empty>true</empty>\n        <VSync>a&lt;b</VSync>\n    </Graphic>\n    <Audio>\n        <TVVolume>50</TVVolume>\n    </Audio>\n</content>\n",
		},
		{
			name:   "xml: a new file",
			format: "xml",
			edits:  []Edit{{[]string{"content", "a"}, "1"}},
			want:   "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<content>\n    <a>1</a>\n</content>\n",
		},
		{
			name:   "json: order kept, objects made",
			format: "json",
			old:    `{"z": 1, "a": {"b": [1, 2], "c": "x"}}`,
			edits:  []Edit{{[]string{"a", "c"}, "y"}, {[]string{"a", "d", "e"}, 2.5}, {[]string{"b"}, false}},
			want:   "{\n    \"z\": 1,\n    \"a\": {\n        \"b\": [\n            1,\n            2\n        ],\n        \"c\": \"y\",\n        \"d\": {\n            \"e\": 2.5\n        }\n    },\n    \"b\": false\n}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Apply(tc.format, []byte(tc.old), tc.edits)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestApplyRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, format, old string
		edit              Edit
	}{
		{"a line break in a value", "ini", "", Edit{[]string{"A", "k"}, "1\n[Evil]"}},
		{"a NUL", "toml", "", Edit{[]string{"k"}, "a\x00"}},
		{"a section in a key", "ini", "", Edit{[]string{"A", "[B]"}, "1"}},
		{"an = in a key", "ini", "", Edit{[]string{"A", "k=v"}, "1"}},
		{"an INI path of one part", "ini", "", Edit{[]string{"A"}, "1"}},
		{"a type no format has", "json", "", Edit{[]string{"a"}, []string{"x"}}},
		{"a toml list rewritten", "toml", "a = [1]\n", Edit{[]string{"a"}, int64(1)}},
		{"a toml string that never ends", "toml", "a = \"\"\"\nb = 1\n", Edit{[]string{"c"}, int64(1)}},
		{"a yaml map replaced by a value", "yaml", "a:\n  b: 1\n", Edit{[]string{"a"}, int64(1)}},
		{"a yaml path through a value", "yaml", "a: 1\n", Edit{[]string{"a", "b"}, int64(1)}},
		{"a yaml tab", "yaml", "a:\n\tb: 1\n", Edit{[]string{"a", "b"}, int64(1)}},
		{"an xml element with children", "xml", "<r><a><b/></a></r>", Edit{[]string{"r", "a"}, "1"}},
		{"an xml root of another name", "xml", "<r/>", Edit{[]string{"s", "a"}, "1"}},
		{"an xml name that isn't one", "xml", "<r/>", Edit{[]string{"r", "a b"}, "1"}},
		{"a json path through a value", "json", `{"a": 1}`, Edit{[]string{"a", "b"}, "1"}},
		{"a json array", "json", `[1]`, Edit{[]string{"a"}, "1"}},
		{"an unknown format", "csv", "", Edit{[]string{"a"}, "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if out, err := Apply(tc.format, []byte(tc.old), []Edit{tc.edit}); err == nil {
				t.Errorf("accepted:\n%s", out)
			}
		})
	}
	big := bytes.Repeat([]byte("a"), MaxSize+1)
	for _, f := range Formats {
		if _, err := Apply(f, big, nil); err == nil {
			t.Errorf("%s: a file over the limit was edited", f)
		}
		if _, err := Entries(f, big); err == nil {
			t.Errorf("%s: a file over the limit was read", f)
		}
	}
}

func TestEntries(t *testing.T) {
	for _, tc := range []struct {
		format, data string
		want         []Entry
	}{
		{"ini", "top = 1\n[A]\nk = v \n; c = d\n", []Entry{{[]string{"A", "k"}, "v"}}},
		{"toml", "a = 1\nb = 2.5\nc = true\nd = 'x'\ne = [1, 2]\n[t.u]\n\"q k\".r = \"s\\tt\"\n", []Entry{
			{[]string{"a"}, int64(1)}, {[]string{"b"}, 2.5}, {[]string{"c"}, true}, {[]string{"d"}, "x"}, {[]string{"e"}, "[1, 2]"}, {[]string{"t", "u", "q k", "r"}, "s\tt"},
		}},
		{"yaml", "A:\n  B (x): 1\n  C: 'it''s'\n  D: \"a\\tb\"\n  E: no\n  F: []\n  G: ~\n", []Entry{
			{[]string{"A", "B (x)"}, int64(1)}, {[]string{"A", "C"}, "it's"}, {[]string{"A", "D"}, "a\tb"}, {[]string{"A", "E"}, "no"}, {[]string{"A", "F"}, "[]"},
		}},
		{"xml", "<r><a>1</a><b><c>x&amp;y</c></b></r>", []Entry{{[]string{"r", "a"}, "1"}, {[]string{"r", "b", "c"}, "x&y"}}},
		{"json", `{"a": 1, "b": {"c": 1.5, "d": null, "e": [1]}}`, []Entry{{[]string{"a"}, int64(1)}, {[]string{"b", "c"}, 1.5}, {[]string{"b", "e"}, "[1]"}}},
	} {
		got, err := Entries(tc.format, []byte(tc.data))
		if err != nil {
			t.Errorf("%s: %v", tc.format, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v\nwant %#v", tc.format, got, tc.want)
		}
	}
}

// fuzzApply is the property every format's fuzz test holds: hostile input never panics, and
// whatever Apply accepts reads back with the value it wrote.
func fuzzApply(t *testing.T, format string, old []byte, path []string, value any) {
	out, err := Apply(format, old, []Edit{{Path: path, Value: value}})
	_, _ = Entries(format, old)
	if err != nil {
		return
	}
	got, err := Entries(format, out)
	if err != nil {
		t.Fatalf("the edited file doesn't read: %v\n%q", err, out)
	}
	for _, e := range got {
		if samePath(e.Path, path) {
			if !SameValue(format, e.Value, value) {
				t.Fatalf("read %#v, wrote %#v\n%q", e.Value, value, out)
			}
			return
		}
	}
	t.Fatalf("the edit isn't there\n%q", out)
}

// fuzzValue picks a value of each type from the fuzzer's inputs.
func fuzzValue(kind uint8, s string, n int64, f float64) any {
	switch kind % 4 {
	case 0:
		return s
	case 1:
		return n
	case 2:
		return f
	}
	return n%2 == 0
}

func fuzzPath(p string) []string {
	return strings.Split(p, "/")
}

func FuzzINI(f *testing.F) {
	f.Add([]byte("[A]\nk = 1\n"), "A/k", uint8(0), "v", int64(1), 1.5)
	f.Add([]byte("k=v\n[B\n[A]]\n"), "A]/k", uint8(1), "", int64(-1), 0.0)
	f.Fuzz(func(t *testing.T, old []byte, path string, kind uint8, s string, n int64, fl float64) {
		fuzzApply(t, "ini", old, fuzzPath(path), fuzzValue(kind, s, n, fl))
	})
}

func FuzzTOML(f *testing.F) {
	f.Add([]byte("a = 1\n[t]\nb = \"x\" # c\n"), "t/b", uint8(0), "v", int64(1), 1.5)
	f.Add([]byte("[a]\nb.c = [1,\n2]\n[[d]]\ne = 1\n"), "a/b/d", uint8(2), "", int64(0), 2.0)
	f.Fuzz(func(t *testing.T, old []byte, path string, kind uint8, s string, n int64, fl float64) {
		fuzzApply(t, "toml", old, fuzzPath(path), fuzzValue(kind, s, n, fl))
	})
}

func FuzzYAML(f *testing.F) {
	f.Add([]byte("A:\n  B C: 1\n  L:\n    - x\nD: |\n  text\n"), "A/B C", uint8(0), "yes", int64(1), 1.5)
	f.Add([]byte("---\n'a': {b: 1}\n"), "a/b", uint8(3), "", int64(0), 0.0)
	f.Fuzz(func(t *testing.T, old []byte, path string, kind uint8, s string, n int64, fl float64) {
		fuzzApply(t, "yaml", old, fuzzPath(path), fuzzValue(kind, s, n, fl))
	})
}

func FuzzXML(f *testing.F) {
	f.Add([]byte("<?xml version=\"1.0\"?>\n<content>\n  <a>1</a>\n  <b/>\n</content>\n"), "content/b", uint8(0), "x<y", int64(1), 1.5)
	f.Add([]byte("<r><!-- c --><a x='1'/></r>"), "r/a/b", uint8(1), "", int64(0), 0.0)
	f.Fuzz(func(t *testing.T, old []byte, path string, kind uint8, s string, n int64, fl float64) {
		fuzzApply(t, "xml", old, fuzzPath(path), fuzzValue(kind, s, n, fl))
	})
}

func FuzzJSON(f *testing.F) {
	f.Add([]byte(`{"a": {"b": 1}, "c": [1]}`), "a/b", uint8(0), "x", int64(1), 1.5)
	f.Add([]byte(`{"a": 1e400}`), "a", uint8(2), "", int64(0), 1e300)
	f.Fuzz(func(t *testing.T, old []byte, path string, kind uint8, s string, n int64, fl float64) {
		fuzzApply(t, "json", old, fuzzPath(path), fuzzValue(kind, s, n, fl))
	})
}
