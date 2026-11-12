// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package inputmap

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/emulators/configfile"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// defaults is the agent's own default key map, so a change there that this package can't map
// fails here.
func defaults() KeyMap { return KeyMap(inputagent.DefaultKeys()) }

func one(p int, button, key string) KeyMap {
	var km KeyMap
	for i := range km {
		km[i] = map[string]string{}
	}
	km[p][button] = key
	return km
}

func TestDefaultKeysMapEverywhere(t *testing.T) {
	for name, e := range emulators {
		for _, goos := range e.oses {
			t.Run(name+"/"+goos, func(t *testing.T) {
				if _, err := Targets(name, goos, defaults()); err != nil {
					t.Fatal(err)
				}
				// Each button of each player must add something of its own to what's written.
				base, err := Targets(name, goos, one(0, "b", ""))
				if err != nil {
					t.Fatal(err)
				}
				for p := range e.players {
					for _, b := range e.buttons {
						key := defaults()[p][b]
						if key == "" {
							continue // the defaults give players 3 and 4 no right stick
						}
						got, err := Targets(name, goos, one(p, b, key))
						if err != nil {
							t.Fatal(err)
						}
						if bindings(got) <= bindings(base) {
							t.Errorf("player %d's %s (%s) isn't bound", p+1, b, key)
						}
					}
				}
			})
		}
	}
}

// bindings counts what a set of targets writes, a whole file by its lines and a list or an
// object by its values. A value that binds nothing (Lichii's "[empty]", Ryujinx's "Unbound")
// doesn't count.
func bindings(ts []Target) int {
	n := 0
	for _, t := range ts {
		if t.Path == nil {
			n += strings.Count(t.Value.(string), "<entry>")
		} else {
			n += values(t.Value)
		}
	}
	return n
}

func values(v any) int {
	switch t := v.(type) {
	case []any:
		n := 0
		for _, x := range t {
			n += values(x)
		}
		return n
	case map[string]any:
		n := 0
		for _, x := range t {
			n += values(x)
		}
		return n
	case string:
		if t == "" || t == "[empty]" || t == "Unbound" {
			return 0
		}
	}
	return 1
}

func TestPlayers(t *testing.T) {
	want := map[string]int{"pcsx2": 4, "dolphin": 4, "flycast": 4, "rpcs3": 2, "ppsspp": 1, "melonds": 1, "xemu": 1, "cemu": 1, "duckstation": 0,
		"ryujinx": 3, "lichii": 2}
	for emu, n := range want {
		if got := Players(emu); got != n {
			t.Errorf("Players(%q) = %d, want %d", emu, got, n)
		}
		if Supported(emu) != (n > 0) {
			t.Errorf("Supported(%q) = %v", emu, Supported(emu))
		}
	}
}

func TestRefusals(t *testing.T) {
	dup := one(0, "b", "z")
	dup[1]["a"] = "z"
	// On Linux RPCS3 reads KP_2 as 2 (it drops the keypad modifier); on Windows it doesn't.
	kp := one(0, "b", "KP_2")
	kp[1]["b"] = "2"
	for _, c := range []struct {
		emu, goos string
		keys      KeyMap
		want      string
	}{
		{"rpcs3", "darwin", defaults(), "weren't verified"},
		{"nonesuch", "linux", defaults(), "no key mapping"},
		{"pcsx2", "linux", one(0, "b", "exclam"), "can be mapped"},
		{"pcsx2", "plan9", defaults(), "weren't verified"},
		{"flycast", "linux", dup, "same key"},
		{"rpcs3", "linux", kp, "same key"},
		// Lichii drops the keypad modifier on every OS.
		{"lichii", "windows", kp, "same key"},
		// Avalonia's Enter and Return are one key, so the keypad's Enter is Return to Ryujinx.
		{"ryujinx", "linux", func() KeyMap { k := one(0, "start", "Return"); k[0]["select"] = "KP_Enter"; return k }(), "same key"},
		// PPSSPP's Windows frontend sees the keypad's Enter as Enter.
		{"ppsspp", "windows", func() KeyMap { k := one(0, "start", "Return"); k[0]["select"] = "KP_Enter"; return k }(), "same key"},
	} {
		_, err := Targets(c.emu, c.goos, c.keys)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s on %s: err = %v, want %q", c.emu, c.goos, err, c.want)
		}
	}
	if _, err := Targets("rpcs3", "windows", kp); err != nil {
		t.Errorf("rpcs3 on windows: %v", err)
	}
}

