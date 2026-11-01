package host

import (
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
)

// readStamp does what web/latency.ts does: it looks at the middle of each cell.
func readStamp(frame []byte, w, h, bpp int) (seq, input uint16) {
	cw, ch := stampCell(w, h)
	var bits uint32
	for i := 0; i < StampBits; i++ {
		p := frame[(ch/2)*w*bpp+(i*cw+cw/2)*bpp:]
		bits <<= 1
		if p[0] > 128 {
			bits |= 1
		}
	}
	return uint16(bits >> 16), uint16(bits)
}

func TestTheNumbersInAPictureReadBack(t *testing.T) {
	for _, c := range []struct{ w, h, format int }{{256, 224, engine.FormatXRGB8888}, {320, 240, engine.FormatRGB565}, {1280, 896, engine.FormatXRGB8888}, {256, 384, engine.Format0RGB1555}} {
		bpp := engine.BytesPerPixel(c.format)
		frame := make([]byte, c.w*c.h*bpp)
		for i := range frame {
			frame[i] = 0x55
		}
		stampFrame(frame, c.w, c.h, c.format, ipc.FrameTiming{Seq: 0x1234ABCD, InputSeq: 0xF00F})
		seq, input := readStamp(frame, c.w, c.h, bpp)
		if seq != 0xABCD || input != 0xF00F {
			t.Fatalf("%+v: read %x and %x", c, seq, input)
		}
		// The rest of the picture is left alone.
		if frame[len(frame)-1] != 0x55 {
			t.Fatalf("%+v: the stamp reached the bottom", c)
		}
	}
}

func TestAPictureTooSmallForTheStripIsLeftAlone(t *testing.T) {
	frame := make([]byte, 16*16*4)
	stampFrame(frame, 16, 16, engine.FormatXRGB8888, ipc.FrameTiming{Seq: 0xFFFFFFFF, InputSeq: 0xFFFF})
	for _, b := range frame {
		if b != 0 {
			t.Fatal("it drew anyway")
		}
	}
}

func TestAPressEndsTheWaitForTheNextFrame(t *testing.T) {
	r := &runner{}
	r.pads.pressed = make(chan struct{}, 1)
	r.ctl = newControl(r)
	r.pads.set(0, engine.PadState{Buttons: 1}, 0)
	start := time.Now()
	r.waitForFrame(200*time.Millisecond, 200*time.Millisecond)
	if took := time.Since(start); took > 100*time.Millisecond {
		t.Fatalf("it waited %v with a press waiting", took)
	}
	// The same state again isn't a press.
	r.pads.set(0, engine.PadState{Buttons: 1}, 0)
	start = time.Now()
	r.waitForFrame(50*time.Millisecond, 50*time.Millisecond)
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Fatalf("it only waited %v with nothing new", took)
	}
}

func TestAFrameAlreadyAheadOfItsTurnIsNotBroughtForwardAgain(t *testing.T) {
	r := &runner{}
	r.pads.pressed = make(chan struct{}, 1)
	r.ctl = newControl(r)
	r.pads.set(0, engine.PadState{Buttons: 1}, 0)
	start := time.Now()
	r.waitForFrame(60*time.Millisecond, 20*time.Millisecond) // more than a whole frame to go
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Fatalf("it only waited %v", took)
	}
}

func TestASteadyGameIgnoresPresses(t *testing.T) {
	r := &runner{}
	r.pads.pressed = make(chan struct{}, 1)
	r.ctl = newControl(r)
	r.ctl.noEarly = true
	r.pads.set(0, engine.PadState{Buttons: 1}, 0)
	start := time.Now()
	r.waitForFrame(40*time.Millisecond, 40*time.Millisecond)
	if took := time.Since(start); took < 30*time.Millisecond {
		t.Fatalf("it only waited %v", took)
	}
}

func TestSoundFromAChipIsThinnedToWhatCanBeUsed(t *testing.T) {
	r := &runner{}
	if got := r.audioRate(48000); got != 48000 || r.audioStep != 1 {
		t.Fatalf("an ordinary rate became %v, step %d", got, r.audioStep)
	}
	got := r.audioRate(2097152)
	if r.audioStep != 43 || got < 48000 || got > 49000 {
		t.Fatalf("a chip's rate became %v, step %d", got, r.audioStep)
	}
	// Twice the step's worth, in two uneven pieces, gives two samples of the average.
	in := make([]int16, r.audioStep*2*2)
	for i := range in {
		if i%2 == 0 {
			in[i] = 1000
		} else {
			in[i] = -1000
		}
	}
	out := append([]int16(nil), r.thinAudio(in[:10])...)
	out = append(out, r.thinAudio(in[10:])...)
	if len(out) != 4 || out[0] != 1000 || out[1] != -1000 || out[2] != 1000 || out[3] != -1000 {
		t.Fatalf("got %v", out)
	}
}

func TestFrameShiftsAreBounded(t *testing.T) {
	r := &runner{}
	r.pads.pressed = make(chan struct{}, 1)
	c := newControl(r)
	c.frameTime = 16 * time.Millisecond
	big := 100000.0
	c.apply(ipc.Control{ShiftMs: &big})
	if c.shift != c.frameTime {
		t.Fatalf("shifted by %v", c.shift)
	}
	c.shift = 0
	nan := big - big
	nan = nan / nan
	c.apply(ipc.Control{ShiftMs: &nan})
	if c.shift != 0 {
		t.Fatalf("NaN shifted by %v", c.shift)
	}
}

func TestThePadsSayWhichInputTheCoreLookedAt(t *testing.T) {
	var p pads
	p.set(0, engine.PadState{Buttons: 1}, 41)
	if seq, _ := p.seen(); seq != 0 {
		t.Fatalf("the core hasn't looked yet, and it says %d", seq)
	}
	p.poll()
	if seq, at := p.seen(); seq != 41 || at == 0 {
		t.Fatalf("got %d at %d", seq, at)
	}
	p.set(0, engine.PadState{Buttons: 3}, 0) // no number: the last one still stands
	p.poll()
	if seq, _ := p.seen(); seq != 41 {
		t.Fatalf("got %d", seq)
	}
}
