// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"net/netip"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestRateLadder(t *testing.T) {
	l := newRateLadder(6000)
	at := time.Unix(1000, 0)
	tick := func(loss float64) (int, bool) {
		at = at.Add(time.Second)
		return l.report(loss, at)
	}
	// One bad report is a blip, not a reason to freeze the picture.
	if _, changed := tick(0.3); changed {
		t.Fatal("one bad report changed the bitrate")
	}
	tick(0)
	// Loss that stays brings it down a step.
	tick(0.2)
	tick(0.2)
	kbps, changed := tick(0.2)
	if !changed || kbps != 4200 {
		t.Fatalf("after lasting loss: %d %v", kbps, changed)
	}
	// And not again at once, however bad: each change is a restart.
	for range 10 {
		if _, changed := tick(0.5); changed {
			t.Fatal("two changes inside the hold")
		}
	}
	// It never goes below what's worth watching.
	for range 600 {
		tick(0.5)
	}
	if l.now != minKbps {
		t.Fatalf("the floor: %d", l.now)
	}
	// A long clean stretch brings it back, a step at a time, and never past where it started.
	for range 3600 {
		tick(0)
	}
	if l.now != 6000 {
		t.Fatalf("after a long clean stretch: %d", l.now)
	}
	if _, changed := tick(0); changed {
		t.Fatal("it went past its starting bitrate")
	}
	// Middling loss holds where it is.
	for range 100 {
		if _, changed := tick(0.04); changed {
			t.Fatal("middling loss changed the bitrate")
		}
	}
}

// TestRelayPushAndEnd: packets that arrive through the local sandbox's socket are taken, a
// relay that has ended takes none, and nobody watching is noticed.
func TestRelayPushAndEnd(t *testing.T) {
	m := NewManager(Config{LoopbackOnly: true})
	r, err := m.StartRelay(RelayOptions{Emulator: "test", Width: 640, Height: 480, FPS: 60, Bind: netip.MustParseAddr("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	pkt, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 102, SequenceNumber: 1}, Payload: []byte{1, 2, 3}}).Marshal()
	r.Push(true, pkt)
	r.Push(false, pkt)
	r.Push(true, []byte{1}) // not RTP: dropped
	if r.Packets() != 2 {
		t.Fatalf("%d packets taken, want 2", r.Packets())
	}
	if r.Unwatched() <= 0 {
		t.Fatal("a relay nobody has connected to counts as watched")
	}
	r.End(EndIdle, "This game was stopped.")
	select {
	case <-r.Done():
	default:
		t.Fatal("End didn't stop the relay")
	}
	r.Push(true, pkt)
	if r.Packets() != 2 || m.GetRelay(r.ID()) != nil {
		t.Fatal("an ended relay still takes packets")
	}
}
