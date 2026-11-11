// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"encoding/binary"
	"math"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
)

func TestFramesAreFoundByNumberNotByCounting(t *testing.T) {
	var r traceRing
	for i := 0; i < 5; i++ {
		r.push(frameTrace{FrameTiming: ipc.FrameTiming{Seq: uint32(i + 100)}})
	}
	// The encoder dropped the first frame: frame 1 is what came out first.
	got, ok := r.take(1)
	if !ok || got.Seq != 101 {
		t.Fatalf("took %v, %v", got.Seq, ok)
	}
	if _, ok := r.take(0); ok {
		t.Fatal("a frame already gone came back")
	}
	if got, ok := r.take(3); !ok || got.Seq != 103 || r.len() != 1 {
		t.Fatalf("took %v, %v, left %d", got.Seq, ok, r.len())
	}
	if _, ok := r.take(9); ok {
		t.Fatal("a frame that never went in came out")
	}
}

func TestFrameIndexSurvivesMillisecondRounding(t *testing.T) {
	for _, fps := range []float64{59.94, 60, 60.0988, 50, 29.97} {
		for n := 0; n < 100000; n += 777 {
			ms := math.Round(float64(n) * 1000 / fps)
			if got := frameIndex(time.Duration(ms)*time.Millisecond, fps); got != n {
				t.Fatalf("at %v fps, frame %d came back as %d", fps, n, got)
			}
		}
	}
}

func TestARecordIsTheSizeAClientExpects(t *testing.T) {
	tr := frameTrace{FrameTiming: ipc.FrameTiming{Seq: 7, InputSeq: 9, InputAt: 1, RunStart: 2, RunEnd: 3}, InputServer: 4, ServerIn: 5, EncIn: 6, EncOut: 8, Sent: 10, RTP: 11, Size: 12, Key: true}
	b := tr.appendRecord(nil)
	if len(b) != traceRecordSize || b[0] != timingRecord || b[1] != 1 {
		t.Fatalf("got %d bytes, starting %x", len(b), b[:2])
	}
	if binary.LittleEndian.Uint32(b[2:]) != 7 || binary.LittleEndian.Uint32(b[6:]) != 9 || binary.LittleEndian.Uint32(b[10:]) != 11 {
		t.Fatalf("the numbers are out of place: %x", b[:18])
	}
	want := []int64{4, 1, 2, 3, 5, 6, 8, 10}
	for i, w := range want {
		if got := int64(binary.LittleEndian.Uint64(b[18+i*8:])); got != w {
			t.Fatalf("time %d is %d, not %d", i, got, w)
		}
	}
}

func TestTimingMessagesAreBoundedAndAnswered(t *testing.T) {
	v := &viewer{s: &Session{}}
	var reply []byte
	answer := func(b []byte) { reply = b }

	ping := make([]byte, 9)
	ping[0] = timingPing
	binary.LittleEndian.PutUint64(ping[1:], math.Float64bits(1234.5))
	if !v.timingMessage(ping, answer) || len(reply) != 17 || reply[0] != timingPong {
		t.Fatalf("a ping got %x", reply)
	}
	if math.Float64frombits(binary.LittleEndian.Uint64(reply[1:])) != 1234.5 {
		t.Fatal("a ping's own time should come back as it went")
	}
	reply = nil
	if !v.timingMessage(ping[:5], answer) || reply != nil {
		t.Fatal("a short ping should be swallowed, not answered")
	}
	if !v.timingMessage([]byte{timingSubscribe, 1}, answer) || !v.wantTiming.Load() {
		t.Fatal("subscribing did nothing")
	}
	if v.timingMessage([]byte{0, 1, 2}, answer) {
		t.Fatal("a pad's message was taken for a timing one")
	}
	if v.timingMessage(nil, answer) {
		t.Fatal("nothing was taken for something")
	}
}

func TestAnInputClockHoldsLittleWhateverItsSent(t *testing.T) {
	var c inputClock
	for i := uint32(1); i < 100000; i += 13 {
		c.note(i)
	}
	if c.when(0) != 0 {
		t.Fatal("0 means no number")
	}
	c.note(0)
	if c.when(99999) == 0 {
		t.Fatal("the newest should be there")
	}
}

func TestAnEncoderHoldsOneFrameOnceItHasStarted(t *testing.T) {
	e := &videoEncoder{got: make(chan struct{})}
	for i := 0; i < 16; i++ {
		if !e.roomForFrame() {
			t.Fatalf("it should take frames to get going, but stopped at %d", i)
		}
		e.inside.push(frameTrace{})
	}
	if e.roomForFrame() {
		t.Fatal("there should be a limit even before it has started")
	}
	close(e.got)
	if e.roomForFrame() {
		t.Fatal("once running it should hold one at a time")
	}
	e.inside.take(15)
	if !e.roomForFrame() {
		t.Fatal("with nothing inside there's room")
	}
}

func TestAStalledEncoderIsNoticed(t *testing.T) {
	now := time.Now()
	e := &videoEncoder{got: make(chan struct{}), started: now}
	if e.stalled(now.Add(time.Hour)) {
		t.Fatal("holding nothing isn't stalling")
	}
	e.inside.push(frameTrace{})
	if e.stalled(now.Add(encoderStart - time.Second)) {
		t.Fatal("it's allowed a while to start")
	}
	if !e.stalled(now.Add(encoderStart + time.Second)) {
		t.Fatal("one that never starts has stalled")
	}
	close(e.got)
	e.lastOut.Store(now.UnixNano())
	if e.stalled(now.Add(encoderStall - time.Second)) {
		t.Fatal("that's not long enough")
	}
	if !e.stalled(now.Add(encoderStall + time.Second)) {
		t.Fatal("one that has stopped has stalled")
	}
}
