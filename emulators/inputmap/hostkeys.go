// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package inputmap

import "strings"

// hostKey is one physical key on a US keyboard, named every way the injectors and the emulators
// name it. The injectors press it (xdotool by X keysym on Linux, CGEventPostToPid by kVK code on
// macOS, SendInput by virtual-key code on Windows) and each emulator's settings must name the
// same key in its own notation, so both sides read from this one table and can't disagree.
type hostKey struct {
	sym int // X11 keysym; also what GTK hands Cemu as the raw key code on Linux

	// Dolphin's XInput2 name: XKeysymToString of the key's level-0 keysym, letters upper-cased
	// (Source/Core/InputCommon/ControllerInterface/Xlib/XInput2.cpp, GetNameForKeyCode). For a
	// keypad digit that is its navigation keysym (KP_2's key is "KP_Down" on the standard xkb
	// keypad), because level 0 is the one without NumLock.
	xname string

	mac     int    // macOS virtual key code, kVK_* in HIToolbox/Events.h
	macName string // Dolphin's Quartz name (QuartzKeyboardAndMouse.mm); "" for "Key <code>"

	vk  int    // Windows virtual-key code, VK_*
	dik string // Dolphin's DInput name (DInput/NamedKeys.h)

	sdl int // SDL scancode, the USB HID usage

	qt     int    // Qt::Key
	pad    bool   // Qt reports it with Qt::KeypadModifier
	qtName string // PCSX2's name for qt (pcsx2-qt/QtKeyCodes.cpp, s_qt_key_names)
	seq    string // QKeySequence(qt).toString(NativeText) on Linux and Windows, as RPCS3 names keys

	nkSDL int // PPSSPP's NKCODE from its SDL frontend (SDL/NKCodeFromSDL.h)
	nkWin int // PPSSPP's NKCODE from its Windows frontend (Windows/RawInput.cpp)
}

// Qt::Key values (qnamespace.h) used below.
const (
	qtEscape    = 0x01000000
	qtTab       = 0x01000001
	qtBackspace = 0x01000003
	qtReturn    = 0x01000004
	qtEnter     = 0x01000005
	qtInsert    = 0x01000006
	qtDelete    = 0x01000007
	qtHome      = 0x01000010
	qtEnd       = 0x01000011
	qtLeft      = 0x01000012
	qtUp        = 0x01000013
	qtRight     = 0x01000014
	qtDown      = 0x01000015
	qtPageUp    = 0x01000016
	qtPageDown  = 0x01000017
	qtF1        = 0x01000030
	qtHelp      = 0x01000058

	qtKeypadModifier = 0x20000000
)

// keys is every key this package can map, by X keysym name.
var keys = map[string]hostKey{}

// aliases are other X names for the same keysym.
var aliases = map[string]string{"Page_Up": "Prior", "Page_Down": "Next"}

