// Package inputagent carries a player's input into a standalone emulator's container. WittyPlay
// sends the same pad, pointer and touch messages a game runner gets (package ipc) over TCP to a
// small agent inside the container, which turns them into key and mouse events on the
// container's X display with xdotool.
//
// Why keys and xdotool, not uinput: a virtual pad through /dev/uinput needs that device in the
// container, which the hardening refuses, and a program that reads raw pads would see the
// host's too. Every desktop emulator takes the keyboard, and its own settings map keys to the
// console's buttons. The agent's key map is in Keys; an image sets the emulator's settings to
// match. Analogue sticks become their directions' keys past a threshold, which is enough for
// menus and most games and is the known limit of this agent.
//
// The protocol: the client sends the session's secret and a newline, then frames of one length
// byte and that many bytes. The agent closes a connection whose first line is wrong, and takes
// one connection at a time.
package inputagent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
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

// Keys maps the libretro joypad buttons (bit numbers in a pad message) to X key names, for the
// first player. An image configures its emulator to these.
var Keys = map[int]string{
	0:  "z",         // B
	1:  "a",         // Y
	2:  "BackSpace", // Select
	3:  "Return",    // Start
	4:  "Up",
	5:  "Down",
	6:  "Left",
	7:  "Right",
	8:  "x", // A
	9:  "s", // X
	10: "q", // L
	11: "w", // R
	12: "e", // L2
	13: "r", // R2
	14: "d", // L3
	15: "f", // R3
}

// Stick keys: the left stick's directions, past stickThreshold.
var stickKeys = [4]string{"i", "k", "j", "l"} // up, down, left, right

const stickThreshold = 16384

// Agent turns messages into Sink calls, remembering what's held so it only sends changes.
type Agent struct {
	Sink Sink
	held map[string]bool
}

// Handle applies one message. Messages for players other than the first are ignored: this agent
// gives one player a keyboard.
func (a *Agent) Handle(p []byte) error {
	if a.held == nil {
		a.held = map[string]bool{}
	}
	switch {
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
	port, buttons, lx, ly, _, _, ok := ipc.ParseInput(p)
	if !ok {
		return errors.New("not a message this agent knows")
	}
	if port != 0 {
		return nil
	}
	want := map[string]bool{}
	for bit, key := range Keys {
		if buttons&(1<<bit) != 0 {
			want[key] = true
		}
	}
	if ly < -stickThreshold {
		want[stickKeys[0]] = true
	}
	if ly > stickThreshold {
		want[stickKeys[1]] = true
	}
	if lx < -stickThreshold {
		want[stickKeys[2]] = true
	}
	if lx > stickThreshold {
		want[stickKeys[3]] = true
	}
	var errs []error
	for key := range a.held {
		if !want[key] {
			errs = append(errs, a.Sink.Key(key, false))
			delete(a.held, key)
		}
	}
	for key := range want {
		if !a.held[key] {
			errs = append(errs, a.Sink.Key(key, true))
			a.held[key] = true
		}
	}
	return errors.Join(errs...)
}

// Release lets go of everything, when a connection ends.
func (a *Agent) Release() {
	for key := range a.held {
		_ = a.Sink.Key(key, false)
	}
	a.held = map[string]bool{}
}

// Serve accepts one connection at a time on l, checks its secret, and applies what it sends.
func Serve(ctx context.Context, l net.Listener, secret string, sink Sink) error {
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
		serveConn(c, secret, &Agent{Sink: sink})
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
