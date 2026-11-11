package inputagent

import (
	"context"
	"net"
	"slices"
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
	_ = a.Handle(pad(1, 0xffff, 0)) // another player: ignored
	if got := s.take(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	a.Release()
	if got := s.take(); !slices.Equal(got, []string{"j up", "x up"}) {
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
