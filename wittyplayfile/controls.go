// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Controls (version 2): how a game is played with the controllers in people's hands. Per-player
// button maps for gamepads, keyboards and mice, hotkeys, turbo, dead zones and sensitivity. They
// are given, like looks, for every game the file covers, per console, and (in a game's own file,
// as its "default") for one game.
//
// Every map is keyed by what the game's console controller has (Targets): the one controller
// libretro speaks, whose names a console's own buttons map to. On the pad, "b" is the bottom
// face button, "a" the right one, "y" the left and "x" the top.

// Controls is a file's "controls".
type Controls struct {
	Default  *ControlSet           `json:"default,omitempty"`
	Consoles map[string]ControlSet `json:"consoles,omitempty"`
	More
}

// ControlSet is the controls at one level. Every part is optional: an empty one is decided by the
// level above, and then by WittyPlay's own defaults.
type ControlSet struct {
	Players []PlayerControls `json:"players,omitempty"`
	Hotkeys *Hotkeys         `json:"hotkeys,omitempty"`
	Turbo   *Turbo           `json:"turbo,omitempty"`
	Analog  *Analog          `json:"analog,omitempty"`
	Rumble  *bool            `json:"rumble,omitempty"`
	More
}

// PlayerControls is one player's controls.
type PlayerControls struct {
	Player int `json:"player"` // 1 to MaxPlayers
	// Device is what is plugged into the player's port (Devices).
	Device string `json:"device,omitempty"`
	// Gamepad: a target's binding on a gamepad, numbered as the W3C standard gamepad numbers its
	// buttons and axes: "button:0", "axis:1-" (an axis, towards its negative end), "axis:5+",
	// "hat:0:up". A trigger read as an axis that rests at one end: "axis:2+@-1".
	Gamepad map[string]string `json:"gamepad,omitempty"`
	// Keyboard: a target's key, by its KeyboardEvent.code name: "KeyZ", "ArrowUp", "Enter".
	Keyboard map[string]string `json:"keyboard,omitempty"`
	// Mouse: a target's mouse button ("left", "right", "middle", "back", "forward", "wheelup",
	// "wheeldown"), and under "aim" what moving the mouse does ("pointer", "lstick", "rstick").
	Mouse map[string]string `json:"mouse,omitempty"`
	// Remap: what the game sees when a target is pressed, by target: {"a": "b"} swaps nothing by
	// itself; with {"b": "a"} too, A and B swap. Applied after the maps above.
	Remap map[string]string `json:"remap,omitempty"`
	// Turbo: targets that repeat while held (or, in "toggle" mode, until pressed again).
	Turbo  []string `json:"turbo,omitempty"`
	Analog *Analog  `json:"analog,omitempty"`
	More
}

// Hotkeys are the player's shortcuts to the emulator itself.
type Hotkeys struct {
	// Enable, when set, has to be held for any hotkey on a gamepad (so Select + R can save a state
	// while Select alone is still the game's).
	Enable *Hotkey `json:"enable,omitempty"`
	// Actions: a hotkey per action (HotkeyActions).
	Actions map[string]Hotkey `json:"actions,omitempty"`
	More
}

// Hotkey is one hotkey: a key (with modifiers, "Control+KeyS"), and gamepad buttons pressed
// together ("button:8+button:5").
type Hotkey struct {
	Keyboard string `json:"keyboard,omitempty"`
	Gamepad  string `json:"gamepad,omitempty"`
	More
}

// Turbo says how fast turbo presses.
type Turbo struct {
	Rate float64 `json:"rate,omitempty"` // presses a second, 1 to 30
	Mode string  `json:"mode,omitempty"` // "hold" (while held) or "toggle"
	More
}

// Analog tunes sticks, triggers and the mouse. Each is optional.
type Analog struct {
	Deadzone         *float64 `json:"deadzone,omitempty"`         // 0 to 0.9 of the stick's travel
	Sensitivity      *float64 `json:"sensitivity,omitempty"`      // 0.1 to 5
	MouseSensitivity *float64 `json:"mouseSensitivity,omitempty"` // 0.1 to 5
	TriggerThreshold *float64 `json:"triggerThreshold,omitempty"` // 0.05 to 1: when an analog trigger counts as pressed
	// StickAsDPad: the left stick presses the D-pad too, for a game that has no analog control.
	StickAsDPad *bool `json:"stickAsDPad,omitempty"`
	More
}

// Limits.
const (
	MaxPlayers  = 8
	maxBindings = 64
)

