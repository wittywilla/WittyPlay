package stream

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// The fallback for version 2: the same messages as over WebRTC, on a WebSocket, for a network
// that won't carry UDP to the server. TCP never loses anything, so what it does with a network
// that's too slow is queue, and every queued frame is delay. So only two frames may wait: past
// that a frame is dropped, the next keyframe is waited for, and the video comes down to fit.

// What a version 2 WebSocket viewer receives, beyond version 1's JPEG and PCM.
const (
	wsVideo = 0x11 // u8 flags (bit 0: keyframe), u32 frame number, u32 90 kHz timestamp, the frame
	wsOpus  = 0x12 // u32 48 kHz timestamp, one Opus packet
)

// How a version 2 WebSocket queues what it sends.
const (
	wsVideoQueue   = 2                      // frames that may wait; past that one is dropped
	wsCtrlQueue    = 512                    // sound, events and timing; past that the connection is hopeless
	wsHelloWait    = 5 * time.Second        // how long a client has to say hello
	wsReportEvery  = 250 * time.Millisecond // how often the queue's state reaches the rate
	wsWriteTimeout = 5 * time.Second
)

// wsQueue is what a version 2 WebSocket has to send: sound, events and timing first, never
// dropped, then at most wsVideoQueue frames.
type wsQueue struct {
	mu     sync.Mutex
	ctrl   []wsMessage
	video  []queuedFrame
	wake   chan struct{}
	done   chan struct{}
	closed bool

	delay   atomic.Int64 // how long the last frame waited, in nanoseconds
	dropped atomic.Int64 // frames dropped for waiting, since it connected
	sent    atomic.Int64 // frames sent
}

type queuedFrame struct {
	b   []byte
	at  time.Time
	key bool
}

func newWSQueue() *wsQueue {
	return &wsQueue{wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (q *wsQueue) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// pushCtrl queues something that's never dropped. It says false when the connection can't
// keep up even with that, and should go.
func (q *wsQueue) pushCtrl(t websocket.MessageType, b []byte) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if len(q.ctrl) >= wsCtrlQueue {
		q.mu.Unlock()
		return false
	}
	q.ctrl = append(q.ctrl, wsMessage{t, b})
	q.mu.Unlock()
	q.poke()
	return true
}

// pushVideo queues a frame, unless two are already waiting. A keyframe replaces whatever is
// waiting, as nothing before it is needed any more.
func (q *wsQueue) pushVideo(b []byte, key bool, at time.Time) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	if key && len(q.video) > 0 {
		q.dropped.Add(int64(len(q.video)))
		q.video = q.video[:0]
	}
	if len(q.video) >= wsVideoQueue {
		q.mu.Unlock()
		q.dropped.Add(1)
		return false
	}
	q.video = append(q.video, queuedFrame{b, at, key})
	q.mu.Unlock()
	q.poke()
	return true
}

// next takes what's to be sent next: sound and events first.
func (q *wsQueue) next() (m wsMessage, frame *queuedFrame, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ctrl) > 0 {
		m = q.ctrl[0]
		q.ctrl[0] = wsMessage{}
		q.ctrl = q.ctrl[1:]
		return m, nil, true
	}
	if len(q.video) > 0 {
		f := q.video[0]
		q.video = q.video[1:]
		return wsMessage{websocket.MessageBinary, f.b}, &f, true
	}
	return m, nil, false
}

func (q *wsQueue) close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.done)
	}
	q.mu.Unlock()
}

// deliverVideoV2 queues one frame for a version 2 WebSocket viewer.
func (s *Session) deliverVideoV2(v *viewer, au AU, tr *frameTrace) {
	if !v.watch {
		return
	}
	if v.waitKey.Load() && !au.Key {
		v.q.dropped.Add(1) // as lost to it as one dropped from the queue
		return
	}
	msg := make([]byte, 10, 10+len(au.Data))
	msg[0] = wsVideo
	if au.Key {
		msg[1] = 1
	}
	binary.LittleEndian.PutUint32(msg[2:], tr.Seq)
	binary.LittleEndian.PutUint32(msg[6:], s.clock90k(tr.ServerIn))
	msg = append(msg, au.Data...)
	tr.Sent = time.Now().UnixNano()
	v.sendTiming(tr)
	if v.q.pushVideo(msg, au.Key, time.Now()) {
		v.waitKey.Store(false)
		v.sent.Add(int64(len(msg)))
		return
	}
	// Dropped: what follows can't be decoded without it, so wait for a keyframe, and ask for
	// one sooner than the regular one.
	v.waitKey.Store(true)
	s.keyframeAsked(v.codec)
}

// clock90k is a moment as the WebSocket's video clock has it: 90 kHz from when the game began.
func (s *Session) clock90k(unixNano int64) uint32 {
	if unixNano == 0 {
		unixNano = time.Now().UnixNano()
	}
	return uint32(uint64(max(unixNano-s.began.UnixNano(), 0)) * 9 / 100000)
}