// Values checked against each emulator's source; the files are cited in inputmap.go.
func TestSpotChecks(t *testing.T) {
	for _, c := range []struct {
		emu, goos string
		want      Target
	}{
		// pcsx2-qt/QtKeyCodes.cpp: Qt::Key_Z is "Z"; keypad keys take "Numpad".
		{"pcsx2", "linux", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad1", "Cross"}, "Keyboard/Z"}},
		{"pcsx2", "windows", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad2", "Cross"}, "Keyboard/Numpad2"}},
		{"pcsx2", "linux", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad2", "Select"}, "Keyboard/NumpadMinus"}},
		{"pcsx2", "linux", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad2", "Type"}, "DualShock2"}},
		{"pcsx2", "darwin", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad2", "RLeft"}, "Keyboard/Help"}},
		{"pcsx2", "darwin", Target{"PCSX2/inis/PCSX2.ini", "ini", []string{"Pad1", "Up"}, "Keyboard/Up"}},
		// XInput2.cpp names a key by its level-0 keysym: the keypad's 2 is KP_Down.
		{"dolphin", "linux", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad2", "Buttons/A"}, "`KP_Down`"}},
		{"dolphin", "linux", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "Device"}, "XInput2/0/Virtual core pointer"}},
		{"dolphin", "linux", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "Buttons/A"}, "`Z`"}},
		{"dolphin", "linux", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad2", "D-Pad/Right"}, "`Next`"}},
		{"dolphin", "darwin", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "D-Pad/Up"}, "`Up Arrow`"}},
		{"dolphin", "darwin", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "Device"}, "Quartz/0/Keyboard & Mouse"}},
		{"dolphin", "darwin", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad2", "C-Stick/Left"}, "`Key 114`"}},
		{"dolphin", "windows", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "Buttons/Start"}, "`RETURN`"}},
		{"dolphin", "windows", Target{"Config/GCPadNew.ini", "ini", []string{"GCPad1", "Device"}, "DInput/0/Keyboard Mouse"}},
		{"dolphin", "windows", Target{"Config/WiimoteNew.ini", "ini", []string{"Wiimote1", "Nunchuk/Buttons/C"}, "`Q`"}},
		{"dolphin", "linux", Target{"Config/WiimoteNew.ini", "ini", []string{"Wiimote3", "Source"}, "1"}},
		{"dolphin", "linux", Target{"Config/WiimoteNew.ini", "ini", []string{"Wiimote1", "Options/Sideways Wiimote"}, "True"}},
		{"dolphin", "linux", Target{"Config/Dolphin.ini", "ini", []string{"Core", "SIDevice3"}, "6"}},
		// KeyCodes.h: NKCODE_Z is 54, NKCODE_ENTER 66; device 1 is the keyboard.
		{"ppsspp", "linux", Target{"PSP/SYSTEM/controls.ini", "ini", []string{"ControlMapping", "Cross"}, "1-54"}},
		{"ppsspp", "windows", Target{"PSP/SYSTEM/controls.ini", "ini", []string{"ControlMapping", "Start"}, "1-66"}},
		{"ppsspp", "darwin", Target{"PSP/SYSTEM/controls.ini", "ini", []string{"ControlMapping", "An.Up"}, "1-37"}},
		// Qt::Key_Return; on macOS the arrows carry Qt::KeypadModifier.
		{"melonds", "linux", Target{"melonDS.toml", "toml", []string{"Instance0", "Keyboard", "Start"}, int64(0x01000004)}},
		{"melonds", "darwin", Target{"melonDS.toml", "toml", []string{"Instance0", "Keyboard", "Up"}, int64(0x21000013)}},
		{"melonds", "windows", Target{"melonDS.toml", "toml", []string{"Instance0", "Keyboard", "B"}, int64('Z')}},
		// SDL_SCANCODE_Z is 29, SDL_SCANCODE_KP_2 90.
		{"flycast", "linux", Target{"mappings/SDL_Keyboard.cfg", "ini", []string{"digital", "bind0"}, "29:btn_a"}},
		{"flycast", "windows", Target{"mappings/SDL_Keyboard_arcade.cfg", "ini", []string{"digital", "bind16"}, "90:btn_a1"}},
		{"flycast", "darwin", Target{"emu.cfg", "ini", []string{"input", "maple_sdl_keyboard"}, "4"}},
		{"flycast", "darwin", Target{"emu.cfg", "ini", []string{"input", "device4"}, "0"}},
		{"xemu", "linux", Target{"xemu.toml", "toml", []string{"input", "keyboard_controller_scancode_map", "a"}, int64(29)}},
		{"xemu", "darwin", Target{"xemu.toml", "toml", []string{"input", "keyboard_controller_scancode_map", "back"}, int64(42)}},
		// keyboard_pad_handler.cpp: QKeySequence text, "Num+" for the keypad.
		{"rpcs3", "linux", Target{"input_configs/global/Default.yml", "yaml", []string{"Player 1 Input", "Config", "Cross"}, "Z"}},
		{"rpcs3", "linux", Target{"input_configs/global/Default.yml", "yaml", []string{"Player 2 Input", "Config", "Cross"}, "Num+2"}},
		{"rpcs3", "linux", Target{"input_configs/global/Default.yml", "yaml", []string{"Player 2 Input", "Config", "Right"}, "PgDown"}},
		{"rpcs3", "windows", Target{"input_configs/global/Default.yml", "yaml", []string{"Player 2 Input", "Config", "R3"}, "Num+,"}},
		{"rpcs3", "windows", Target{"input_configs/global/Default.yml", "yaml", []string{"Player 2 Input", "Handler"}, "Keyboard"}},
	} {
		got, err := Targets(c.emu, c.goos, defaults())
		if err != nil {
			t.Fatalf("%s on %s: %v", c.emu, c.goos, err)
		}
		if !slices.ContainsFunc(got, func(g Target) bool { return reflect.DeepEqual(g, c.want) }) {
			t.Errorf("%s on %s: no %+v", c.emu, c.goos, c.want)
		}
	}
}