// Targets are what a player's maps are keyed by, numbered as libretro numbers them (and as
// WittyPlay's own layouts do): the numbers are what TargetID gives.
var Targets = []string{
	"b", "y", "select", "start", "up", "down", "left", "right", "a", "x", "l", "r", "l2", "r2", "l3", "r3",
	"lstick-left", "lstick-right", "lstick-up", "lstick-down", "rstick-left", "rstick-right", "rstick-up", "rstick-down",
}

// TargetID is a target's number, -1 for none.
func TargetID(name string) int {
	for i, t := range Targets {
		if t == name {
			return i
		}
	}
	return -1
}

// Devices are what may be plugged into a port.
var Devices = []string{"gamepad", "analog", "mouse", "lightgun", "keyboard", "multitap", "none"}

// HotkeyActions are the actions a hotkey can do.
var HotkeyActions = []string{
	"menu", "pause", "reset", "exit", "saveState", "loadState", "nextSlot", "previousSlot", "fastForward", "fastForwardHold",
	"slowMotion", "rewind", "frameAdvance", "screenshot", "recordVideo", "fullscreen", "mute", "volumeUp", "volumeDown",
	"nextDisc", "ejectDisc", "nextShader", "cheats", "netplayChat",
}

var (
	gamepadRe = regexp.MustCompile(`^(button:(\d{1,2})|axis:(\d{1,2})([+-])(@-?1)?|hat:(\d{1,2}):(up|down|left|right))$`)
	keyRe     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}$`)
	mouseBtns = []string{"left", "right", "middle", "back", "forward", "wheelup", "wheeldown"}
	aims      = []string{"pointer", "lstick", "rstick"}
	modifiers = map[string]bool{"Control": true, "Shift": true, "Alt": true, "Meta": true}
)

// PadBinding is a gamepad binding read from its text form.
type PadBinding struct {
	Button, Axis, Hat *int
	// Dir: for an axis 1 or -1; for a hat 1 up, 2 right, 3 down, 4 left.
	Dir int
	// Rest: where a trigger read as an axis rests, -1 or 1; 0 for the middle.
	Rest int
}

// ParseGamepad reads "button:0", "axis:1-", "axis:2+@-1" or "hat:0:up".
func ParseGamepad(s string) (PadBinding, bool) {
	m := gamepadRe.FindStringSubmatch(s)
	if m == nil {
		return PadBinding{}, false
	}
	num := func(v string, max int) (*int, bool) {
		n, err := strconv.Atoi(v)
		return &n, err == nil && n >= 0 && n < max
	}
	var b PadBinding
	var ok bool
	switch {
	case m[2] != "":
		b.Button, ok = num(m[2], 64)
	case m[3] != "":
		b.Axis, ok = num(m[3], 32)
		b.Dir = 1
		if m[4] == "-" {
			b.Dir = -1
		}
		switch m[5] {
		case "@-1":
			b.Rest = -1
		case "@1":
			b.Rest = 1
		}
	default:
		b.Hat, ok = num(m[6], 32)
		b.Dir = map[string]int{"up": 1, "right": 2, "down": 3, "left": 4}[m[7]]
	}
	return b, ok
}

// String writes a binding in its text form.
func (b PadBinding) String() string {
	switch {
	case b.Button != nil:
		return fmt.Sprintf("button:%d", *b.Button)
	case b.Axis != nil:
		s := fmt.Sprintf("axis:%d+", *b.Axis)
		if b.Dir < 0 {
			s = fmt.Sprintf("axis:%d-", *b.Axis)
		}
		if b.Rest != 0 {
			s += fmt.Sprintf("@%d", b.Rest)
		}
		return s
	case b.Hat != nil:
		return fmt.Sprintf("hat:%d:%s", *b.Hat, map[int]string{1: "up", 2: "right", 3: "down", 4: "left"}[b.Dir])
	}
	return ""
}

// validKeyChord checks "KeyS" or "Control+Shift+KeyS".
func validKeyChord(s string) bool {
	parts := strings.Split(s, "+")
	if len(parts) > 4 {
		return false
	}
	for i, p := range parts {
		if i < len(parts)-1 {
			if !modifiers[p] {
				return false
			}
			continue
		}
		if !keyRe.MatchString(p) {
			return false
		}
	}
	return true
}

func validPadChord(s string) bool {
	parts := strings.Split(s, "+button:")
	if len(parts) > 3 {
		return false
	}
	for i, p := range parts {
		if i > 0 {
			p = "button:" + p
		}
		if _, ok := ParseGamepad(p); !ok {
			return false
		}
	}
	return true
}

func oneOfList(v string, l []string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func (c *checker) controlSet(w string, s *ControlSet) {
	if s == nil {
		return
	}
	if len(s.Players) > MaxPlayers {
		c.bad(w+".players", "at most %d players", MaxPlayers)
	}
	seen := map[int]bool{}
	for i := range s.Players {
		p, pw := &s.Players[i], fmt.Sprintf("%s.players[%d]", w, i)
		if p.Player < 1 || p.Player > MaxPlayers {
			c.bad(pw+".player", "a player is numbered 1 to %d", MaxPlayers)
		} else if seen[p.Player] {
			c.bad(pw+".player", "player %d is given twice", p.Player)
		}
		seen[p.Player] = true
		if p.Device != "" && !oneOfList(p.Device, Devices) && !c.newer {
			c.bad(pw+".device", "%q is not one of %s", p.Device, strings.Join(Devices, ", "))
		}
		maps := func(name string, m map[string]string, valid func(k, v string) bool, what string) {
			if len(m) > maxBindings {
				c.bad(pw+"."+name, "more than %d bindings", maxBindings)
			}
			for _, k := range sortedKeys(m) {
				if !valid(k, m[k]) {
					c.bad(pw+"."+name+"."+k, "%s", what)
				}
			}
		}
		target := func(k string) bool { return TargetID(k) >= 0 || c.newer && simpleName.MatchString(k) }
		maps("gamepad", p.Gamepad, func(k, v string) bool { _, ok := ParseGamepad(v); return target(k) && ok },
			`maps a button name (b, a, up, l2, lstick-left...) to "button:0", "axis:1-" or "hat:0:up"`)
		maps("keyboard", p.Keyboard, func(k, v string) bool { return target(k) && keyRe.MatchString(v) },
			`maps a button name to a key's code name, such as KeyZ or ArrowUp`)
		maps("mouse", p.Mouse, func(k, v string) bool {
			if k == "aim" {
				return oneOfList(v, aims)
			}
			return target(k) && oneOfList(v, mouseBtns)
		}, `maps a button name to left, right, middle, back, forward, wheelup or wheeldown; "aim" is pointer, lstick or rstick`)
		maps("remap", p.Remap, func(k, v string) bool { return target(k) && target(v) }, "maps a button name to another")
		if len(p.Turbo) > len(Targets) {
			c.bad(pw+".turbo", "too many buttons")
		}
		for j, t := range p.Turbo {
			if !target(t) {
				c.bad(fmt.Sprintf("%s.turbo[%d]", pw, j), "%q is not a button name", t)
			}
		}
		c.analog(pw+".analog", p.Analog)
	}
	if h := s.Hotkeys; h != nil {
		hk := func(hw string, k Hotkey) {
			if k.Keyboard != "" && !validKeyChord(k.Keyboard) {
				c.bad(hw+".keyboard", `a key's code name, with modifiers if wanted: "F2", "Control+KeyS"`)
			}
			if k.Gamepad != "" && !validPadChord(k.Gamepad) {
				c.bad(hw+".gamepad", `up to three gamepad bindings joined by +: "button:8+button:5"`)
			}
		}
		if h.Enable != nil {
			hk(w+".hotkeys.enable", *h.Enable)
		}
		for _, k := range sortedKeys(h.Actions) {
			if !oneOfList(k, HotkeyActions) && !c.newer {
				c.bad(w+".hotkeys.actions."+k, "%q is not a hotkey action", k)
			}
			hk(w+".hotkeys.actions."+k, h.Actions[k])
		}
	}
	if t := s.Turbo; t != nil {
		if t.Rate != 0 && (math.IsNaN(t.Rate) || t.Rate < 1 || t.Rate > 30) {
			c.bad(w+".turbo.rate", "between 1 and 30 presses a second")
		}
		if t.Mode != "" && t.Mode != "hold" && t.Mode != "toggle" {
			c.bad(w+".turbo.mode", "hold or toggle")
		}
	}
	c.analog(w+".analog", s.Analog)
}

