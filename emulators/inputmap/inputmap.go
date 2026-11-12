// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package inputmap writes the keyboard bindings each standalone emulator needs so that the keys
// WittyPlay injects for a player's pad (see sandbox/docker/inputagent's KeyMap) press that
// player's buttons. Nothing else would: out of the box an emulator binds only its own default
// keys, or none at all.
//
// Every emulator names keys its own way, and often differently on each host OS because it reads
// the OS's own keyboard backend, so each notation here was taken from the emulator's source at
// the version WittyPlay pins (cited at each emulator below). Where a notation couldn't be
// verified for an OS, Targets refuses that OS rather than guess.
//
// Two assumptions every notation shares: the keyboard layout is US, and on X11 the keypad's
// keys arrive as KP_0..KP_9 (NumLock on), the way xdotool's keysyms name them.
package inputmap

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// KeyMap gives each of four players' RetroPad buttons an X keysym name, as inputagent.KeyMap.
type KeyMap [4]map[string]string

// Target is one value to set in an emulator's settings. File is relative to the emulator's
// settings folder, with forward slashes. Format is ini, toml, yaml, xml or json, and Path names
// the value as package configfile does: ini [section, key]; toml [table..., key]; yaml
// [key, ...]; xml [element, ...]; json [key, ...]. Value is a string, an int64 or a bool.
//
// A Target with no Path is a whole file: Value is its complete text, to be written as it is.
// That is for a file that holds nothing but these bindings in a shape configfile can't edit
// (a list of repeated elements).
type Target struct {
	File   string
	Format string
	Path   []string
	Value  any
}

// The supported host OSes, as GOOS names them.
var allOS = []string{"linux", "darwin", "windows"}

type emulator struct {
	players int
	oses    []string // the host OSes whose notation was verified
	// buttons are the RetroPad buttons this emulator's pad has, in the order they're written.
	buttons []string
	// ident is what the emulator compares to tell keys apart on goos: two different buttons with
	// the same ident would be the same key to it.
	ident func(goos string, k hostKey) string
	build func(goos string, bound [][]bound) []Target
}

type bound struct {
	button string
	key    hostKey
}

var emulators = map[string]emulator{
	"pcsx2":   pcsx2,
	"dolphin": dolphin,
	"ppsspp":  ppsspp,
	"melonds": melonds,
	"flycast": flycast,
	"xemu":    xemu,
	"cemu":    cemu,
	"rpcs3":   rpcs3,
}

// Supported says whether this package maps keys for an emulator on at least one host OS.
func Supported(emulator string) bool {
	_, ok := emulators[emulator]
	return ok
}

// SupportedOn says whether the emulator's key notation on goos was verified, so Targets can
// produce bindings for it there.
func SupportedOn(emulator, goos string) bool {
	e, ok := emulators[emulator]
	return ok && slices.Contains(e.oses, goos)
}

// Players is how many players the emulator's keyboard bindings can give keys of their own; 0
// for an emulator this package doesn't know.
func Players(emulator string) int {
	return emulators[emulator].players
}

// Targets is every binding for players 1 to Players(emulator) on host goos. A button a player has
// no key for is left out. It's an error for a key to be one this package can't name, or for two
// buttons to come out as the same key in the emulator's notation, since one would shadow the other.
func Targets(emulator, goos string, keys KeyMap) ([]Target, error) {
	e, ok := emulators[emulator]
	if !ok {
		return nil, fmt.Errorf("inputmap: no key mapping for the emulator %q", emulator)
	}
	if !slices.Contains(e.oses, goos) {
		return nil, fmt.Errorf("inputmap: %s's key names on %s weren't verified, so they aren't written", emulator, goos)
	}
	seen := map[string]string{}
	all := make([][]bound, e.players)
	for p := range e.players {
		for _, b := range e.buttons {
			sym := keys[p][b]
			if sym == "" {
				continue
			}
			k, ok := lookup(sym)
			if !ok {
				return nil, fmt.Errorf("inputmap: player %d's %s key %q isn't one that can be mapped", p+1, b, sym)
			}
			id := e.ident(goos, k)
			who := fmt.Sprintf("player %d's %s", p+1, b)
			if other, dup := seen[id]; dup {
				return nil, fmt.Errorf("inputmap: %s and %s are the same key to %s", other, who, emulator)
			}
			seen[id] = who
			all[p] = append(all[p], bound{b, k})
		}
	}
	return e.build(goos, all), nil
}

