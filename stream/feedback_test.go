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
	c.note(0, 90*time.Millisecond, now)
	now = now.Add(time.Second)
	if !c.note(0, 90*time.Millisecond, now) {
		t.Fatal("round trips growing like that mean the network is full")
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