func init() {
	// Letters: X names them lower case (the unshifted keysym); Dolphin's XInput2 upper-cases them.
	macLetters := map[byte]int{'a': 0x00, 's': 0x01, 'd': 0x02, 'f': 0x03, 'h': 0x04, 'g': 0x05, 'z': 0x06, 'x': 0x07,
		'c': 0x08, 'v': 0x09, 'b': 0x0B, 'q': 0x0C, 'w': 0x0D, 'e': 0x0E, 'r': 0x0F, 'y': 0x10, 't': 0x11, 'o': 0x1F,
		'u': 0x20, 'i': 0x22, 'p': 0x23, 'l': 0x25, 'j': 0x26, 'k': 0x28, 'n': 0x2D, 'm': 0x2E}
	for c := byte('a'); c <= 'z'; c++ {
		up := strings.ToUpper(string(c))
		nk := 29 + int(c-'a') // NKCODE_A = 29
		keys[string(c)] = hostKey{sym: int(c), xname: up, mac: macLetters[c], macName: up, vk: int(c - 32), dik: up,
			sdl: 4 + int(c-'a'), qt: int(c - 32), qtName: up, seq: up, nkSDL: nk, nkWin: nk}
	}
	// The digit row. kVK codes for it aren't in order.
	macDigits := []int{0x1D, 0x12, 0x13, 0x14, 0x15, 0x17, 0x16, 0x1A, 0x1C, 0x19}
	for d := 0; d <= 9; d++ {
		s := string(rune('0' + d))
		sdl := 30 + d - 1 // SDL_SCANCODE_1 = 30 ... SDL_SCANCODE_0 = 39
		if d == 0 {
			sdl = 39
		}
		keys[s] = hostKey{sym: '0' + d, xname: s, mac: macDigits[d], macName: s, vk: '0' + d, dik: s, sdl: sdl,
			qt: '0' + d, qtName: s, seq: s, nkSDL: 7 + d, nkWin: 7 + d}
	}
	// F1 to F12.
	macF := []int{0x7A, 0x78, 0x63, 0x76, 0x60, 0x61, 0x62, 0x64, 0x65, 0x6D, 0x67, 0x6F}
	for i := 0; i < 12; i++ {
		n := "F" + itoa(i+1)
		keys[n] = hostKey{sym: 0xFFBE + i, xname: n, mac: macF[i], macName: n, vk: 0x70 + i, dik: n, sdl: 58 + i,
			qt: qtF1 + i, qtName: n, seq: n, nkSDL: 131 + i, nkWin: 131 + i}
	}
	// The keypad's digits. Qt reports Qt::Key_0..9 with the keypad modifier.
	kpX := []string{"KP_Insert", "KP_End", "KP_Down", "KP_Next", "KP_Left", "KP_Begin", "KP_Right", "KP_Home", "KP_Up", "KP_Prior"}
	kpMac := []int{0x52, 0x53, 0x54, 0x55, 0x56, 0x57, 0x58, 0x59, 0x5B, 0x5C}
	for d := 0; d <= 9; d++ {
		s := string(rune('0' + d))
		sdl := 89 + d - 1 // SDL_SCANCODE_KP_1 = 89 ... SDL_SCANCODE_KP_0 = 98
		if d == 0 {
			sdl = 98
		}
		keys["KP_"+s] = hostKey{sym: 0xFFB0 + d, xname: kpX[d], mac: kpMac[d], macName: "Keypad " + s, vk: 0x60 + d,
			dik: "NUMPAD" + s, sdl: sdl, qt: '0' + d, pad: true, qtName: s, seq: s, nkSDL: 144 + d, nkWin: 144 + d}
	}

	for name, k := range map[string]hostKey{
		"Up":    {sym: 0xFF52, xname: "Up", mac: 0x7E, macName: "Up Arrow", vk: 0x26, dik: "UP", sdl: 82, qt: qtUp, qtName: "Up", seq: "Up", nkSDL: 19, nkWin: 19},
		"Down":  {sym: 0xFF54, xname: "Down", mac: 0x7D, macName: "Down Arrow", vk: 0x28, dik: "DOWN", sdl: 81, qt: qtDown, qtName: "Down", seq: "Down", nkSDL: 20, nkWin: 20},
		"Left":  {sym: 0xFF51, xname: "Left", mac: 0x7B, macName: "Left Arrow", vk: 0x25, dik: "LEFT", sdl: 80, qt: qtLeft, qtName: "Left", seq: "Left", nkSDL: 21, nkWin: 21},
		"Right": {sym: 0xFF53, xname: "Right", mac: 0x7C, macName: "Right Arrow", vk: 0x27, dik: "RIGHT", sdl: 79, qt: qtRight, qtName: "Right", seq: "Right", nkSDL: 22, nkWin: 22},
		"Home":  {sym: 0xFF50, xname: "Home", mac: 0x73, macName: "Home", vk: 0x24, dik: "HOME", sdl: 74, qt: qtHome, qtName: "Home", seq: "Home", nkSDL: 122, nkWin: 122},
		"End":   {sym: 0xFF57, xname: "End", mac: 0x77, macName: "End", vk: 0x23, dik: "END", sdl: 77, qt: qtEnd, qtName: "End", seq: "End", nkSDL: 123, nkWin: 123},
		// XKeysymToString gives a keysym's first name in keysymdef.h: Prior, not Page_Up.
		"Prior": {sym: 0xFF55, xname: "Prior", mac: 0x74, macName: "Page Up", vk: 0x21, dik: "PRIOR", sdl: 75, qt: qtPageUp, qtName: "PageUp", seq: "PgUp", nkSDL: 92, nkWin: 92},
		"Next":  {sym: 0xFF56, xname: "Next", mac: 0x79, macName: "Page Down", vk: 0x22, dik: "NEXT", sdl: 78, qt: qtPageDown, qtName: "PageDown", seq: "PgDown", nkSDL: 93, nkWin: 93},
		// A Mac keyboard has Help where Insert is; Qt reports it as Qt::Key_Help (qapplekeymapper.mm),
		// and Dolphin's Quartz table has no name for it, so it is "Key 114" there.
		"Insert":    {sym: 0xFF63, xname: "Insert", mac: 0x72, macName: "", vk: 0x2D, dik: "INSERT", sdl: 73, qt: qtInsert, qtName: "Insert", seq: "Ins", nkSDL: 124, nkWin: 124},
		"Delete":    {sym: 0xFFFF, xname: "Delete", mac: 0x75, macName: "Delete", vk: 0x2E, dik: "DELETE", sdl: 76, qt: qtDelete, qtName: "Delete", seq: "Del", nkSDL: 112, nkWin: 112},
		"Return":    {sym: 0xFF0D, xname: "Return", mac: 0x24, macName: "Return", vk: 0x0D, dik: "RETURN", sdl: 40, qt: qtReturn, qtName: "Return", seq: "Return", nkSDL: 66, nkWin: 66},
		"BackSpace": {sym: 0xFF08, xname: "BackSpace", mac: 0x33, macName: "Backspace", vk: 0x08, dik: "BACK", sdl: 42, qt: qtBackspace, qtName: "Backspace", seq: "Backspace", nkSDL: 67, nkWin: 67},
		"Tab":       {sym: 0xFF09, xname: "Tab", mac: 0x30, macName: "Tab", vk: 0x09, dik: "TAB", sdl: 43, qt: qtTab, qtName: "Tab", seq: "Tab", nkSDL: 61, nkWin: 61},
		"Escape":    {sym: 0xFF1B, xname: "Escape", mac: 0x35, macName: "Escape", vk: 0x1B, dik: "ESCAPE", sdl: 41, qt: qtEscape, qtName: "Escape", seq: "Esc", nkSDL: 111, nkWin: 111},
		"space":     {sym: 0x20, xname: "space", mac: 0x31, macName: "Space", vk: 0x20, dik: "SPACE", sdl: 44, qt: 0x20, qtName: "Space", seq: "Space", nkSDL: 62, nkWin: 62},
		"minus":     {sym: 0x2D, xname: "minus", mac: 0x1B, macName: "-", vk: 0xBD, dik: "MINUS", sdl: 45, qt: 0x2D, qtName: "Minus", seq: "-", nkSDL: 69, nkWin: 69},
		// PPSSPP's Windows table sends VK_OEM_PLUS to NKCODE_PLUS, its SDL one SDLK_EQUALS to NKCODE_EQUALS.
		"equal":        {sym: 0x3D, xname: "equal", mac: 0x18, macName: "=", vk: 0xBB, dik: "EQUALS", sdl: 46, qt: 0x3D, qtName: "Equal", seq: "=", nkSDL: 70, nkWin: 81},
		"bracketleft":  {sym: 0x5B, xname: "bracketleft", mac: 0x21, macName: "[", vk: 0xDB, dik: "LBRACKET", sdl: 47, qt: 0x5B, qtName: "BracketLeft", seq: "[", nkSDL: 71, nkWin: 71},
		"bracketright": {sym: 0x5D, xname: "bracketright", mac: 0x1E, macName: "]", vk: 0xDD, dik: "RBRACKET", sdl: 48, qt: 0x5D, qtName: "BracketRight", seq: "]", nkSDL: 72, nkWin: 72},
		"backslash":    {sym: 0x5C, xname: "backslash", mac: 0x2A, macName: `\`, vk: 0xDC, dik: "BACKSLASH", sdl: 49, qt: 0x5C, qtName: "Backslash", seq: `\`, nkSDL: 73, nkWin: 73},
		"semicolon":    {sym: 0x3B, xname: "semicolon", mac: 0x29, macName: ";", vk: 0xBA, dik: "SEMICOLON", sdl: 51, qt: 0x3B, qtName: "Semicolon", seq: ";", nkSDL: 74, nkWin: 74},
		"apostrophe":   {sym: 0x27, xname: "apostrophe", mac: 0x27, macName: "'", vk: 0xDE, dik: "APOSTROPHE", sdl: 52, qt: 0x27, qtName: "Apostrophe", seq: "'", nkSDL: 75, nkWin: 75},
		"grave":        {sym: 0x60, xname: "grave", mac: 0x32, macName: "Tilde", vk: 0xC0, dik: "GRAVE", sdl: 53, qt: 0x60, qtName: "QuoteLeft", seq: "`", nkSDL: 68, nkWin: 68},
		"comma":        {sym: 0x2C, xname: "comma", mac: 0x2B, macName: ",", vk: 0xBC, dik: "COMMA", sdl: 54, qt: 0x2C, qtName: "Comma", seq: ",", nkSDL: 55, nkWin: 55},
		"period":       {sym: 0x2E, xname: "period", mac: 0x2F, macName: ".", vk: 0xBE, dik: "PERIOD", sdl: 55, qt: 0x2E, qtName: "Period", seq: ".", nkSDL: 56, nkWin: 56},
		"slash":        {sym: 0x2F, xname: "slash", mac: 0x2C, macName: "/", vk: 0xBF, dik: "SLASH", sdl: 56, qt: 0x2F, qtName: "Slash", seq: "/", nkSDL: 76, nkWin: 76},

		// The keypad's other keys. Their X names are their own at level 0.
		"KP_Add":      {sym: 0xFFAB, xname: "KP_Add", mac: 0x45, macName: "Keypad +", vk: 0x6B, dik: "ADD", sdl: 87, qt: 0x2B, pad: true, qtName: "Plus", seq: "+", nkSDL: 157, nkWin: 157},
		"KP_Subtract": {sym: 0xFFAD, xname: "KP_Subtract", mac: 0x4E, macName: "Keypad -", vk: 0x6D, dik: "SUBTRACT", sdl: 86, qt: 0x2D, pad: true, qtName: "Minus", seq: "-", nkSDL: 156, nkWin: 156},
		"KP_Multiply": {sym: 0xFFAA, xname: "KP_Multiply", mac: 0x43, macName: "Keypad *", vk: 0x6A, dik: "MULTIPLY", sdl: 85, qt: 0x2A, pad: true, qtName: "Asterisk", seq: "*", nkSDL: 155, nkWin: 155},
		"KP_Divide":   {sym: 0xFFAF, xname: "KP_Divide", mac: 0x4B, macName: "Keypad /", vk: 0x6F, dik: "DIVIDE", sdl: 84, qt: 0x2F, pad: true, qtName: "Slash", seq: "/", nkSDL: 154, nkWin: 154},
		// KP_Decimal's key is [KP_Delete, KP_Decimal] in xkb, so Dolphin's XInput2 calls it KP_Delete.
		"KP_Decimal": {sym: 0xFFAE, xname: "KP_Delete", mac: 0x41, macName: "Keypad .", vk: 0x6E, dik: "DECIMAL", sdl: 99, qt: 0x2E, pad: true, qtName: "Period", seq: ".", nkSDL: 158, nkWin: 158},
		// Windows has no virtual key of its own for the keypad's Enter: it is VK_RETURN with the
		// extended flag, which PPSSPP's RawInput table can't tell from Return, and Cemu sees VK_RETURN.
		"KP_Enter": {sym: 0xFF8D, xname: "KP_Enter", mac: 0x4C, macName: "Keypad Enter", vk: vkKPEnter, dik: "NUMPADENTER", sdl: 88, qt: qtEnter, pad: true, qtName: "Enter", seq: "Enter", nkSDL: 160, nkWin: 66},
	} {
		keys[name] = k
	}
}

// vkKPEnter is what HostKey gives for the keypad's Enter on Windows: VK_RETURN with 0x100 set, so
// the Windows injector can tell it from Return and send it with KEYEVENTF_EXTENDEDKEY.
const vkKPEnter = 0x10D

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}