// Reserved lists the keys in keys, for the players Targets maps, that the emulator takes for
// itself on goos before a game sees them, so binding them does nothing.
func Reserved(emulator, goos string, keys KeyMap) []string {
	var taken []string
	switch emulator {
	case "flycast":
		// core/sdl/sdl.cpp (v2.7): F11 with no modifier toggles full screen and isn't passed on.
		taken = []string{"F11"}
	}
	var out []string
	for p := range Players(emulator) {
		for _, b := range emulators[emulator].buttons {
			if slices.Contains(taken, keys[p][b]) {
				out = append(out, fmt.Sprintf("player %d's %s (%s)", p+1, b, keys[p][b]))
			}
		}
	}
	return out
}

// names gives each RetroPad button the emulator's name for it, so build can range over bound.
type names map[string]string

func (n names) of(b string) string { return n[b] }

func keysOf(n names) []string {
	order := []string{"b", "y", "select", "start", "up", "down", "left", "right", "a", "x", "l", "r", "l2", "r2", "l3", "r3",
		"lup", "ldown", "lleft", "lright", "rup", "rdown", "rleft", "rright"}
	var out []string
	for _, b := range order {
		if _, ok := n[b]; ok {
			out = append(out, b)
		}
	}
	return out
}

func ini(file, section, key string, v any) Target {
	return Target{File: file, Format: "ini", Path: []string{section, key}, Value: v}
}

// ---------------------------------------------------------------- PCSX2

// PCSX2 (v2.8.2) reads [Pad1]..[Pad8] of inis/PCSX2.ini (pcsx2/SIO/Pad/Pad.cpp GetConfigSection);
// a binding is "Keyboard/<name>" (pcsx2/Input/InputManager.cpp), the name from the Qt key table
// in pcsx2-qt/QtKeyCodes.cpp, prefixed "Numpad" when Qt reports the keypad modifier. Its Qt
// frontend reads keys the same way on every OS; on macOS it drops the keypad modifier Qt puts on
// the arrows and Home/End/PgUp/PgDn (QtUtils::KeyEventToCode), so those read as plain keys there.
//
// Pads past 1 default to NotConnected, so each gets Type = DualShock2. Players 1 and 2 are the
// two ports; Pad3 and Pad4 are multitap slots 1-B and 1-C (pcsx2/SIO/Sio.cpp
// sioConvertPortAndSlotToPad), which only work with [Pad] MultitapPort1 = true. That is left to
// the game's settings: some games misbehave with a multitap plugged in.
var pcsx2Names = names{"b": "Cross", "a": "Circle", "y": "Square", "x": "Triangle", "l": "L1", "r": "R1", "l2": "L2", "r2": "R2",
	"l3": "L3", "r3": "R3", "select": "Select", "start": "Start", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"lup": "LUp", "ldown": "LDown", "lleft": "LLeft", "lright": "LRight", "rup": "RUp", "rdown": "RDown", "rleft": "RLeft", "rright": "RRight"}

var pcsx2 = emulator{
	players: 4,
	oses:    allOS,
	buttons: keysOf(pcsx2Names),
	ident:   func(goos string, k hostKey) string { return pcsx2Key(goos, k) },
	build: func(goos string, all [][]bound) []Target {
		const file = "PCSX2/inis/PCSX2.ini"
		var out []Target
		for p, bs := range all {
			sec := "Pad" + strconv.Itoa(p+1)
			out = append(out, ini(file, sec, "Type", "DualShock2"))
			for _, b := range bs {
				out = append(out, ini(file, sec, pcsx2Names.of(b.button), pcsx2Key(goos, b.key)))
			}
		}
		return out
	},
}

func pcsx2Key(goos string, k hostKey) string {
	name := k.qtName
	if goos == "darwin" && k.qt == qtInsert {
		name = "Help"
	}
	if k.pad {
		name = "Numpad" + name
	}
	return "Keyboard/" + name
}

// ---------------------------------------------------------------- Dolphin

