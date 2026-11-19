// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamefiles

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// Per-game input files in the key = "value" form libretro front ends write:
//
//   - Game.rmp, a remap: input_playerN_btn_<button> = "<RetroPad id>" says what the game sees when
//     the button is pressed, and input_libretro_device_pN which device is plugged in;
//   - Game.cfg, an override: input_playerN_<button> (a key), _btn and _axis (a gamepad), hotkeys,
//     the analog dead zone and sensitivity, and the turbo period.
//
// Only input lines are read or written. Writing keeps every other line of a file that is there.

// cfgButtons are the button names these files use, in RetroPad order (wittyplayfile.Targets).
var cfgButtons = []string{"b", "y", "select", "start", "up", "down", "left", "right", "a", "x", "l", "r", "l2", "r2", "l3", "r3",
	"l_x_minus", "l_x_plus", "l_y_minus", "l_y_plus", "r_x_minus", "r_x_plus", "r_y_minus", "r_y_plus"}

func cfgButton(name string) string {
	for i, b := range cfgButtons {
		if b == name {
			return wittyplayfile.Targets[i]
		}
	}
	return ""
}

func cfgName(target string) string {
	if i := wittyplayfile.TargetID(target); i >= 0 {
		return cfgButtons[i]
	}
	return ""
}

// cfgHotkeys: the files' hotkey names and the .wittyplay action each is.
var cfgHotkeys = map[string]string{
	"input_menu_toggle": "menu", "input_pause_toggle": "pause", "input_reset": "reset", "input_exit_emulator": "exit",
	"input_save_state": "saveState", "input_load_state": "loadState", "input_state_slot_increase": "nextSlot",
	"input_state_slot_decrease": "previousSlot", "input_toggle_fast_forward": "fastForward", "input_hold_fast_forward": "fastForwardHold",
	"input_hold_slowmotion": "slowMotion", "input_rewind": "rewind", "input_frame_advance": "frameAdvance", "input_screenshot": "screenshot",
	"input_recording_toggle": "recordVideo", "input_toggle_fullscreen": "fullscreen", "input_audio_mute": "mute",
	"input_volume_up": "volumeUp", "input_volume_down": "volumeDown", "input_disk_next": "nextDisc",
	"input_disk_eject_toggle": "ejectDisc", "input_shader_next": "nextShader", "input_cheat_toggle": "cheats",
	"input_netplay_game_watch": "netplayChat",
}

// cfgKeys: the files' key names and their KeyboardEvent.code names.
var cfgKeys = func() map[string]string {
	m := map[string]string{
		"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight", "enter": "Enter", "kp_enter": "NumpadEnter",
		"space": "Space", "escape": "Escape", "tab": "Tab", "backspace": "Backspace", "shift": "ShiftLeft", "rshift": "ShiftRight",
		"ctrl": "ControlLeft", "rctrl": "ControlRight", "alt": "AltLeft", "ralt": "AltRight", "insert": "Insert", "del": "Delete",
		"home": "Home", "end": "End", "pageup": "PageUp", "pagedown": "PageDown", "comma": "Comma", "period": "Period",
		"slash": "Slash", "backslash": "Backslash", "semicolon": "Semicolon", "quote": "Quote", "leftbracket": "BracketLeft",
		"rightbracket": "BracketRight", "minus": "Minus", "equals": "Equal", "tilde": "Backquote", "backquote": "Backquote",
		"capslock": "CapsLock", "pause": "Pause", "print": "PrintScreen", "scroll_lock": "ScrollLock",
		"add": "NumpadAdd", "subtract": "NumpadSubtract", "multiply": "NumpadMultiply", "divide": "NumpadDivide", "kp_period": "NumpadDecimal",
	}
	for c := 'a'; c <= 'z'; c++ {
		m[string(c)] = "Key" + strings.ToUpper(string(c))
	}
	for i := 0; i <= 9; i++ {
		m[fmt.Sprintf("num%d", i)] = fmt.Sprintf("Digit%d", i)
		m[fmt.Sprintf("keypad%d", i)] = fmt.Sprintf("Numpad%d", i)
	}
	for i := 1; i <= 15; i++ {
		m[fmt.Sprintf("f%d", i)] = fmt.Sprintf("F%d", i)
	}
	return m
}()