// ServeStream is version 2's WebSocket: the hello comes first, as text, and the stream's
// description goes back. away is the server's word on where the device is.
func (s *Session) ServeStream(w http.ResponseWriter, r *http.Request, away bool) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	c.SetReadLimit(maxClientMessage)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	refuse := func(e *StreamError) {
		b, _ := json.Marshal(map[string]any{"type": "error", "code": e.Code, "text": e.Text})
		wctx, wcancel := context.WithTimeout(ctx, time.Second)
		_ = c.Write(wctx, websocket.MessageText, b)
		wcancel()
		_ = c.Close(websocket.StatusPolicyViolation, e.Code)
	}
	hctx, hcancel := context.WithTimeout(ctx, wsHelloWait)
	t, b, err := c.Read(hctx)
	hcancel()
	if err != nil || t != websocket.MessageText {
		refuse(streamErr(http.StatusBadRequest, CodeBadHello, "the first message must be the hello, as text"))
		return
	}
	var hello Hello
	if err := json.Unmarshal(b, &hello); err != nil || (hello.Type != "" && hello.Type != "hello") {
		refuse(streamErr(http.StatusBadRequest, CodeBadHello, "the hello couldn't be read"))
		return
	}
	if hello, err = cleanHello(hello); err != nil {
		var se *StreamError
		errors.As(err, &se)
		refuse(se)
		return
	}
	select {
	case <-s.done:
		refuse(streamErr(http.StatusNotFound, CodeNoSession, "this game has ended"))
		return
	default:
	}
	v := s.newV2Viewer(hello)
	v.ws, v.q = c, newWSQueue()
	if v.watch {
		// H.264 when this server can encode it and the device didn't put JPEG first.
		v.codec = "jpeg"
		if len(s.chain("h264")) > 0 && (len(hello.Video) == 0 || hello.prefers("h264", "jpeg") == "h264") {
			v.codec = "h264"
		}
		// Opus unless the device can't take it, or this FFmpeg can't make it.
		v.audioWS = "pcm"
		if s.cfg.HaveEncoder("libopus") && (len(hello.Audio) == 0 || hello.Audio[0] == "opus") {
			v.audioWS = "opus"
		}
		v.waitKey.Store(true)
	}
	v.connected.Store(time.Now())
	s.startConnection(v, away)
	// The description goes first, then anything a hello was refused.
	desc, _ := json.Marshal(s.streamInfo(v))
	early := v.early.take()
	v.q.pushCtrl(websocket.MessageText, desc)
	for _, e := range early {
		v.q.pushCtrl(websocket.MessageText, e)
	}
	defer v.close()
	go s.writeStream(ctx, cancel, v)
	s.attach(v)
	for {
		t, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		switch t {
		case websocket.MessageText:
			s.clientText(v, b)
		case websocket.MessageBinary:
			if !v.timingMessage(b, func(out []byte) { v.q.pushCtrl(websocket.MessageBinary, out) }) {
				s.inputFrom(v, b)
			}
		}
	}
}

// writeStream sends what's queued, sound and events first, and says how long frames are
// waiting. A connection that can't take a message in five seconds has gone.
func (s *Session) writeStream(ctx context.Context, cancel context.CancelFunc, v *viewer) {
	defer cancel()
	tick := time.NewTicker(wsReportEvery)
	defer tick.Stop()
	var worst time.Duration
	var dropped0, sent0 int64
	for {
		m, frame, ok := v.q.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				_ = v.ws.Close(websocket.StatusNormalClosure, "the game has ended")
				return
			case <-v.q.done:
				return
			case <-v.q.wake:
				continue
			case <-tick.C:
				s.reportQueue(v, &worst, &dropped0, &sent0)
				continue
			}
		}
		wctx, wcancel := context.WithTimeout(ctx, wsWriteTimeout)
		err := v.ws.Write(wctx, m.typ, m.data)
		wcancel()
		if err != nil {
			return
		}
		if frame != nil {
			waited := time.Since(frame.at)
			v.q.delay.Store(int64(waited))
			v.q.sent.Add(1)
			worst = max(worst, waited)
		}
		select {
		case <-tick.C:
			s.reportQueue(v, &worst, &dropped0, &sent0)
		default:
		}
	}
}

// reportQueue passes how the queue has been doing to the rate, as a WebRTC report would.
func (s *Session) reportQueue(v *viewer, worst *time.Duration, dropped0, sent0 *int64) {
	if !v.watch || v.codec != "h264" {
		return
	}
	d, n := v.q.dropped.Load(), v.q.sent.Load()
	lost := 0.0
	if all := (d - *dropped0) + (n - *sent0); all > 0 {
		lost = float64(d-*dropped0) / float64(all)
	}
	*dropped0, *sent0 = d, n
	// How late frames are: the worst of what waited here and what the client saw arrive late,
	// which includes every queue on the way that this side can't see. Over TCP that's nearly
	// all of it: the system's buffers take a second of video before a write here ever waits.
	// The client's word stands until it says something else, or for a second: it reports twice
	// a second, and this looks four times.
	var said time.Duration
	if at := v.clientDelayAt.Load(); at != 0 && time.Since(time.Unix(0, at)) < time.Second {
		said = time.Duration(v.clientDelay.Load())
	}
	s.noteWSDelay(v, max(*worst, said), lost)
	*worst = 0
}

// early holds events for a viewer whose channel for them isn't open yet.
type earlyEvents struct {
	mu   sync.Mutex
	list [][]byte
}

func (e *earlyEvents) add(b []byte) {
	e.mu.Lock()
	if len(e.list) < 64 {
		e.list = append(e.list, b)
	}
	e.mu.Unlock()
}

func (e *earlyEvents) take() [][]byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.list
	e.list = nil
	return l
}

// latencyNamed is a LatencyMode by the name a hello gives it.
func latencyNamed(name string) LatencyMode {
	switch name {
	case "balanced":
		return LatencyBalanced
	case "steady":
		return LatencySteady
	}
	return LatencyFastest
}