// Dolphin (2609b) reads GameCube pads from Config/GCPadNew.ini [GCPad1]..[GCPad4] and emulated
// Wii Remotes from Config/WiimoteNew.ini [Wiimote1]..[Wiimote4]. Each section's Device names the
// keyboard: "<source>/0/<device name>" where the source is the OS's backend (ControllerInterface
// .cpp): XInput2 on Linux, whose device is the X server's master pointer, "Virtual core pointer"
// (Xlib/XInput2.cpp); Quartz on macOS, "Keyboard & Mouse" (Quartz/QuartzKeyboardAndMouse.mm);
// DInput on Windows, "Keyboard Mouse" (DInput/DInputKeyboardMouse.cpp). A control is
// "<group>/<name> = <expression>" (ControllerEmu/ControlGroup/ControlGroup.cpp) and an
// expression names a key between backticks (ControlReference/ExpressionParser.cpp).
//
// A Wii game gets a sideways Wii Remote with a Nunchuk, the pad layout controls.go gives "wii":
// "Options/Sideways Wiimote" turns the D-pad to suit (WiimoteEmu.cpp), Extension = Nunchuk
// selects the attachment, whose controls are "Nunchuk/<group>/<name>"
// (ControlGroup/Attachments.cpp). Remotes 2-4 default to Source = 0, none (Config/
// WiimoteSettings.cpp), and GameCube ports 2-4 to SIDevice = 0 (Config/MainSettings.cpp), so both
// are turned on: Source 1 is emulated (WiimoteCommon), SIDevice 6 a standard controller
// (HW/SI/SI_Device.h).
var dolphinGC = names{"b": "Buttons/A", "y": "Buttons/B", "a": "Buttons/X", "x": "Buttons/Y", "r": "Buttons/Z", "start": "Buttons/Start",
	"l2": "Triggers/L", "r2": "Triggers/R", "up": "D-Pad/Up", "down": "D-Pad/Down", "left": "D-Pad/Left", "right": "D-Pad/Right",
	"lup": "Main Stick/Up", "ldown": "Main Stick/Down", "lleft": "Main Stick/Left", "lright": "Main Stick/Right",
	"rup": "C-Stick/Up", "rdown": "C-Stick/Down", "rleft": "C-Stick/Left", "rright": "C-Stick/Right"}

var dolphinWii = names{"y": "Buttons/1", "x": "Buttons/2", "b": "Buttons/A", "a": "Buttons/B", "select": "Buttons/-", "start": "Buttons/+",
	"l": "Nunchuk/Buttons/C", "r": "Nunchuk/Buttons/Z", "up": "D-Pad/Up", "down": "D-Pad/Down", "left": "D-Pad/Left", "right": "D-Pad/Right",
	"lup": "Nunchuk/Stick/Up", "ldown": "Nunchuk/Stick/Down", "lleft": "Nunchuk/Stick/Left", "lright": "Nunchuk/Stick/Right"}

var dolphin = emulator{
	players: 4,
	oses:    allOS,
	buttons: union(keysOf(dolphinGC), keysOf(dolphinWii)),
	ident:   func(goos string, k hostKey) string { return dolphinKey(goos, k) },
	build: func(goos string, all [][]bound) []Target {
		device := map[string]string{"linux": "XInput2/0/Virtual core pointer", "darwin": "Quartz/0/Keyboard & Mouse",
			"windows": "DInput/0/Keyboard Mouse"}[goos]
		const gc, wii, main = "Config/GCPadNew.ini", "Config/WiimoteNew.ini", "Config/Dolphin.ini"
		var out []Target
		for p, bs := range all {
			n := strconv.Itoa(p + 1)
			out = append(out, ini(gc, "GCPad"+n, "Device", device))
			if p > 0 {
				out = append(out, ini(main, "Core", "SIDevice"+strconv.Itoa(p), "6"))
			}
			for _, b := range bs {
				if name, ok := dolphinGC[b.button]; ok {
					out = append(out, ini(gc, "GCPad"+n, name, "`"+dolphinKey(goos, b.key)+"`"))
				}
			}
			sec := "Wiimote" + n
			out = append(out, ini(wii, sec, "Device", device), ini(wii, sec, "Source", "1"),
				ini(wii, sec, "Extension", "Nunchuk"), ini(wii, sec, "Options/Sideways Wiimote", "True"))
			for _, b := range bs {
				if name, ok := dolphinWii[b.button]; ok {
					out = append(out, ini(wii, sec, name, "`"+dolphinKey(goos, b.key)+"`"))
				}
			}
		}
		return out
	},
}