func (c *checker) analog(w string, a *Analog) {
	if a == nil {
		return
	}
	rng := func(name string, v *float64, lo, hi float64) {
		if v != nil && (math.IsNaN(*v) || *v < lo || *v > hi) {
			c.bad(w+"."+name, "between %g and %g", lo, hi)
		}
	}
	rng("deadzone", a.Deadzone, 0, 0.9)
	rng("sensitivity", a.Sensitivity, 0.1, 5)
	rng("mouseSensitivity", a.MouseSensitivity, 0.1, 5)
	rng("triggerThreshold", a.TriggerThreshold, 0.05, 1)
}

// For is the controls for one game on one console: the console's over the file's default, part
// by part. A player given at both levels takes the console's maps over the default's, binding by
// binding.
func (c *Controls) For(console string) ControlSet {
	var out ControlSet
	if c == nil {
		return out
	}
	if c.Default != nil {
		out = *c.Default
	}
	if s, ok := c.Consoles[console]; ok && console != "" {
		out = s.Over(out)
	}
	return out
}

// Over is s with what it leaves out taken from under.
func (s ControlSet) Over(under ControlSet) ControlSet {
	if s.Hotkeys == nil {
		s.Hotkeys = under.Hotkeys
	} else if under.Hotkeys != nil {
		h := *s.Hotkeys
		if h.Enable == nil {
			h.Enable = under.Hotkeys.Enable
		}
		h.Actions = mergeMap(under.Hotkeys.Actions, h.Actions)
		s.Hotkeys = &h
	}
	if s.Turbo == nil {
		s.Turbo = under.Turbo
	}
	if s.Analog == nil {
		s.Analog = under.Analog
	}
	if s.Rumble == nil {
		s.Rumble = under.Rumble
	}
	byPlayer := map[int]PlayerControls{}
	for _, p := range under.Players {
		byPlayer[p.Player] = p
	}
	for _, p := range s.Players {
		if u, ok := byPlayer[p.Player]; ok {
			if p.Device == "" {
				p.Device = u.Device
			}
			p.Gamepad, p.Keyboard = mergeMap(u.Gamepad, p.Gamepad), mergeMap(u.Keyboard, p.Keyboard)
			p.Mouse, p.Remap = mergeMap(u.Mouse, p.Mouse), mergeMap(u.Remap, p.Remap)
			if p.Turbo == nil {
				p.Turbo = u.Turbo
			}
			if p.Analog == nil {
				p.Analog = u.Analog
			}
		}
		byPlayer[p.Player] = p
	}
	s.Players = nil
	for _, n := range sortedKeysInt(byPlayer) {
		s.Players = append(s.Players, byPlayer[n])
	}
	return s
}