// lookup finds a key by its X keysym name.
func lookup(keysym string) (hostKey, bool) {
	if a, ok := aliases[keysym]; ok {
		keysym = a
	}
	k, ok := keys[keysym]
	return k, ok
}

// HostKey says which key code the injector for goos presses for an X keysym name, so mapping and
// injection agree: on "darwin" a macOS virtual key code (kVK_*), on "windows" a virtual-key code
// (VK_*; the keypad's Enter is 0x10D, VK_RETURN marked as extended), on "linux" the keysym's
// value. Only unshifted keys of a US keyboard are known: a keysym that needs Shift (an upper-case
// letter, "exclam") would press a modifier the emulator then sees too.
func HostKey(goos, keysym string) (code int, ok bool) {
	k, ok := lookup(keysym)
	if !ok {
		return 0, false
	}
	switch goos {
	case "darwin":
		return k.mac, true
	case "windows":
		return k.vk, true
	case "linux":
		return k.sym, true
	}
	return 0, false
}

// SDLScancode is the SDL scancode (USB HID usage) of the key an X keysym names, the same on every
// host since SDL reads scancodes.
func SDLScancode(keysym string) (int, bool) {
	k, ok := lookup(keysym)
	return k.sdl, ok
}

// QtKey is the value a Qt program reads from QKeyEvent::key() | modifiers for the key an X keysym
// names on goos: the Qt::Key, with Qt::KeypadModifier for the keypad's keys and, on macOS, for
// the arrow keys too (macOS marks arrows as keypad keys, and Qt passes that on).
func QtKey(goos, keysym string) (int, bool) {
	k, ok := lookup(keysym)
	if !ok {
		return 0, false
	}
	q := k.qtFor(goos)
	if k.padFor(goos) {
		q |= qtKeypadModifier
	}
	return q, true
}

// qtFor is the Qt::Key on goos: a Mac's Help key, where Insert would be, is Qt::Key_Help.
func (k hostKey) qtFor(goos string) int {
	if goos == "darwin" && k.qt == qtInsert {
		return qtHelp
	}
	return k.qt
}

func (k hostKey) padFor(goos string) bool {
	if goos == "darwin" && (k.qt == qtUp || k.qt == qtDown || k.qt == qtLeft || k.qt == qtRight) && !k.pad {
		return true
	}
	return k.pad
}
