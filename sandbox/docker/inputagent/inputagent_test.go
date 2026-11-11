// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package inputagent

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
)

type sink struct {
	mu     sync.Mutex
	events []string
}

func (s *sink) Key(name string, down bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := "up"
	if down {
		d = "down"
	}
	s.events = append(s.events, name+" "+d)
	return nil
}

func (s *sink) Pointer(x, y float64, pressed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "pointer")
	return nil
}

func (s *sink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.events
	s.events = nil
	slices.Sort(out)
	return out
}

func pad(port byte, buttons uint16, lx int16) []byte {
	p := make([]byte, ipc.InputSize)
	p[0] = port
	p[1], p[2] = byte(buttons), byte(buttons>>8)
	p[3], p[4] = byte(uint16(lx)), byte(uint16(lx)>>8)
	return p
}

func TestAgentSendsOnlyChanges(t *testing.T) {
	s := &sink{}
	a := &Agent{Sink: s}
	_ = a.Handle(pad(0, 1<<8|1<<3, 0)) // A and Start
	if got := s.take(); !slices.Equal(got, []string{"Return down", "x down"}) {
		t.Fatalf("%v", got)
	}
	_ = a.Handle(pad(0, 1<<8, 0)) // Start let go
	if got := s.take(); !slices.Equal(got, []string{"Return up"}) {
		t.Fatalf("%v", got)
	}
	_ = a.Handle(pad(0, 1<<8, -30000)) // stick left
	if got := s.take(); !slices.Equal(got, []string{"j down"}) {
		t.Fatalf("%v", got)
	}
	_ = a.Handle(pad(1, 1<<8, 0)) // the second player's A is a key of its own
	if got := s.take(); !slices.Equal(got, []string{"KP_3 down"}) {
		t.Fatalf("%v", got)
	}
	_ = a.Handle(pad(7, 0xffff, 0)) // there's no eighth player
	if got := s.take(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	a.Release()
	if got := s.take(); !slices.Equal(got, []string{"KP_3 up", "j up", "x up"}) {
		t.Fatalf("%v", got)
	}
	if a.Handle([]byte{1, 2}) == nil {
		t.Fatal("garbage accepted")
	}
}

func TestServeNeedsTheSecret(t *testing.T) {
	secret := strings.Repeat("s", 32)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sink{}
	go func() { _ = Serve(ctx, l, secret, s) }()

	wrong := &Forwarder{Addr: l.Addr().String(), Secret: strings.Repeat("x", 32)}
	wrong.Send(pad(0, 1<<8, 0))
	time.Sleep(100 * time.Millisecond)
	wrong.Close()
	time.Sleep(50 * time.Millisecond)
	if got := s.take(); len(got) != 0 {
		t.Fatalf("the wrong secret got through: %v", got)
	}

	f := &Forwarder{Addr: l.Addr().String(), Secret: secret}
	defer f.Close()
	f.Send(pad(0, 1<<8, 0))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.take(); len(got) > 0 {
			if !slices.Equal(got, []string{"x down"}) {
				t.Fatalf("%v", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing arrived")
}

// TestPlayersNeverShareAKey: four players' pads can't press each other's buttons, whatever the
// default map becomes.
func TestPlayersNeverShareAKey(t *testing.T) {
	seen := map[string]int{}
	for p, m := range DefaultKeys() {
		if len(m) < 16 {
			t.Errorf("player %d has keys for only %d buttons", p+1, len(m))
		}
		for button, key := range m {
			if !slices.Contains(Buttons, button) || !keyName.MatchString(key) {
				t.Errorf("player %d: %s -> %q", p+1, button, key)
			}
			if other, ok := seen[key]; ok {
				t.Errorf("%q is both player %d's and player %d's", key, other+1, p+1)
			}
			seen[key] = p
		}
	}
}

func TestParseKeys(t *testing.T) {
	km, err := ParseKeys(`[{"a":"c","b":""},{},{"start":"F13"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if km[0]["a"] != "c" || km[0]["y"] != "a" || km[2]["start"] != "F13" || km[1]["a"] != "KP_3" {
		t.Errorf("%v", km)
	}
	if _, ok := km[0]["b"]; ok {
		t.Error("an emptied key is still pressed")
	}
	for _, bad := range []string{`[{"a":"x y"}]`, `[{"a":"--window"}]`, `[{"a":"$(reboot)"}]`, `[{"fire":"z"}]`, `[{},{},{},{},{}]`, `{"a":"z"}`, `nope`} {
		if _, err := ParseKeys(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
	// The agent uses the map it's given.
	s := &sink{}
	a := &Agent{Sink: s, Keys: &km}
	_ = a.Handle(pad(0, 1<<8|1<<0, 0)) // A and B: B has no key now
	if got := s.take(); !slices.Equal(got, []string{"c down"}) {
		t.Fatalf("%v", got)
	}
}

func TestBitrateMessage(t *testing.T) {
	var got []int
	a := &Agent{Sink: &sink{}, Bitrate: func(k int) { got = append(got, k) }}
	if err := a.Handle(BitrateMessage(4200)); err != nil || !slices.Equal(got, []int{4200}) {
		t.Fatalf("%v %v", err, got)
	}
	// It can't be mistaken for input, and input can't be mistaken for it.
	if len(BitrateMessage(1)) == ipc.InputSize || len(BitrateMessage(1)) == ipc.PointerSize {
		t.Fatal("a bitrate message is the size of an input message")
	}
	_ = a.Handle(pad(0, 0xB7, 0))
	if len(got) != 1 {
		t.Fatalf("a pad message changed the bitrate: %v", got)
	}
}

// TestBridge: the local sandbox's one socket carries RTP up and input down, and gives nothing
// to a connection without the secret.
func TestBridge(t *testing.T) {
	secret := strings.Repeat("s", 32)
	dir, err := os.MkdirTemp("", "wpb")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "host.sock")
	type pkt struct {
		kind byte
		data string
	}
	got := make(chan pkt, 8)
	h := &Host{Secret: secret, OnPacket: func(kind byte, p []byte) { got <- pkt{kind, string(p)} }}
	if err := h.Listen(path); err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	// A stranger on the socket is hung up on, and nothing it sends is taken.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte(strings.Repeat("x", 32) + "\nv\x00\x03abc"))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("a stranger was kept on the line")
	}
	c.Close()

	video, audio := freeUDP(t), freeUDP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &sink{}
	done := make(chan error, 1)
	go func() { done <- Bridge(ctx, path, secret, video, audio, &Agent{Sink: s}) }()

	send := func(port int, data string) {
		u, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			t.Fatal(err)
		}
		defer u.Close()
		_, _ = u.Write([]byte(data))
	}
	deadline := time.Now().Add(5 * time.Second)
	var first pkt
loop:
	for {
		send(video, "picture")
		select {
		case first = <-got:
			break loop
		case <-time.After(50 * time.Millisecond):
			if time.Now().After(deadline) {
				t.Fatal("nothing came up the bridge")
			}
		}
	}
	if first.kind != KindVideo || first.data != "picture" {
		t.Fatalf("%c %q", first.kind, first.data)
	}
	send(audio, "sound")
	for {
		select {
		case p := <-got:
			if p.kind == KindAudio {
				if p.data != "sound" {
					t.Fatalf("%q", p.data)
				}
				goto down
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the sound didn't come up")
		}
	}
down:
	h.Send(pad(0, 1<<8, 0))
	for time.Now().Before(deadline) {
		if ev := s.take(); len(ev) > 0 {
			if !slices.Equal(ev, []string{"x down"}) {
				t.Fatalf("%v", ev)
			}
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("input didn't come down the bridge")
}

func freeUDP(t *testing.T) int {
	t.Helper()
	u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	return u.LocalAddr().(*net.UDPAddr).Port
}