// Player is one player's controls in the set, or nil.
func (s ControlSet) Player(n int) *PlayerControls {
	for i := range s.Players {
		if s.Players[i].Player == n {
			return &s.Players[i]
		}
	}
	return nil
}

func mergeMap[V any](under, over map[string]V) map[string]V {
	if len(under) == 0 {
		return over
	}
	out := make(map[string]V, len(under)+len(over))
	for k, v := range under {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

func sortedKeysInt[V any](m map[int]V) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// Each type with More reads and writes itself through an alias, which has no methods.

func (x *Controls) UnmarshalJSON(b []byte) error {
	type a Controls
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Controls) MarshalJSON() ([]byte, error) {
	type a Controls
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *ControlSet) UnmarshalJSON(b []byte) error {
	type a ControlSet
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x ControlSet) MarshalJSON() ([]byte, error) {
	type a ControlSet
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *PlayerControls) UnmarshalJSON(b []byte) error {
	type a PlayerControls
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x PlayerControls) MarshalJSON() ([]byte, error) {
	type a PlayerControls
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Hotkeys) UnmarshalJSON(b []byte) error {
	type a Hotkeys
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Hotkeys) MarshalJSON() ([]byte, error) {
	type a Hotkeys
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Hotkey) UnmarshalJSON(b []byte) error {
	type a Hotkey
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Hotkey) MarshalJSON() ([]byte, error) {
	type a Hotkey
	return encodeKeeping((*a)(&x), x.Unknown)
}

func (x *Turbo) UnmarshalJSON(b []byte) error {
	type a Turbo
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Turbo) MarshalJSON() ([]byte, error) { type a Turbo; return encodeKeeping((*a)(&x), x.Unknown) }

func (x *Analog) UnmarshalJSON(b []byte) error {
	type a Analog
	e, err := decodeKeeping(b, (*a)(x))
	x.Unknown = e
	return err
}
func (x Analog) MarshalJSON() ([]byte, error) {
	type a Analog
	return encodeKeeping((*a)(&x), x.Unknown)
}