func dolphinKey(goos string, k hostKey) string {
	switch goos {
	case "linux":
		return k.xname
	case "darwin":
		if k.macName == "" {
			return "Key " + strconv.Itoa(k.mac)
		}
		return k.macName
	}
	return k.dik
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------- PPSSPP

// PPSSPP (v1.20.4) reads PSP/SYSTEM/controls.ini [ControlMapping], "<button> = <device>-<code>"
// (Core/KeyMap.cpp psp_button_names, Common/Input/InputState.cpp InputMapping::ToConfigString),
// device 1 being the keyboard (DEVICE_ID_KEYBOARD, Common/Input/InputState.h) and the code an
// Android-style NKCODE (Common/Input/KeyCodes.h). The Linux AppImage and the macOS build are its
// SDL frontend, which translates SDL keycodes (SDL/NKCodeFromSDL.h); the Windows build reads raw
// input virtual keys (Windows/RawInput.cpp windowsTransTable), where = is NKCODE_PLUS and the
// keypad's Enter is plain Enter. A PSP is one player, so PPSSPP has one keyboard player.
//
// Loading controls.ini clears every mapping it doesn't list, so a file holding only these loses
// PPSSPP's own default hotkeys (Escape for its menu among them).
var ppssppNames = names{"b": "Cross", "a": "Circle", "y": "Square", "x": "Triangle", "l": "L", "r": "R", "select": "Select", "start": "Start",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right", "lup": "An.Up", "ldown": "An.Down", "lleft": "An.Left", "lright": "An.Right"}

var ppsspp = emulator{
	players: 1,
	oses:    allOS,
	buttons: keysOf(ppssppNames),
	ident:   func(goos string, k hostKey) string { return ppssppKey(goos, k) },
	build: func(goos string, all [][]bound) []Target {
		var out []Target
		for _, b := range all[0] {
			out = append(out, ini("PSP/SYSTEM/controls.ini", "ControlMapping", ppssppNames.of(b.button), ppssppKey(goos, b.key)))
		}
		return out
	},
}

func ppssppKey(goos string, k hostKey) string {
	nk := k.nkSDL
	if goos == "windows" {
		nk = k.nkWin
	}
	return "1-" + strconv.Itoa(nk)
}

// ---------------------------------------------------------------- melonDS

// melonDS (1.1) reads [Instance0.Keyboard] of melonDS.toml, each button a Qt key code
// (src/frontend/qt_sdl/Config.cpp, EmuInstanceInput.cpp buttonNames). It compares
// QKeyEvent::key() with the modifiers kept only when they are exactly Qt::KeypadModifier
// (EmuInstance::onKeyPress), which is also how its settings dialog records a key
// (InputConfig/MapButton.h). Every key is unbound by default. One DS, one player.
var melondsNames = names{"b": "B", "a": "A", "y": "Y", "x": "X", "l": "L", "r": "R", "select": "Select", "start": "Start",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right"}

var melonds = emulator{
	players: 1,
	oses:    allOS,
	buttons: keysOf(melondsNames),
	ident:   func(goos string, k hostKey) string { return strconv.Itoa(qtValue(goos, k)) },
	build: func(goos string, all [][]bound) []Target {
		var out []Target
		for _, b := range all[0] {
			out = append(out, Target{File: "melonDS.toml", Format: "toml", Path: []string{"Instance0", "Keyboard", melondsNames.of(b.button)},
				Value: int64(qtValue(goos, b.key))})
		}
		return out
	},
}

func qtValue(goos string, k hostKey) int {
	q := k.qtFor(goos)
	if k.padFor(goos) {
		q |= qtKeypadModifier
	}
	return q
}

// ---------------------------------------------------------------- Flycast

// Flycast (v2.7) reads keys through SDL on every OS (core/sdl/sdl.cpp; on Windows unless its
// RawInput option is on, which is off by default) as SDL scancodes. Its keyboard's bindings live
// in mappings/SDL_Keyboard.cfg, named "<api>_<device name>" (core/input/gamepad_device.cpp
// make_mapping_filename; the device name is the English "Keyboard"), with "_arcade" for NAOMI
// games. Bindings are [digital] bind0, bind1, ... = "<scancode>:<button>", the button suffixed
// 1-3 for ports 2-4, read until the first missing one (core/input/mapping.cpp InputMapping::load),
// so a blank one is written after the last to cut off any older ones.
//
// The keyboard drives all four ports when emu.cfg's [input] maple_sdl_keyboard is 4
// (GamepadDevice::Register and gamepad_btn_input), and ports 2-4 hold no controller unless
// [input] device2..device4 is 0, MDT_SegaController (core/cfg/option.cpp, core/hw/maple/
// maple_cfg.h). Select is NAOMI's coin (btn_d, keyboard_device.h); a Dreamcast pad has none.
var flycastNames = names{"b": "btn_a", "a": "btn_b", "y": "btn_x", "x": "btn_y", "start": "btn_start", "select": "btn_d",
	"l2": "btn_trigger_left", "r2": "btn_trigger_right", "up": "btn_dpad1_up", "down": "btn_dpad1_down", "left": "btn_dpad1_left",
	"right": "btn_dpad1_right", "lup": "btn_analog_up", "ldown": "btn_analog_down", "lleft": "btn_analog_left", "lright": "btn_analog_right"}

var flycast = emulator{
	players: 4,
	oses:    allOS,
	buttons: keysOf(flycastNames),
	ident:   func(_ string, k hostKey) string { return strconv.Itoa(k.sdl) },
	build: func(_ string, all [][]bound) []Target {
		var binds []string
		for p, bs := range all {
			for _, b := range bs {
				name := flycastNames.of(b.button)
				if p > 0 {
					name += strconv.Itoa(p)
				}
				binds = append(binds, strconv.Itoa(b.key.sdl)+":"+name)
			}
		}
		var out []Target
		for _, file := range []string{"mappings/SDL_Keyboard.cfg", "mappings/SDL_Keyboard_arcade.cfg"} {
			out = append(out, ini(file, "emulator", "mapping_name", "Keyboard"), ini(file, "emulator", "version", "3"))
			for i, s := range binds {
				out = append(out, ini(file, "digital", "bind"+strconv.Itoa(i), s))
			}
			out = append(out, ini(file, "digital", "bind"+strconv.Itoa(len(binds)), ""))
		}
		out = append(out, ini("emu.cfg", "input", "maple_sdl_keyboard", "4"))
		for p := 1; p < len(all); p++ {
			out = append(out, ini("emu.cfg", "input", "device"+strconv.Itoa(p+1), "0"))
		}
		return out
	},
}

// ---------------------------------------------------------------- xemu

// xemu (v0.8.139) has one keyboard controller, read with SDL_GetKeyboardState through
// [input.keyboard_controller_scancode_map] of xemu.toml, each button an SDL scancode
// (config_spec.yml, ui/xemu-input.c). So one player on every OS.
var xemuNames = names{"b": "a", "a": "b", "y": "x", "x": "y", "l": "white", "r": "black", "l2": "ltrigger", "r2": "rtrigger",
	"select": "back", "start": "start", "l3": "lstick_btn", "r3": "rstick_btn", "up": "dpad_up", "down": "dpad_down",
	"left": "dpad_left", "right": "dpad_right", "lup": "lstick_up", "ldown": "lstick_down", "lleft": "lstick_left",
	"lright": "lstick_right", "rup": "rstick_up", "rdown": "rstick_down", "rleft": "rstick_left", "rright": "rstick_right"}

var xemu = emulator{
	players: 1,
	oses:    allOS,
	buttons: keysOf(xemuNames),
	ident:   func(_ string, k hostKey) string { return strconv.Itoa(k.sdl) },
	build: func(_ string, all [][]bound) []Target {
		var out []Target
		for _, b := range all[0] {
			out = append(out, Target{File: "xemu.toml", Format: "toml",
				Path: []string{"input", "keyboard_controller_scancode_map", xemuNames.of(b.button)}, Value: int64(b.key.sdl)})
		}
		return out
	},
}

// ---------------------------------------------------------------- Cemu

// Cemu (v2.6) reads controllerProfiles/controller0.xml for the first emulated controller
// (src/input/InputManager.cpp): <emulated_controller><type>Wii U GamePad</type> with a
// <controller> whose <api> is Keyboard, and <mappings> of <entry><mapping>button id</mapping>
// <button>key code</button></entry>, the ids being VPADController::ButtonId
// (src/input/emulated/VPADController.h). The key code is wxKeyEvent::GetRawKeyCode
// (src/gui/CemuApp.cpp FilterEvent): the virtual-key code on Windows, the GDK keyval (the X
// keysym) on Linux, the kVK code on macOS (wxWidgets src/msw/window.cpp, src/gtk/window.cpp,
// src/osx/cocoa/window.mm). Only the GamePad is mapped: other players would need Pro Controllers,
// which not every game takes, so this is one player. The profile is a list, so it's written whole.
var cemuIDs = map[string]int{"a": 1, "b": 2, "x": 3, "y": 4, "l": 5, "r": 6, "l2": 7, "r2": 8, "start": 9, "select": 10,
	"up": 11, "down": 12, "left": 13, "right": 14, "l3": 15, "r3": 16, "lup": 17, "ldown": 18, "lleft": 19, "lright": 20,
	"rup": 21, "rdown": 22, "rleft": 23, "rright": 24}

var cemu = emulator{
	players: 1,
	oses:    allOS,
	buttons: keysOf(namesFromIDs(cemuIDs)),
	ident:   func(goos string, k hostKey) string { return strconv.Itoa(cemuKey(goos, k)) },
	build: func(goos string, all [][]bound) []Target {
		var sb strings.Builder
		sb.WriteString("<?xml version=\"1.0\"?>\n<emulated_controller>\n\t<type>Wii U GamePad</type>\n\t<controller>\n" +
			"\t\t<api>Keyboard</api>\n\t\t<uuid>keyboard</uuid>\n\t\t<display_name>Keyboard</display_name>\n\t\t<mappings>\n")
		for _, b := range all[0] {
			fmt.Fprintf(&sb, "\t\t\t<entry>\n\t\t\t\t<mapping>%d</mapping>\n\t\t\t\t<button>%d</button>\n\t\t\t</entry>\n",
				cemuIDs[b.button], cemuKey(goos, b.key))
		}
		sb.WriteString("\t\t</mappings>\n\t</controller>\n</emulated_controller>\n")
		return []Target{{File: "controllerProfiles/controller0.xml", Format: "xml", Value: sb.String()}}
	},
}

func cemuKey(goos string, k hostKey) int {
	switch goos {
	case "linux":
		return k.sym
	case "darwin":
		return k.mac
	}
	// wParam carries no extended bit: the keypad's Enter is VK_RETURN here.
	return k.vk & 0xFF
}

func namesFromIDs(ids map[string]int) names {
	n := names{}
	for b := range ids {
		n[b] = b
	}
	return n
}

// ---------------------------------------------------------------- RPCS3

// RPCS3 (v0.0.43) reads input_configs/global/Default.yml: "Player N Input" with Handler and
// Device "Keyboard" and a Config of button names (rpcs3/Emu/Io/pad_config.h). A key is named as
// keyboard_pad_handler::GetKeyName does (rpcs3/Input/keyboard_pad_handler.cpp):
// QKeySequence(key).toString(NativeText), "Num+<key>" for the keypad, and on Windows the
// keypad's keys are told apart by scan code ("Num+," for the decimal key, assuming the Windows
// injector sends scan codes). On macOS NativeText uses the Mac's key symbols, which the source
// doesn't spell out, so macOS isn't supported.
//
// When it binds a name it parses it back with QKeySequence and drops the modifiers
// (GetKeyCode), so on Linux the keypad's keys are the same keys to it as the ones they share a
// Qt::Key with: KP_2 is 2, KP_Subtract is minus. The default key map's third player uses the
// main row's digits and minus, which the second has on the keypad, so RPCS3 gets two players.
var rpcs3Names = names{"b": "Cross", "a": "Circle", "y": "Square", "x": "Triangle", "l": "L1", "r": "R1", "l2": "L2", "r2": "R2",
	"l3": "L3", "r3": "R3", "select": "Select", "start": "Start", "up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"lup": "Left Stick Up", "ldown": "Left Stick Down", "lleft": "Left Stick Left", "lright": "Left Stick Right",
	"rup": "Right Stick Up", "rdown": "Right Stick Down", "rleft": "Right Stick Left", "rright": "Right Stick Right"}

var rpcs3 = emulator{
	players: 2,
	oses:    []string{"linux", "windows"},
	buttons: keysOf(rpcs3Names),
	ident: func(goos string, k hostKey) string {
		if goos == "linux" {
			return strconv.Itoa(k.qt)
		}
		return rpcs3Key(goos, k)
	},
	build: func(goos string, all [][]bound) []Target {
		const file = "input_configs/global/Default.yml"
		var out []Target
		for p, bs := range all {
			player := "Player " + strconv.Itoa(p+1) + " Input"
			out = append(out, Target{File: file, Format: "yaml", Path: []string{player, "Handler"}, Value: "Keyboard"},
				Target{File: file, Format: "yaml", Path: []string{player, "Device"}, Value: "Keyboard"})
			for _, b := range bs {
				out = append(out, Target{File: file, Format: "yaml", Path: []string{player, "Config", rpcs3Names.of(b.button)},
					Value: rpcs3Key(goos, b.key)})
			}
		}
		return out
	},
}

func rpcs3Key(goos string, k hostKey) string {
	if !k.pad {
		return k.seq
	}
	if goos == "windows" && k.qt == '.' {
		return "Num+,"
	}
	return "Num+" + k.seq
}
