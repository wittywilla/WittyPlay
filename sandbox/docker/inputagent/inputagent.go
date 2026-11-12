// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package inputagent carries a player's input into a standalone emulator's container. WittyPlay
// sends the same pad, pointer and touch messages a game runner gets (package ipc) over TCP to a
// small agent inside the container, which turns them into key and mouse events on the
// container's X display with xdotool.
//
// Why keys and xdotool, not uinput: a virtual pad through /dev/uinput needs that device in the
// container, which the hardening refuses, and a program that reads raw pads would see the
// host's too. Every desktop emulator takes the keyboard, and its own settings map keys to the
// console's buttons. Each of four players has keys of their own (KeyMap), which an
// administrator can change; an image sets the emulator's settings to match. Analogue sticks
// become their directions' keys past a threshold, which is enough for menus and most games and
// is the known limit of this agent. There's no way back for rumble: a keyboard has none.
//
// The protocol: the client sends the session's secret and a newline, then frames of one length
// byte and that many bytes. The agent closes a connection whose first line is wrong, and takes
// one connection at a time.
package inputagent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
)

// MaxFrame is the largest message: a touch message with every finger.
const MaxFrame = 1 + 1 + ipc.MaxTouches*ipc.TouchSize

// ---------------------------------------------------------------- the host's end

// Forwarder sends input to one container's agent, connecting when it first has something to send
// and again after a failure. Send never blocks the caller for long: input that can't be sent is
// dropped, as a game drops a late frame.
type Forwarder struct {
	Addr   string // host:port
	Secret string
	Dial   func(ctx context.Context, network, addr string) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
	next time.Time // no reconnect before this
}

// Send passes one message on.
func (f *Forwarder) Send(p []byte) {
	if len(p) == 0 || len(p) > MaxFrame {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn == nil {
		if time.Now().Before(f.next) {
			return
		}
		if err := f.connectLocked(); err != nil {
			f.next = time.Now().Add(time.Second)
			return
		}
	}
	_ = f.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := f.conn.Write(append([]byte{byte(len(p))}, p...)); err != nil {
		f.conn.Close()
		f.conn = nil
	}
}

func (f *Forwarder) connectLocked() error {
	dial := f.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 2 * time.Second}).DialContext
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := dial(ctx, "tcp", f.Addr)
	if err != nil {
		return err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := io.WriteString(c, f.Secret+"\n"); err != nil {
		c.Close()
		return err
	}
	f.conn = c
	return nil
}

// Close lets go of the connection.
func (f *Forwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conn != nil {
		f.conn.Close()
		f.conn = nil
	}
}

// ---------------------------------------------------------------- the container's end

// Sink is what the agent does with input. XDoTool is the real one.
type Sink interface {
	Key(name string, down bool) error
	Pointer(x, y float64, pressed bool) error // 0..1 across the display
}

// MaxPlayers is how many pads the agent gives keys to.
const MaxPlayers = 4

// Buttons names a pad's buttons by their bit in a pad message (libretro's joypad order), and
// then the two sticks' directions, which become keys past stickThreshold. These names are the
// keys of a KeyMap.
var Buttons = []string{"b", "y", "select", "start", "up", "down", "left", "right", "a", "x", "l", "r", "l2", "r2", "l3", "r3",
	"lup", "ldown", "lleft", "lright", "rup", "rdown", "rleft", "rright"}

// KeyMap says which X key each button of each player presses. An administrator can change it
// (the "keys.p1" to "keys.p4" settings) to match the keys an emulator already knows.
type KeyMap [MaxPlayers]map[string]string

// DefaultKeys is the map used when nothing else is set. An emulator's own settings have to map
// these keys to its pads; an image ships settings that do. Player 4's use the function keys,
// which some emulators keep for themselves, so those are the first to change.
func DefaultKeys() KeyMap {
	row := func(keys ...string) map[string]string {
		m := map[string]string{}
		for i, k := range keys {
			if k != "" {
				m[Buttons[i]] = k
			}
		}
		return m
	}
	return KeyMap{
		row("z", "a", "BackSpace", "Return", "Up", "Down", "Left", "Right", "x", "s", "q", "w", "e", "r", "d", "f", "i", "k", "j", "l", "t", "g", "v", "b"),
		row("KP_2", "KP_1", "KP_Subtract", "KP_Add", "Home", "End", "Delete", "Next", "KP_3", "KP_5", "KP_4", "KP_6", "KP_7", "KP_9", "KP_0", "KP_Decimal",
			"KP_8", "KP_Divide", "KP_Multiply", "Prior", "c", "h", "Insert", "space"),
		row("1", "2", "3", "4", "5", "6", "7", "8", "9", "0", "m", "n", "o", "p", "u", "y", "minus", "equal", "bracketleft", "bracketright"),
		row("F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12", "semicolon", "apostrophe", "comma", "period", "slash", "backslash", "grave", "Tab"),
	}
}

var keyName = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// ParseKeys reads a KeyMap from JSON (a list of up to four objects, button name to X key name)
// laid over the defaults. A name that isn't a button, or a key that couldn't be an X key name,
// is refused: the key reaches xdotool's command line.
func ParseKeys(js string) (KeyMap, error) {
	km := DefaultKeys()
	if js == "" {
		return km, nil
	}
	var in []map[string]string
	if err := json.Unmarshal([]byte(js), &in); err != nil || len(in) > MaxPlayers {
		return km, errors.New("the key map is unreadable")
	}
	for p, m := range in {
		for button, key := range m {
			if !slices.Contains(Buttons, button) {
				return km, fmt.Errorf("there's no button named %q", button)
			}
			if key == "" {
				delete(km[p], button)
				continue
			}
			if !keyName.MatchString(key) {
				return km, fmt.Errorf("%q can't be a key's name", key)
			}
			km[p][button] = key
		}
	}
	return km, nil
}