var codeToCfg = func() map[string]string {
	out := map[string]string{}
	for k, v := range cfgKeys {
		if old, ok := out[v]; !ok || len(k) < len(old) {
			out[v] = k
		}
	}
	return out
}()

// cfgLines reads key = "value" lines.
func cfgLines(b []byte) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || len(out) >= 5000 {
			continue
		}
		v = strings.TrimSpace(v)
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"`)
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// devices: libretro device numbers.
var devices = map[int]string{0: "none", 1: "gamepad", 2: "mouse", 3: "keyboard", 4: "lightgun", 5: "analog"}

func deviceNumber(name string) (int, bool) {
	for n, d := range devices {
		if d == name {
			return n, true
		}
	}
	return 0, false
}

func readRemap(p string) ([]wittyplayfile.PlayerControls, map[int]string, bool) {
	b, ok := readSmall(p, MaxConfig)
	if !ok {
		return nil, nil, false
	}
	lines := cfgLines(b)
	byPlayer := map[int]*wittyplayfile.PlayerControls{}
	dev := map[int]string{}
	for k, v := range lines {
		var n int
		var btn string
		if _, err := fmt.Sscanf(k, "input_libretro_device_p%d", &n); err == nil && n >= 1 && n <= wittyplayfile.MaxPlayers {
			if id, err := strconv.Atoi(v); err == nil {
				if d, ok := devices[id&0xff]; ok {
					dev[n] = d
				}
			}
			continue
		}
		rest, ok := strings.CutPrefix(k, "input_player")
		if !ok {
			continue
		}
		num, after, ok := strings.Cut(rest, "_btn_")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(num)
		btn = cfgButton(after)
		id, err2 := strconv.Atoi(v)
		if err != nil || err2 != nil || n < 1 || n > wittyplayfile.MaxPlayers || btn == "" || id < 0 || id >= 16 {
			continue
		}
		to := wittyplayfile.Targets[id]
		if to == btn {
			continue
		}
		p := byPlayer[n]
		if p == nil {
			p = &wittyplayfile.PlayerControls{Player: n, Remap: map[string]string{}}
			byPlayer[n] = p
		}
		p.Remap[btn] = to
	}
	var out []wittyplayfile.PlayerControls
	for _, n := range sortedInts(byPlayer) {
		out = append(out, *byPlayer[n])
	}
	if len(out) == 0 && len(dev) == 0 {
		return nil, nil, false
	}
	return out, dev, true
}

