package host

import (
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
)

// A game whose rate is close to its screen's keeps to the screen, so no frame is dropped or
// shown twice; one that isn't keeps its own speed.
func TestAFrameLastsAsLongAsTheScreenWantsWhenItsClose(t *testing.T) {
	for _, c := range []struct {
		game, screen, want float64
	}{
		{60.0988, 60, 60},       // NES, SNES: 0.16% slower
		{59.8261, 59.94, 59.94}, // DS on a TV
		{60.0988, 0, 60.0988},   // no screen given
		{50.007, 60, 50.007},    // PAL on 60 Hz: far too far, so its own speed
		{60, 120, 60},           // a multiple is the display's business, not the game's
		{0, 60, 60},             // a core that said nothing is taken as 60
	} {
		got := framePeriod(c.game, c.screen)
		want := time.Duration(float64(time.Second) / c.want)
		if got != want {
			t.Errorf("game %v, screen %v: %v, want %v", c.game, c.screen, got, want)
		}
	}
	if ipc.DisplayPaceLimit != 0.01 {
		t.Error("the limit is documented as 1%")
	}
}

func TestTheScreenIsSetAndCleared(t *testing.T) {
	c := newControl(nil)
	hz := 59.94
	c.apply(ipc.Control{DisplayHz: &hz})
	if c.displayHz != 59.94 {
		t.Fatalf("displayHz %v", c.displayHz)
	}
	for _, bad := range []float64{0, -1, 5000} {
		c.apply(ipc.Control{DisplayHz: &bad})
		if c.displayHz != 0 {
			t.Errorf("%v should clear it, got %v", bad, c.displayHz)
		}
	}
}