// Ryujinx's players are one list, written whole: every button of every player is bound, as the
// Key enum names it (src/Ryujinx.Common/Configuration/Hid/Key.cs), the rest Unbound.
func TestRyujinxInputConfig(t *testing.T) {
	for _, goos := range allOS {
		got, err := Targets("ryujinx", goos, defaults())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].File != "Config.json" || got[0].Format != "json" || !reflect.DeepEqual(got[0].Path, []string{"input_config"}) {
			t.Fatalf("%s: %+v", goos, got)
		}
		list := got[0].Value.([]any)
		if len(list) != Players("ryujinx") {
			t.Fatalf("%d players", len(list))
		}
		// build itself writes four players when it's given four: the default map less the
		// second player's keypad Enter, which is the first's Return to Ryujinx.
		var four [][]bound
		for p, m := range defaults() {
			var bs []bound
			for _, b := range ryujinx.buttons {
				if k, ok := lookup(m[b]); ok && !(p == 1 && b == "rright") {
					bs = append(bs, bound{b, k})
				}
			}
			four = append(four, bs)
		}
		list = ryujinx.build(goos, four)[0].Value.([]any)
		if len(list) != 4 {
			t.Fatalf("%d players", len(list))
		}
		seen := map[string]bool{}
		for p, e := range list {
			entry := e.(map[string]any)
			for k, want := range map[string]any{"backend": "WindowKeyboard", "id": "0", "controller_type": "ProController",
				"player_index": fmt.Sprint("Player", p+1), "version": int64(1)} {
				if entry[k] != want {
					t.Errorf("player %d: %s is %v, want %v", p+1, k, entry[k], want)
				}
			}
			for b, name := range ryujinxNames {
				g, key, _ := strings.Cut(name, "/")
				v := entry[g].(map[string]any)[key].(string)
				if defaults()[p][b] == "" || (p == 1 && b == "rright") {
					if v != "Unbound" {
						t.Errorf("player %d's %s: %s", p+1, b, v)
					}
					continue
				}
				if v == "Unbound" || v == "" || seen[v] {
					t.Errorf("player %d's %s: %q is unbound or taken", p+1, b, v)
				}
				seen[v] = true
			}
			for _, g := range []string{"left_joycon", "right_joycon"} {
				m := entry[g].(map[string]any)
				if m["button_sl"] != "Unbound" || m["button_sr"] != "Unbound" {
					t.Errorf("player %d: %s's side buttons: %v", p+1, g, m)
				}
			}
		}
		key := func(p int, g, k string) any { return list[p].(map[string]any)[g].(map[string]any)[k] }
		for _, c := range []struct {
			p    int
			g, k string
			want string
		}{
			{0, "right_joycon", "button_b", "Z"},
			{0, "right_joycon", "button_plus", "Enter"},
			{0, "left_joycon", "button_minus", "BackSpace"},
			{0, "left_joycon_stick", "stick_up", "I"},
			{1, "right_joycon", "button_b", "Keypad2"},
			{1, "left_joycon", "button_minus", "KeypadSubtract"},
			{1, "left_joycon", "dpad_right", "PageDown"},
			{1, "left_joycon_stick", "stick_right", "PageUp"},
			{1, "right_joycon_stick", "stick_up", "C"},
			{2, "right_joycon", "button_b", "Number1"},
			{2, "left_joycon_stick", "stick_down", "Plus"},
			{3, "right_joycon", "button_b", "F1"},
		} {
			if got := key(c.p, c.g, c.k); got != c.want {
				t.Errorf("%s: player %d's %s.%s is %v, want %s", goos, c.p+1, c.g, c.k, got, c.want)
			}
		}
		// It goes into a real Config.json and leaves the rest of it alone.
		old := []byte(`{"version": 70, "res_scale": 2, "input_config": [{"backend": "GamepadSDL2", "id": "x"}]}`)
		out, err := configfile.Apply("json", old, []configfile.Edit{{Path: got[0].Path, Value: list}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"res_scale": 2`) || strings.Contains(string(out), "GamepadSDL2") ||
			strings.Count(string(out), `"backend": "WindowKeyboard"`) != 4 {
			t.Errorf("%s:\n%s", goos, out)
		}
	}
	if got := Reserved("ryujinx", "linux", one(0, "start", "F5")); !reflect.DeepEqual(got, []string{"player 1's start (F5)"}) {
		t.Errorf("Reserved = %v", got)
	}
}

// Lichii reads each value only with "\default = false" beside it, and every mapping of each
// player is written so its own defaults (the same keys for every player) don't stay bound.
func TestLichiiControls(t *testing.T) {
	got, err := Targets("lichii", "linux", defaults())
	if err != nil {
		t.Fatal(err)
	}
	const file = "qt-config.ini"
	at := map[string]string{}
	for _, g := range got {
		if g.File != file || g.Format != "ini" || g.Path[0] != "Controls" {
			t.Fatalf("%+v", g)
		}
		at[g.Path[1]] = g.Value.(string)
	}
	for p := range 2 {
		prefix := fmt.Sprint("player_", p, "_")
		for _, m := range append([]string{"connected", "type"}, lichiiMappings...) {
			if _, ok := at[prefix+m]; !ok {
				t.Errorf("no %s%s", prefix, m)
			}
			if at[prefix+m+`\default`] != "false" {
				t.Errorf("%s%s isn't marked as set", prefix, m)
			}
		}
		if at[prefix+"connected"] != "true" || at[prefix+"type"] != "0" {
			t.Errorf("player %d isn't a connected Pro Controller", p+1)
		}
		for _, m := range []string{"button_home", "button_screenshot", "button_slleft", "button_srright", "motionleft", "motionright"} {
			if at[prefix+m] != "[empty]" {
				t.Errorf("%s%s: %s", prefix, m, at[prefix+m])
			}
		}
	}
	if len(got) != 2*2*(2+len(lichiiMappings)) {
		t.Errorf("%d targets", len(got))
	}
	for key, want := range map[string]string{
		// Qt::Key_Z is 0x5A; the keypad's 2 is Qt::Key_2 without its modifier.
		"player_0_button_b":     "engine:keyboard,code:90,toggle:0",
		"player_0_button_plus":  "engine:keyboard,code:16777220,toggle:0",
		"player_1_button_b":     "engine:keyboard,code:50,toggle:0",
		"player_1_button_minus": "engine:keyboard,code:45,toggle:0",
		"player_0_lstick": "engine:analog_from_button,up:engine$0keyboard$1code$073$1toggle$00,down:engine$0keyboard$1code$075$1toggle$00," +
			"left:engine$0keyboard$1code$074$1toggle$00,right:engine$0keyboard$1code$076$1toggle$00," +
			"modifier:engine$0keyboard$1code$00$1toggle$00,modifier_scale:0.500000",
	} {
		if at[key] != want {
			t.Errorf("%s = %q, want %q", key, at[key], want)
		}
	}
	// On macOS the Help key stands where Insert is.
	mac, err := Targets("lichii", "darwin", defaults())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(mac, func(g Target) bool { return strings.Contains(g.Value.(string), "code$016777304$1") }) {
		t.Error("no Qt::Key_Help on macOS")
	}
	// The values go into an INI file as they are.
	edits := make([]configfile.Edit, 0, len(got))
	for _, g := range got {
		edits = append(edits, configfile.Edit{Path: g.Path, Value: g.Value})
	}
	if _, err := configfile.Apply("ini", []byte("[Controls]\nplayer_0_button_a\\default=true\n"), edits); err != nil {
		t.Error(err)
	}
}

func TestCemuProfile(t *testing.T) {
	for _, c := range []struct {
		goos string
		want string // the B button (id 2) on z
	}{
		{"linux", "<mapping>2</mapping>\n\t\t\t\t<button>122</button>"},  // GDK keyval of z
		{"windows", "<mapping>2</mapping>\n\t\t\t\t<button>90</button>"}, // VK_Z
		{"darwin", "<mapping>2</mapping>\n\t\t\t\t<button>6</button>"},   // kVK_ANSI_Z
	} {
		got, err := Targets("cemu", c.goos, defaults())
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Path != nil || got[0].File != "controllerProfiles/controller0.xml" {
			t.Fatalf("%s: %+v", c.goos, got)
		}
		text := got[0].Value.(string)
		for _, w := range []string{c.want, "<type>Wii U GamePad</type>", "<api>Keyboard</api>"} {
			if !strings.Contains(text, w) {
				t.Errorf("%s: no %q in\n%s", c.goos, w, text)
			}
		}
		if n := strings.Count(text, "<entry>"); n != 24 {
			t.Errorf("%s: %d entries, want 24", c.goos, n)
		}
	}
}

func TestFlycastCutsOffOlderBinds(t *testing.T) {
	got, _ := Targets("flycast", "linux", defaults())
	last := Target{"mappings/SDL_Keyboard.cfg", "ini", []string{"digital", fmt.Sprint("bind", 4*16)}, ""}
	if !slices.ContainsFunc(got, func(g Target) bool { return reflect.DeepEqual(g, last) }) {
		t.Errorf("no blank %v after the last binding", last.Path)
	}
}

func TestReserved(t *testing.T) {
	// Flycast's pad has no L, so the defaults' F11 (player 4's l) isn't bound there.
	if got := Reserved("flycast", "linux", defaults()); got != nil {
		t.Errorf("Reserved = %v", got)
	}
	if got := Reserved("flycast", "windows", one(1, "start", "F11")); !reflect.DeepEqual(got, []string{"player 2's start (F11)"}) {
		t.Errorf("Reserved = %v", got)
	}
	if got := Reserved("pcsx2", "linux", defaults()); got != nil {
		t.Errorf("Reserved = %v", got)
	}
}

func TestHostKey(t *testing.T) {
	for _, c := range []struct {
		goos, sym string
		want      int
	}{
		{"darwin", "z", 0x06},      // kVK_ANSI_Z
		{"darwin", "Return", 0x24}, // kVK_Return
		{"darwin", "KP_2", 0x54},   // kVK_ANSI_Keypad2
		{"darwin", "Insert", 0x72}, // kVK_Help
		{"darwin", "F1", 0x7A},     // kVK_F1
		{"windows", "z", 0x5A},     // 'Z'
		{"windows", "KP_Enter", 0x10D},
		{"windows", "Return", 0x0D}, // VK_RETURN
		{"windows", "equal", 0xBB},  // VK_OEM_PLUS
		{"windows", "Next", 0x22},   // VK_NEXT
		{"windows", "Page_Down", 0x22},
		{"linux", "Prior", 0xFF55},
		{"linux", "KP_Decimal", 0xFFAE},
	} {
		if got, ok := HostKey(c.goos, c.sym); !ok || got != c.want {
			t.Errorf("HostKey(%q, %q) = %#x, %v; want %#x", c.goos, c.sym, got, ok, c.want)
		}
	}
	for _, bad := range [][2]string{{"windows", "Z"}, {"darwin", "exclam"}, {"plan9", "z"}} {
		if _, ok := HostKey(bad[0], bad[1]); ok {
			t.Errorf("HostKey(%q, %q) is known", bad[0], bad[1])
		}
	}
	if got, ok := SDLScancode("KP_0"); !ok || got != 98 {
		t.Errorf("SDLScancode(KP_0) = %d", got)
	}
	if got, ok := QtKey("linux", "KP_Enter"); !ok || got != 0x21000005 {
		t.Errorf("QtKey(linux, KP_Enter) = %#x", got)
	}
}

// Every key the agent's defaults use is in the table, and no two keys share a code anywhere an
// injector or emulator reads one, or one key would press another.
func TestTable(t *testing.T) {
	for p, m := range defaults() {
		for b, sym := range m {
			if !slices.Contains(inputagent.Buttons, b) {
				t.Errorf("player %d: %q isn't a button", p+1, b)
			}
			for _, goos := range allOS {
				if _, ok := HostKey(goos, sym); !ok {
					t.Errorf("%s has no %s key", sym, goos)
				}
			}
		}
	}
	codes := map[string]func(k hostKey) any{
		"x11": func(k hostKey) any { return k.sym }, "xname": func(k hostKey) any { return k.xname },
		"mac": func(k hostKey) any { return k.mac }, "vk": func(k hostKey) any { return k.vk },
		"dik": func(k hostKey) any { return k.dik }, "sdl": func(k hostKey) any { return k.sdl },
		"nkSDL": func(k hostKey) any { return k.nkSDL },
		"qt":    func(k hostKey) any { return qtValue("linux", k) }, "qtMac": func(k hostKey) any { return qtValue("darwin", k) },
		"pcsx2": func(k hostKey) any { return pcsx2Key("linux", k) }, "rpcs3": func(k hostKey) any { return rpcs3Key("windows", k) },
	}
	for what, code := range codes {
		seen := map[any]string{}
		for name, k := range keys {
			c := code(k)
			if other, dup := seen[c]; dup {
				t.Errorf("%s: %s and %s are both %v", what, other, name, c)
			}
			seen[c] = name
		}
	}
	if len(keys) != 26+10+12+10+26+6 {
		t.Errorf("%d keys", len(keys))
	}
}
