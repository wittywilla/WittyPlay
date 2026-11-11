// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"testing"
	"time"
)

func TestTheRateComesDownQuicklyAndGoesUpSlowly(t *testing.T) {
	var c rateControl
	now := time.Now()
	step := func() bool { now = now.Add(time.Second); return c.note(0, 5*time.Millisecond, now) }
	if step() || c.rate() != 1 {
		t.Fatalf("a calm network shouldn't change anything: %v", c.rate())
	}
	// Lost packets, reported twice.
	now = now.Add(rateChangeEvery)
	if c.note(0.1, 5*time.Millisecond, now) {
		t.Fatal("one bad report might be nothing")
	}
	now = now.Add(time.Second)
	if !c.note(0.1, 5*time.Millisecond, now) || c.rate() != rateDown {
		t.Fatalf("it should have stepped down, to %v", c.rate())
	}
	// Still bad, but it has only just changed.
	now = now.Add(time.Second)
	c.note(0.1, 5*time.Millisecond, now)
	now = now.Add(time.Second)
	if c.note(0.1, 5*time.Millisecond, now) {
		t.Fatal("changes are kept apart")
	}
	// Calm again: not straight away.
	for i := 0; i < 15; i++ {
		if step() {
			t.Fatal("it went up too soon")
		}
	}
	went := false
	for i := 0; i < 15 && !went; i++ {
		went = step()
	}
	if !went || c.rate() <= rateDown {
		t.Fatalf("it never recovered: %v", c.rate())
	}
	for i := 0; i < 200; i++ {
		step()
	}
	if c.rate() != 1 {
		t.Fatalf("it should end up back at full rate, not %v", c.rate())
	}
}

func TestFillingQueuesCountEvenWithNothingLost(t *testing.T) {
	var c rateControl
	now := time.Now()
	c.note(0, 10*time.Millisecond, now)
	now = now.Add(rateChangeEvery)
	changed := false
	for i := 0; i < rateRTTReports+1 && !changed; i++ {
		now = now.Add(time.Second)
		changed = c.note(0, 90*time.Millisecond, now)
	}
	if !changed {
		t.Fatal("round trips that stay grown like that mean the network is full")
	}
}

// A busy machine answers late now and then. Measured on loopback: 45 ms, 133 ms, 3.6 s and
// 4.3 s against a usual 1 to 3, a report or two at a time.
func TestABusyMachineIsNotAFullNetwork(t *testing.T) {
	var c rateControl
	now := time.Now()
	calm := 2 * time.Millisecond
	for _, rtt := range []time.Duration{calm, calm, 45 * time.Millisecond, 133 * time.Millisecond, calm, 3600 * time.Millisecond, 4300 * time.Millisecond, calm, calm, 73 * time.Millisecond, 45 * time.Millisecond, 60 * time.Millisecond, calm} {
		now = now.Add(time.Second)
		if c.note(0, rtt, now) {
			t.Fatalf("a %v round trip changed the rate to %v", rtt, c.rate())
		}
	}
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		if c.note(0, 4*time.Second, now) {
			t.Fatal("a round trip of seconds on a network that usually takes milliseconds is a stalled machine")
		}
	}
}

func TestTheRateHasAFloor(t *testing.T) {
	var c rateControl
	now := time.Now()
	for i := 0; i < 100; i++ {
		now = now.Add(3 * time.Second)
		c.note(0.5, 0, now)
	}
	if c.rate() != rateFloor {
		t.Fatalf("it reached %v", c.rate())
	}
}