const stickThreshold = 16384

// ControlMark begins a message that isn't input: ControlMark, then a bitrate in kbit/s as two
// bytes, low first. Its length is no pad, pointer or touch message's.
const (
	ControlMark = 0xB7
	ControlSize = 3
)

// BitrateMessage asks the agent to have the picture encoded at kbps from now on.
func BitrateMessage(kbps int) []byte {
	kbps = min(max(kbps, 1), 65535)
	return []byte{ControlMark, byte(kbps), byte(kbps >> 8)}
}

// Agent turns messages into Sink calls, remembering what's held so it only sends changes.
type Agent struct {
	Sink Sink
	// Keys is the key map; DefaultKeys if it was never set.
	Keys *KeyMap
	// Bitrate, if set, hears a request to change the encoder's bitrate.
	Bitrate func(kbps int)
	held    [MaxPlayers]map[string]bool
}

// Handle applies one message. Each of four players has keys of their own.
func (a *Agent) Handle(p []byte) error {
	if a.Keys == nil {
		km := DefaultKeys()
		a.Keys = &km
	}
	switch {
	case len(p) == ControlSize && p[0] == ControlMark:
		if a.Bitrate != nil {
			a.Bitrate(int(p[1]) | int(p[2])<<8)
		}
		return nil
	case len(p) == ipc.PointerSize && p[0] == ipc.PointerMark:
		x, y, pressed, ok := ipc.ParsePointer(p)
		if !ok {
			return errors.New("bad pointer")
		}
		return a.Sink.Pointer((float64(x)+32767)/65534, (float64(y)+32767)/65534, pressed)
	case len(p) > 0 && p[0] == ipc.TouchesMark:
		t, ok := ipc.ParseTouches(p)
		if !ok {
			return errors.New("bad touches")
		}
		if len(t) == 0 {
			return nil
		}
		return a.Sink.Pointer((float64(t[0].X)+32767)/65534, (float64(t[0].Y)+32767)/65534, t[0].Pressed)
	}
	port, buttons, lx, ly, rx, ry, ok := ipc.ParseInput(p)
	if !ok {
		return errors.New("not a message this agent knows")
	}
	if port < 0 || port >= MaxPlayers {
		return nil
	}
	keys := a.Keys[port]
	want := map[string]bool{}
	press := func(button string) {
		if k := keys[button]; k != "" {
			want[k] = true
		}
	}
	for bit := 0; bit < 16; bit++ {
		if buttons&(1<<bit) != 0 {
			press(Buttons[bit])
		}
	}
	for i, v := range []int16{ly, lx, ry, rx} {
		names := [4][2]string{{"lup", "ldown"}, {"lleft", "lright"}, {"rup", "rdown"}, {"rleft", "rright"}}[i]
		if v < -stickThreshold {
			press(names[0])
		}
		if v > stickThreshold {
			press(names[1])
		}
	}
	if a.held[port] == nil {
		a.held[port] = map[string]bool{}
	}
	held := a.held[port]
	var errs []error
	for key := range held {
		if !want[key] {
			errs = append(errs, a.Sink.Key(key, false))
			delete(held, key)
		}
	}
	for key := range want {
		if !held[key] {
			errs = append(errs, a.Sink.Key(key, true))
			held[key] = true
		}
	}
	return errors.Join(errs...)
}

// Release lets go of everything, when a connection ends.
func (a *Agent) Release() {
	for p := range a.held {
		for key := range a.held[p] {
			_ = a.Sink.Key(key, false)
		}
		a.held[p] = nil
	}
}

// Serve accepts one connection at a time on l, checks its secret, and applies what it sends.
func Serve(ctx context.Context, l net.Listener, secret string, sink Sink) error {
	return ServeWith(ctx, l, secret, func() *Agent { return &Agent{Sink: sink} })
}

// ServeWith is Serve with an agent of the caller's making for each connection: one with a key
// map, or one that hears bitrate requests.
func ServeWith(ctx context.Context, l net.Listener, secret string, agent func() *Agent) error {
	if len(secret) < 32 {
		return errors.New("the input agent needs a secret of 32 characters or more")
	}
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		serveConn(c, secret, agent())
	}
}

func serveConn(c net.Conn, secret string, a *Agent) {
	defer c.Close()
	defer a.Release()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReaderSize(c, 512)
	line, err := r.ReadSlice('\n')
	if err != nil || subtle.ConstantTimeCompare(line[:len(line)-1], []byte(secret)) != 1 {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	buf := make([]byte, MaxFrame)
	for {
		n, err := r.ReadByte()
		if err != nil {
			return
		}
		if n == 0 || int(n) > MaxFrame {
			return
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return
		}
		_ = a.Handle(buf[:n])
	}
}

// XDoTool sends events to the X display in DISPLAY through xdotool. Width and Height are the
// display's, for the pointer.
type XDoTool struct {
	Width, Height int
	pressed       bool
}

func (x *XDoTool) Key(name string, down bool) error {
	verb := "keyup"
	if down {
		verb = "keydown"
	}
	return exec.Command("xdotool", verb, "--", name).Run()
}

func (x *XDoTool) Pointer(fx, fy float64, pressed bool) error {
	px, py := int(fx*float64(x.Width)), int(fy*float64(x.Height))
	args := []string{"mousemove", "--", strconv.Itoa(px), strconv.Itoa(py)}
	if pressed != x.pressed {
		verb := "mouseup"
		if pressed {
			verb = "mousedown"
		}
		args = append(args, verb, "1")
		x.pressed = pressed
	}
	if err := exec.Command("xdotool", args...).Run(); err != nil {
		return fmt.Errorf("xdotool: %w", err)
	}
	return nil
}
