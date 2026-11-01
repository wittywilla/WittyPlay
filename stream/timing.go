package stream

import (
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/wittywilla/wittyplay/ipc"
)

// How long each stage of the path takes, measured on real frames. Every frame carries a
// record of when it passed each stage on the server. A client that asks for them gets
// them over an unreliable channel, adds what it saw, and can then say where the time went
// on this game, on this machine, right now. web/latency.ts is the other half.
//
// A viewer that doesn't ask pays nothing but a few time readings.

// What the timing messages start with. A pad's first byte is its port and a pointer's is
// 0xFF, so these can share a WebSocket's channel with input.
const (
	timingPing      = 0xF1 // client: an f64 of its own clock
	timingPong      = 0xF2 // server: that f64, then an i64 of Unix nanoseconds
	timingSubscribe = 0xF3 // client: one byte, 1 to have frame records sent
	timingRecord    = 0xF4 // server: one frame's record
	timingLatency   = 0xF5 // client: one byte, a LatencyMode
	timingShift     = 0xF6 // client: an i16 of tenths of a millisecond to move the frames by
)

// frameTrace is one frame's journey, in Unix nanoseconds.
type frameTrace struct {
	ipc.FrameTiming
	InputServer int64 // when the input it saw reached the server
	ServerIn    int64 // when the whole picture had arrived from the runner
	EncIn       int64 // when it went to the encoder
	EncOut      int64 // when the encoded frame came back
	Sent        int64 // when the last packet had gone to the network
	RTP         uint32
	Size        int32 // encoded bytes
	Key         bool
}

const traceRecordSize = 1 + 1 + 4*4 + 8*8

// appendRecord writes the record a client reads: see parseRecord in web/latency.ts.
func (t *frameTrace) appendRecord(b []byte) []byte {
	b = append(b, timingRecord)
	if t.Key {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	for _, v := range []uint32{t.Seq, t.InputSeq, t.RTP, uint32(t.Size)} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	for _, v := range []int64{t.InputServer, t.InputAt, t.RunStart, t.RunEnd, t.ServerIn, t.EncIn, t.EncOut, t.Sent} {
		b = binary.LittleEndian.AppendUint64(b, uint64(v))
	}
	return b
}

// traceRing holds the frames inside an encoder, in order. What comes out says which frame
// it is by its timestamp, because counting would be wrong after any frame an encoder drops.
type traceRing struct {
	mu   sync.Mutex
	at   [128]frameTrace
	head int
	tail int
}

func (r *traceRing) push(t frameTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at[r.head%len(r.at)] = t
	r.head++
	if r.head-r.tail > len(r.at) {
		r.tail = r.head - len(r.at)
	}
}

// take finds the frame that was nth to go in, and forgets it and everything before it.
func (r *traceRing) take(n int) (frameTrace, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < r.tail || n >= r.head {
		return frameTrace{}, false
	}
	r.tail = n + 1
	return r.at[n%len(r.at)], true
}

// frameIndex turns a timestamp from the encoder into which frame that was, counting from
// the first one given to it at this frame rate.
func frameIndex(ts time.Duration, fps float64) int {
	return int(math.Round(ts.Seconds() * fps))
}

func (r *traceRing) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head - r.tail
}

// inputClock remembers when numbered inputs reached the server. Input numbers come from a
// client, so this only ever holds a few times, whatever it's sent.
type inputClock struct {
	at [64]atomic.Int64
}

func (c *inputClock) note(seq uint32) {
	if seq != 0 {
		c.at[seq%uint32(len(c.at))].Store(time.Now().UnixNano())
	}
}

func (c *inputClock) when(seq uint32) int64 {
	if seq == 0 {
		return 0
	}
	return c.at[seq%uint32(len(c.at))].Load()
}

// timingMessage answers what a client says about timing, and says whether that's what it
// was. reply is how an answer gets back.
func (v *viewer) timingMessage(p []byte, reply func([]byte)) bool {
	if len(p) == 0 {
		return false
	}
	switch p[0] {
	case timingPing:
		if len(p) != 9 {
			return true
		}
		// The f64 goes back as it came: it means something only to the client.
		out := make([]byte, 0, 17)
		out = append(out, timingPong)
		out = binary.LittleEndian.AppendUint64(out, math.Float64bits(math.Float64frombits(binary.LittleEndian.Uint64(p[1:]))))
		out = binary.LittleEndian.AppendUint64(out, uint64(time.Now().UnixNano()))
		reply(out)
		return true
	case timingSubscribe:
		if len(p) == 2 {
			v.wantTiming.Store(p[1] == 1)
		}
		return true
	case timingLatency:
		if len(p) == 2 && p[1] <= byte(LatencySteady) {
			v.setLatency(LatencyMode(p[1]))
		}
		return true
	case timingShift:
		if len(p) == 3 {
			v.s.shiftFrames(float64(int16(binary.LittleEndian.Uint16(p[1:]))) / 10)
		}
		return true
	}
	return false
}

// setLatency applies a person's choice between speed and steadiness.
func (v *viewer) setLatency(mode LatencyMode) {
	if v.video != nil {
		v.video.setLatency(mode)
	}
	off := mode == LatencySteady
	_ = v.s.control(ipc.Control{NoEarlyInput: &off})
}

// shiftFrames moves when frames are made, to land them just before a viewer's display
// wants them. It's only for a game with one viewer, whose display is the only one that
// counts, and it's limited to a few times a second however often it's asked.
func (s *Session) shiftFrames(ms float64) {
	s.viewMu.Lock()
	one := len(s.viewers) == 1
	now := time.Now()
	allowed := now.Sub(s.lastShift) > 200*time.Millisecond
	if one && allowed {
		s.lastShift = now
	}
	s.viewMu.Unlock()
	if one && allowed {
		_ = s.control(ipc.Control{ShiftMs: &ms})
	}
}

// sendTiming gives a viewer that wants it this frame's record.
func (v *viewer) sendTiming(t *frameTrace) {
	if !v.wantTiming.Load() {
		return
	}
	b := t.appendRecord(make([]byte, 0, traceRecordSize))
	if dc := v.timing.Load(); dc != nil {
		if dc.ReadyState() == webrtc.DataChannelStateOpen {
			_ = dc.Send(b)
		}
		return
	}
	if v.ws != nil {
		v.send(websocketBinary, b)
	}
}