func readConfig(p string) (wittyplayfile.ControlSet, bool) {
	var s wittyplayfile.ControlSet
	b, ok := readSmall(p, MaxConfig)
	if !ok {
		return s, false
	}
	lines := cfgLines(b)
	players := map[int]*wittyplayfile.PlayerControls{}
	player := func(n int) *wittyplayfile.PlayerControls {
		if players[n] == nil {
			players[n] = &wittyplayfile.PlayerControls{Player: n}
		}
		return players[n]
	}
	any := false
	for k, v := range lines {
		if v == "" || v == "nul" {
			continue
		}
		if rest, ok := strings.CutPrefix(k, "input_player"); ok {
			num, name, ok := strings.Cut(rest, "_")
			n, err := strconv.Atoi(num)
			if !ok || err != nil || n < 1 || n > wittyplayfile.MaxPlayers {
				continue
			}
			kind := "key"
			for _, sfx := range []string{"_btn", "_axis"} {
				if strings.HasSuffix(name, sfx) {
					kind, name = sfx[1:], strings.TrimSuffix(name, sfx)
				}
			}
			t := cfgButton(name)
			if t == "" {
				continue
			}
			switch kind {
			case "key":
				if code, ok := cfgKeys[v]; ok {
					p := player(n)
					if p.Keyboard == nil {
						p.Keyboard = map[string]string{}
					}
					p.Keyboard[t], any = code, true
				}
			default:
				if g, ok := padFromCfg(kind, v); ok {
					p := player(n)
					if p.Gamepad == nil {
						p.Gamepad = map[string]string{}
					}
					p.Gamepad[t], any = g, true
				}
			}
			continue
		}
		base, kind := k, "key"
		if strings.HasSuffix(k, "_btn") {
			base, kind = strings.TrimSuffix(k, "_btn"), "btn"
		}
		if action, ok := cfgHotkeys[base]; ok || base == "input_enable_hotkey" {
			var hk wittyplayfile.Hotkey
			if kind == "key" {
				code, ok := cfgKeys[v]
				if !ok {
					continue
				}
				hk.Keyboard = code
			} else if g, ok := padFromCfg("btn", v); ok {
				hk.Gamepad = g
			} else {
				continue
			}
			if s.Hotkeys == nil {
				s.Hotkeys = &wittyplayfile.Hotkeys{}
			}
			if base == "input_enable_hotkey" {
				s.Hotkeys.Enable = mergeHotkey(s.Hotkeys.Enable, hk)
			} else {
				if s.Hotkeys.Actions == nil {
					s.Hotkeys.Actions = map[string]wittyplayfile.Hotkey{}
				}
				s.Hotkeys.Actions[action] = *mergeHotkey(ptr(s.Hotkeys.Actions[action]), hk)
			}
			any = true
			continue
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(f) {
			continue
		}
		switch k {
		case "input_analog_deadzone":
			if f >= 0 && f <= 0.9 {
				analog(&s).Deadzone, any = &f, true
			}
		case "input_analog_sensitivity":
			if f >= 0.1 && f <= 5 {
				analog(&s).Sensitivity, any = &f, true
			}
		case "input_turbo_period":
			if f >= 2 && f <= 60 {
				rate := math.Round(60/f*10) / 10
				s.Turbo, any = &wittyplayfile.Turbo{Rate: math.Max(1, math.Min(30, rate))}, true
			}
		}
	}
	for _, n := range sortedInts(players) {
		s.Players = append(s.Players, *players[n])
	}
	return s, any
}

func ptr(h wittyplayfile.Hotkey) *wittyplayfile.Hotkey { return &h }

func mergeHotkey(have *wittyplayfile.Hotkey, add wittyplayfile.Hotkey) *wittyplayfile.Hotkey {
	if have == nil {
		return &add
	}
	out := *have
	if add.Keyboard != "" {
		out.Keyboard = add.Keyboard
	}
	if add.Gamepad != "" {
		out.Gamepad = add.Gamepad
	}
	return &out
}

func analog(s *wittyplayfile.ControlSet) *wittyplayfile.Analog {
	if s.Analog == nil {
		s.Analog = &wittyplayfile.Analog{}
	}
	return s.Analog
}

// padFromCfg reads "3", "h0up" (a _btn value) or "+1", "-2" (an _axis value).
func padFromCfg(kind, v string) (string, bool) {
	var out string
	switch {
	case kind == "axis" && len(v) >= 2 && (v[0] == '+' || v[0] == '-') && isDigits(v[1:]):
		out = "axis:" + v[1:] + string(v[0])
	case kind == "btn" && isDigits(v):
		out = "button:" + v
	case kind == "btn" && strings.HasPrefix(v, "h"):
		i := 1
		for i < len(v) && v[i] >= '0' && v[i] <= '9' {
			i++
		}
		if i == 1 {
			return "", false
		}
		out = "hat:" + v[1:i] + ":" + v[i:]
	default:
		return "", false
	}
	_, ok := wittyplayfile.ParseGamepad(out)
	return out, ok
}

// padToCfg writes a binding as a _btn or _axis value.
func padToCfg(s string) (kind, v string, ok bool) {
	b, ok := wittyplayfile.ParseGamepad(s)
	switch {
	case !ok:
		return "", "", false
	case b.Button != nil:
		return "btn", strconv.Itoa(*b.Button), true
	case b.Hat != nil:
		return "btn", fmt.Sprintf("h%d%s", *b.Hat, map[int]string{1: "up", 2: "right", 3: "down", 4: "left"}[b.Dir]), true
	case b.Axis != nil:
		sign := "+"
		if b.Dir < 0 {
			sign = "-"
		}
		return "axis", sign + strconv.Itoa(*b.Axis), true
	}
	return "", "", false
}

// remapLines and configLines are the lines a control set gives each file.
func remapLines(s wittyplayfile.ControlSet) map[string]string {
	out := map[string]string{}
	for _, p := range s.Players {
		for from, to := range p.Remap {
			if id := wittyplayfile.TargetID(to); id >= 0 && id < 16 && wittyplayfile.TargetID(from) < 16 {
				out[fmt.Sprintf("input_player%d_btn_%s", p.Player, cfgName(from))] = strconv.Itoa(id)
			}
		}
		if n, ok := deviceNumber(p.Device); ok && p.Device != "" {
			out[fmt.Sprintf("input_libretro_device_p%d", p.Player)] = strconv.Itoa(n)
		}
	}
	return out
}

func configLines(s wittyplayfile.ControlSet) map[string]string {
	out := map[string]string{}
	for _, p := range s.Players {
		for t, code := range p.Keyboard {
			if k, ok := codeToCfg[code]; ok {
				out[fmt.Sprintf("input_player%d_%s", p.Player, cfgName(t))] = k
			}
		}
		for t, g := range p.Gamepad {
			if kind, v, ok := padToCfg(g); ok {
				out[fmt.Sprintf("input_player%d_%s_%s", p.Player, cfgName(t), kind)] = v
			}
		}
	}
	if h := s.Hotkeys; h != nil {
		put := func(base string, k wittyplayfile.Hotkey) {
			if c, ok := codeToCfg[k.Keyboard]; ok && !strings.Contains(k.Keyboard, "+") {
				out[base] = c
			}
			if kind, v, ok := padToCfg(k.Gamepad); ok && kind == "btn" {
				out[base+"_btn"] = v
			}
		}
		if h.Enable != nil {
			put("input_enable_hotkey", *h.Enable)
		}
		for base, action := range cfgHotkeys {
			if k, ok := h.Actions[action]; ok {
				put(base, k)
			}
		}
	}
	if a := s.Analog; a != nil {
		if a.Deadzone != nil {
			out["input_analog_deadzone"] = fmt.Sprintf("%f", *a.Deadzone)
		}
		if a.Sensitivity != nil {
			out["input_analog_sensitivity"] = fmt.Sprintf("%f", *a.Sensitivity)
		}
	}
	if t := s.Turbo; t != nil && t.Rate > 0 {
		out["input_turbo_period"] = strconv.Itoa(int(math.Round(60 / t.Rate)))
	}
	return out
}

// mergeConfigFile sets lines in a key = "value" file, keeping every other line, and writes it
// when anything changed. A file that can't be read whole is left alone.
func mergeConfigFile(p string, set map[string]string) (bool, error) {
	if len(set) == 0 {
		return false, nil
	}
	var old []byte
	if _, err := os.Lstat(p); err == nil {
		b, ok := readSmall(p, MaxConfig)
		if !ok {
			return false, fmt.Errorf("%s can't be read: left alone", p)
		}
		old = b
	}
	done := map[string]bool{}
	var out bytes.Buffer
	changed := false
	sc := bufio.NewScanner(bytes.NewReader(old))
	sc.Buffer(make([]byte, 4096), 64<<10)
	for sc.Scan() {
		line := sc.Text()
		k, _, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if v, mine := set[k]; ok && mine && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			nl := fmt.Sprintf("%s = %q", k, v)
			changed = changed || nl != strings.TrimSpace(line)
			out.WriteString(nl + "\n")
			done[k] = true
			continue
		}
		out.WriteString(line + "\n")
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		if !done[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&out, "%s = %q\n", k, set[k])
		changed = true
	}
	if !changed {
		return false, nil
	}
	return true, writeAtomic(p, out.Bytes())
}

func sortedInts[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
