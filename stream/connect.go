package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/wittywilla/wittyplay/ipc"
)

// Version 2 of the stream: one way in for every client, and the same messages whichever way
// they travel. WebRTC is the way, and a WebSocket carrying the same messages is the fallback for
// a network that won't carry UDP. The server's docs/GAMING-STREAM.md is the protocol; this is
// the server's half of it.

// StreamVersion is the version a mode says it can be reached with, in Mode.Stream.
const StreamVersion = 2

// Hello is what a client says about itself as it connects.
type Hello struct {
	Type    string   `json:"type,omitempty"` // "hello", over a WebSocket
	V       int      `json:"v"`
	App     string   `json:"app,omitempty"`
	Device  string   `json:"device,omitempty"`
	Name    string   `json:"name,omitempty"`
	Watch   *bool    `json:"watch,omitempty"` // false: a controller only; left out is true
	Video   []string `json:"video,omitempty"` // codecs it decodes, best first
	Audio   []string `json:"audio,omitempty"` // opus, pcm
	Seats   []int    `json:"seats"`           // the players it drives; nil: any nobody holds
	Display Display  `json:"display"`
	Latency string   `json:"latency,omitempty"`
	MaxKbps int      `json:"maxKbps,omitempty"`
}

// Display is the screen a client shows the game on.
type Display struct {
	Hz     float64 `json:"hz,omitempty"`
	Width  int     `json:"width,omitempty"`
	Height int     `json:"height,omitempty"`
}

// StreamInfo is what a connection gets: in the answer, and as the first event.
type StreamInfo struct {
	Type      string       `json:"type"` // "stream"
	V         int          `json:"v"`
	Conn      string       `json:"conn"`
	Transport string       `json:"transport"` // webrtc | websocket
	Video     string       `json:"video"`     // h264 | vp8 | jpeg | ""
	Audio     string       `json:"audio"`     // opus | pcm | ""
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Aspect    float64      `json:"aspect"`
	FPS       float64      `json:"fps"`
	Rate      int          `json:"rate"`
	Turns     int          `json:"turns"`
	Ports     int          `json:"ports"`
	Motion    bool         `json:"motion"`
	MotionOn  []MotionWant `json:"motionOn"`
	LEDs      []LEDState   `json:"leds"`
	Pace      float64      `json:"pace"`
	Seats     []Seat       `json:"seats"`
	Kbps      int          `json:"kbps"`
	Away      bool         `json:"away"`
}

// Seat is one player and the device that has it.
type Seat struct {
	Port int    `json:"port"`
	Conn string `json:"conn"`
	Name string `json:"name,omitempty"`
	You  bool   `json:"you,omitempty"`
}

// Connected is the answer to a WebRTC connection: what it gets, and the SDP.
type Connected struct {
	Stream StreamInfo `json:"stream"`
	SDP    string     `json:"sdp"`
}

// StreamError is a refusal with its HTTP status and a code a client can act on.
type StreamError struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Text   string `json:"text"`
}

func (e *StreamError) Error() string { return e.Text }

func streamErr(status int, code, text string) *StreamError {
	return &StreamError{Status: status, Code: code, Text: text}
}

// What a refusal can say.
const (
	CodeBadHello  = "bad-hello"
	CodeNoSession = "no-session"
	CodeTooLarge  = "too-large"
	CodeNoCodec   = "no-codec"
	CodeBusy      = "busy"
	CodeFailed    = "failed"
	CodeSeatTaken = "seat-taken"
	CodeBadSeat   = "bad-seat"
	CodeBadMsg    = "bad-message"
)

// Why a game ended, in the "ended" event.
const (
	EndStopped = "stopped"
	EndQuit    = "quit"
	EndCrashed = "crashed"
	EndIdle    = "idle"
	EndEvicted = "evicted"
)

// v2State is what a session keeps for version 2.
type v2State struct {
	seatOf    [MaxPlayers]*viewer // who holds each player; under viewMu
	connSeq   atomic.Uint64
	pace      float64 // the refresh rate the game runs at, or 0 for its own; under viewMu
	endCode   atomic.Value
	statsOnce sync.Once
	hapticState
	began  time.Time // the WebSocket's clocks count from here
	opusTS uint32    // the WebSocket's sound clock, in 48 kHz samples; only opusSender touches it
}

var deviceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// cleanHello checks a hello and keeps what can be used of it.
func cleanHello(h Hello) (Hello, error) {
	bad := func(what string) (Hello, error) {
		return h, streamErr(http.StatusBadRequest, CodeBadHello, what)
	}
	if h.V != StreamVersion {
		return bad(fmt.Sprintf("this server speaks version %d of the stream", StreamVersion))
	}
	h.App = clip(h.App, 64)
	if h.Device != "" && !deviceID.MatchString(h.Device) {
		return bad("device: up to 64 letters, digits, dots, dashes or underscores")
	}
	h.Name = clip(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, h.Name), 40)
	h.Video = known(h.Video, "h264", "hevc", "av1", "vp8", "jpeg")
	h.Audio = known(h.Audio, "opus", "pcm")
	if h.Seats != nil {
		seen := map[int]bool{}
		var seats []int
		for _, p := range h.Seats {
			if p < 0 || p >= MaxPlayers {
				return bad(fmt.Sprintf("seats: players are 0 to %d", MaxPlayers-1))
			}
			if !seen[p] {
				seen[p] = true
				seats = append(seats, p)
			}
		}
		h.Seats = append([]int{}, seats...)
	}
	if h.Display.Hz < 10 || h.Display.Hz > 500 || math.IsNaN(h.Display.Hz) {
		h.Display.Hz = 0
	}
	h.Display.Width, h.Display.Height = min(max(h.Display.Width, 0), 1<<15), min(max(h.Display.Height, 0), 1<<15)
	switch h.Latency {
	case "", "auto", "fastest", "balanced", "steady":
	default:
		return bad("latency: auto, fastest, balanced or steady")
	}
	h.MaxKbps = min(max(h.MaxKbps, 0), 1_000_000)
	return h, nil
}

// clip keeps the first n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// known keeps the names it knows, in order, once each, and at most eight of them.
func known(list []string, names ...string) []string {
	var out []string
	for _, n := range list {
		n = strings.ToLower(n)
		if slices.Contains(names, n) && !slices.Contains(out, n) && len(out) < 8 {
			out = append(out, n)
		}
	}
	return out
}

func (h Hello) watching() bool { return h.Watch == nil || *h.Watch }

// prefers says which of two codecs a hello lists first; "" if it lists neither.
func (h Hello) prefers(a, b string) string {
	for _, c := range h.Video {
		if c == a || c == b {
			return c
		}
	}
	return ""
}

// Connect answers a version 2 connection over WebRTC. away says the server counts the device
// as away from home, which it starts lower for.
func (s *Session) Connect(ctx context.Context, hello Hello, offer string, away bool) (Connected, error) {
	hello, err := cleanHello(hello)
	if err != nil {
		return Connected{}, err
	}
	if len(offer) > MaxOfferSize {
		return Connected{}, streamErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "the connection offer is too large")
	}
	select {
	case <-s.done:
		return Connected{}, streamErr(http.StatusNotFound, CodeNoSession, "this game has ended")
	default:
	}
	v := s.newV2Viewer(hello)
	if v.watch {
		prefer := ""
		if hello.prefers("h264", "vp8") == "vp8" {
			prefer = "vp8"
		}
		if v.codec = s.pickCodec(offer, prefer); v.codec == "" {
			return Connected{}, streamErr(http.StatusUnsupportedMediaType, CodeNoCodec, "this device and this server have no video format in common")
		}
	}
	s.startConnection(v, away)
	sdp, err := s.peer(ctx, v, offer)
	if err != nil {
		var se *StreamError
		if errors.As(err, &se) {
			return Connected{}, err
		}
		return Connected{}, streamErr(http.StatusInternalServerError, CodeFailed, err.Error())
	}
	return Connected{Stream: s.streamInfo(v), SDP: sdp}, nil
}

// newV2Viewer makes the viewer a hello describes. It isn't counted in yet.
func (s *Session) newV2Viewer(hello Hello) *viewer {
	v := &viewer{s: s, v2: true, hello: hello, watch: hello.watching(), anySeat: hello.Seats == nil}
	v.conn = "k" + strconv.FormatUint(s.connSeq.Add(1), 36)
	v.hz.Store(math.Float64bits(hello.Display.Hz))
	v.maxKbps = hello.MaxKbps
	return v
}

// startConnection does what a hello asks before anything flows: its players, and how
// fast to start.
func (s *Session) startConnection(v *viewer, away bool) {
	v.away = away
	s.viewMu.Lock()
	refused, released := s.claimSeatsLocked(v, v.hello.Seats, false)
	if away && v.watch && s.watchersLocked() == 0 {
		s.rates.startAt(awayStart)
	}
	s.viewMu.Unlock()
	s.releasePads(released)
	for _, p := range refused {
		v.queueError(CodeSeatTaken, p, fmt.Sprintf("Player %d is on another device.", p+1))
	}
	if v.hello.Latency != "" && v.hello.Latency != "auto" {
		v.pendingLatency = v.hello.Latency
	}
}

// awayStart is the share of the full bitrate a device away from home starts at. It goes up
// from there within seconds when the network carries it, and that's kinder than starting at
// what only a home network carries and stalling.
const awayStart = 0.5

// streamInfo describes the stream as this viewer gets it.
func (s *Session) streamInfo(v *viewer) StreamInfo {
	info := s.Info()
	out := StreamInfo{Type: "stream", V: StreamVersion, Conn: v.conn, Transport: "webrtc", Video: v.codec,
		Width: info.Width, Height: info.Height, Aspect: info.Aspect, FPS: info.FPS, Turns: info.Turns, Away: v.away}
	if v.ws != nil {
		out.Transport = "websocket"
	}
	if !v.watch {
		out.Video = ""
	}
	switch {
	case !v.watch:
	case v.pc != nil || v.audioWS == "opus":
		out.Audio, out.Rate = "opus", 48000
	case v.audioWS == "pcm":
		out.Audio = "pcm"
	}
	if v.codec == "h264" || v.codec == "vp8" {
		// Small pictures are doubled before they're encoded.
		chain := s.chain(v.codec)
		hw := len(chain) > 0 && isHardware(chain[0])
		sc := scaleFor(info.Width, hw)
		out.Width, out.Height = info.Width*sc, info.Height*sc
		out.Kbps = s.startKbps(out.Width, out.Height)
	}
	s.viewMu.Lock()
	out.Pace = s.pace
	out.Ports = s.portsLocked()
	out.Motion, out.MotionOn, out.LEDs = s.motionOffered, s.motionWantLocked(), s.ledsLocked()
	if out.Audio == "pcm" {
		out.Rate = int(s.pcmRateLocked() + 0.5)
	}
	out.Seats = s.seatsLocked(v)
	s.viewMu.Unlock()
	return out
}

// startKbps is what the video will start at, for the stream's description.
func (s *Session) startKbps(w, h int) int {
	bps := float64(bitrate(s.opt.Quality, w, h)) * s.rates.rate()
	if c := s.capBps(); c > 0 && bps > float64(c) {
		bps = float64(c)
	}
	return int(bps / 1000)
}

// ---- seats ----

// claimSeatsLocked gives v exactly these players, or as many of them as it can have, and
// says which it couldn't and which nobody holds any more. A player held by another device is
// refused unless take is set, or it's the same device connecting again.
func (s *Session) claimSeatsLocked(v *viewer, ports []int, take bool) (refused, released []int) {
	if ports == nil {
		return nil, nil
	}
	v.anySeat = false
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	for p, holder := range s.seatOf {
		if holder == v && !want[p] {
			s.seatOf[p] = nil
			released = append(released, p)
		}
	}
	for _, p := range ports {
		holder := s.seatOf[p]
		switch {
		case holder == v:
		case holder == nil, take, holder.closed.Load(), v.hello.Device != "" && holder.hello.Device == v.hello.Device:
			s.seatOf[p] = v
		default:
			refused = append(refused, p)
		}
	}
	s.seatsChangedLocked()
	return refused, released
}

// releaseSeatsLocked lets go of everything v holds, and says which players nobody holds now.
func (s *Session) releaseSeatsLocked(v *viewer) (released []int) {
	for p, holder := range s.seatOf {
		if holder == v {
			s.seatOf[p] = nil
			released = append(released, p)
		}
	}
	if len(released) > 0 {
		s.seatsChangedLocked()
	}
	return released
}

// releasePads lets go of every button of these players, so nothing stays held down by a
// device that has gone.
func (s *Session) releasePads(ports []int) {
	for _, p := range ports {
		var zero [ipc.InputSizeFull]byte
		zero[0] = byte(p)
		s.viewMu.Lock()
		changed := s.lastPad[p] != zero
		s.lastPad[p] = zero
		s.viewMu.Unlock()
		if changed {
			_ = s.command(ipc.CmdInput, zero[:])
		}
	}
}

// seatsLocked lists who holds each player, as v sees it.
func (s *Session) seatsLocked(v *viewer) []Seat {
	out := []Seat{}
	for p, holder := range s.seatOf {
		if holder != nil {
			out = append(out, Seat{Port: p, Conn: holder.conn, Name: holder.hello.Name, You: holder == v})
		}
	}
	return out
}

// seatsChangedLocked tells every version 2 viewer who has which player now.
func (s *Session) seatsChangedLocked() {
	for v := range s.viewers {
		if v.v2 {
			b, _ := json.Marshal(map[string]any{"type": "seats", "seats": s.seatsLocked(v)})
			go v.sendEvent(b)
		}
	}
	s.updatePaceLocked()
}

// mayDrive says whether input from v may move this player.
func (s *Session) mayDrive(v *viewer, port int) bool {
	if port < 0 || port >= MaxPlayers {
		return false
	}
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	holder := s.seatOf[port]
	if holder == nil {
		return v == nil || v.anySeat
	}
	return holder == v
}

// inputFrom applies input from one viewer, if it holds the player it's for.
func (s *Session) inputFrom(v *viewer, p []byte) {
	if len(p) == 0 {
		return
	}
	switch {
	case len(p) == ipc.PointerSize && p[0] == 0xFF:
		// A touch screen is the first player's.
		if s.mayDrive(v, 0) {
			s.Input(p)
		}
	case p[0] == ipc.TouchesMark:
		// Several fingers on the first player's touch screen.
		if _, ok := ipc.ParseTouches(p); ok && s.mayDrive(v, 0) {
			s.viewMu.Lock()
			s.lastInput = time.Now()
			s.viewMu.Unlock()
			_ = s.command(ipc.CmdTouches, p)
		}
	case p[0] == ipc.MotionMark:
		// A player's motion, for a core that reads it. It isn't someone playing: a device
		// lying on a table sends it too.
		if port, _, _, ok := ipc.ParseMotion(p); ok && s.mayDrive(v, port) {
			_ = s.command(ipc.CmdMotion, p)
		}
	default:
		if s.mayDrive(v, int(p[0])) {
			s.Input(p)
		}
	}
}

// ---- what clients say on the events channel, or as text on a WebSocket ----

type clientMessage struct {
	Type  string `json:"type"`
	Ports []int  `json:"ports"`
	Take  bool   `json:"take"`
	On    bool   `json:"on"`
	// A report of what the client sees.
	Hz        float64 `json:"hz"`
	RTTMs     float64 `json:"rttMs"`
	JitterMs  float64 `json:"jitterMs"`
	DecodeMs  float64 `json:"decodeMs"`
	DisplayMs float64 `json:"displayMs"`
	DelayMs   float64 `json:"delayMs"`
	Dropped   int64   `json:"dropped"`
}

// maxClientMessage bounds what a client may say in one message.
const maxClientMessage = 4096

// clientText acts on one text message from a version 2 client.
func (s *Session) clientText(v *viewer, b []byte) {
	if len(b) > maxClientMessage {
		v.queueError(CodeBadMsg, -1, "That message is too long.")
		return
	}
	var m clientMessage
	if err := json.Unmarshal(b, &m); err != nil {
		v.queueError(CodeBadMsg, -1, "That message couldn't be read.")
		return
	}
	switch m.Type {
	case "seats":
		ports := []int{}
		for _, p := range m.Ports {
			if p < 0 || p >= MaxPlayers {
				v.queueError(CodeBadSeat, p, fmt.Sprintf("Players are 1 to %d.", MaxPlayers))
				return
			}
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
		s.viewMu.Lock()
		refused, released := s.claimSeatsLocked(v, ports, m.Take)
		s.viewMu.Unlock()
		s.releasePads(released)
		for _, p := range refused {
			v.queueError(CodeSeatTaken, p, fmt.Sprintf("Player %d is on another device.", p+1))
		}
	case "stats":
		v.statsOn.Store(m.On)
		if m.On {
			s.statsOnce.Do(func() { go s.statsLoop() })
		}
	case "report":
		if m.Hz >= 10 && m.Hz <= 500 && math.Float64frombits(v.hz.Load()) != m.Hz {
			v.hz.Store(math.Float64bits(m.Hz))
			s.viewMu.Lock()
			s.updatePaceLocked()
			s.viewMu.Unlock()
		}
		if m.DelayMs >= 0 && v.q != nil {
			// Taken with the queue's own state at each look, as one report: two sources taking
			// turns would each undo what the other said.
			v.clientDelay.Store(int64(min(m.DelayMs, 10_000) * float64(time.Millisecond)))
			v.clientDelayAt.Store(time.Now().UnixNano())
		}
		if m.RTTMs > 0 && m.RTTMs < 60_000 {
			v.reportedRTT.Store(int64(m.RTTMs * float64(time.Millisecond)))
		}
	}
	// Anything else is from a newer client, and is left alone.
}

// queueError tells one viewer it was refused something.
func (v *viewer) queueError(code string, port int, text string) {
	m := map[string]any{"type": "error", "code": code, "text": text}
	if port >= 0 {
		m["port"] = port
	}
	b, _ := json.Marshal(m)
	v.sendEvent(b)
}

// ---- stats ----

// ConnStats is one connection's part of the stats event.
type ConnStats struct {
	Transport string  `json:"transport"`
	Video     string  `json:"video"`
	Audio     string  `json:"audio"`
	Kbps      float64 `json:"kbps"`
	Share     float64 `json:"share"`
	RTTMs     float64 `json:"rttMs"`
	Lost      float64 `json:"lost"`
	QueueMs   float64 `json:"queueMs"`
	Dropped   int64   `json:"dropped"`
	Seats     []int   `json:"seats"`
}

// statsLoop sends stats once a second to every viewer that asked for them.
func (s *Session) statsLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	prev := map[*viewer]int64{}
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		var asking []*viewer
		for _, v := range s.snapshotViewers() {
			if v.statsOn.Load() {
				asking = append(asking, v)
			}
		}
		if len(asking) == 0 {
			continue
		}
		st := s.Stats()
		s.viewMu.Lock()
		pace := s.pace
		seats := map[*viewer][]int{}
		for p, holder := range s.seatOf {
			if holder != nil {
				seats[holder] = append(seats[holder], p)
			}
		}
		s.viewMu.Unlock()
		share := s.rates.rate()
		for _, v := range asking {
			sent := v.sent.Load()
			c := ConnStats{Transport: "webrtc", Video: v.codec, Kbps: float64(sent-prev[v]) * 8 / 1000, Share: share,
				RTTMs: float64(v.rtt.Load()) / 1e6, Lost: float64(v.lost.Load()) / 256, Seats: seats[v]}
			prev[v] = sent
			if c.Seats == nil {
				c.Seats = []int{}
			}
			if v.ws != nil {
				c.Transport = "websocket"
				c.Audio = v.audioWS
				if v.q != nil {
					c.QueueMs = float64(v.q.delay.Load()) / 1e6
					c.Dropped = v.q.dropped.Load()
				}
				if r := v.reportedRTT.Load(); r > 0 {
					c.RTTMs = float64(r) / 1e6
				}
			} else if v.audio != nil {
				c.Audio = "opus"
			}
			if !v.watch {
				c.Video, c.Audio = "", ""
			}
			b, _ := json.Marshal(struct {
				Type string `json:"type"`
				Stats
				Pace float64   `json:"pace"`
				Conn ConnStats `json:"conn"`
			}{"stats", st, pace, c})
			v.sendEvent(b)
		}
		for v := range prev {
			if v.closed.Load() {
				delete(prev, v)
			}
		}
	}
}

// ---- pacing ----

// paceLimit is how far a screen's rate may be from the game's for the game to run at it: 1%
// is a change of pitch nobody hears, and more would be a game running at the wrong speed.
const paceLimit = 0.01

// updatePaceLocked runs the game at the refresh rate of the screen that matters most, when it's
// close enough: the one showing the first player, or else the first watching. A NES game runs
// at 60.0988 frames a second, so on a 60 Hz screen it otherwise drops a frame every ten seconds.
func (s *Session) updatePaceLocked() {
	fps := s.info.FPS
	var primary *viewer
	if h := s.seatOf[0]; h != nil && h.watch && math.Float64frombits(h.hz.Load()) > 0 {
		primary = h
	} else {
		for v := range s.viewers {
			if v.watch && math.Float64frombits(v.hz.Load()) > 0 && (primary == nil || v.seq < primary.seq) {
				primary = v
			}
		}
	}
	pace := 0.0
	if primary != nil {
		pace = paceFor(fps, math.Float64frombits(primary.hz.Load()))
	}
	if pace == s.pace {
		return
	}
	s.pace = pace
	s.log.Info("pacing the game to a screen", "game", fps, "screen", pace)
	p := pace
	go func() { _ = s.control(ipc.Control{DisplayHz: &p}) }()
	// The sound's rate follows: the same samples now take a little more or less time.
	if s.opus != nil {
		s.stopOpusLocked()
		s.startOpusLocked()
	}
}

// paceFor is the rate a game of this rate runs at on a screen of that one: the screen's when
// they're within paceLimit of each other, or 0 for the game's own.
func paceFor(fps, hz float64) float64 {
	if fps > 0 && hz > 0 && math.Abs(hz/fps-1) <= paceLimit {
		return hz
	}
	return 0
}

// pcmRateLocked is the sound's real rate: the core's own, or what it becomes at the screen's pace.
func (s *Session) pcmRateLocked() float64 {
	if s.pace > 0 && s.info.FPS > 0 {
		return s.rate * s.pace / s.info.FPS
	}
	return s.rate
}

// ---- rate ----

// capBps is the lowest limit any watching device has asked for, or 0.
func (s *Session) capBps() int {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	return s.capBpsLocked()
}

func (s *Session) capBpsLocked() int {
	cap := 0
	for v := range s.viewers {
		if v.watch && v.maxKbps > 0 && (cap == 0 || v.maxKbps*1000 < cap) {
			cap = v.maxKbps * 1000
		}
	}
	return cap
}

// noteWSDelay takes how long a WebSocket viewer's frames are waiting, and how many it lost
// to waiting, as a WebRTC report's round trip and loss. A queue that keeps growing is a network
// that's full, the same as a round trip that keeps growing.
func (s *Session) noteWSDelay(v *viewer, delay time.Duration, lost float64) {
	now := time.Now()
	if time.Since(v.connected.Load().(time.Time)) < rateSettle {
		return
	}
	// A frame that waits behind others is late by that much: as a round trip, the base is the
	// network's and the rest is the queue. Unlike a round trip a busy machine answers late, a
	// long one here is a real queue, so it's kept under what the rate takes for a stall.
	if s.rates.note(lost, min(delay, rateRTTSane-time.Millisecond)+time.Millisecond, now) {
		s.log.Info("matching the video to a WebSocket", "share", s.rates.rate(), "lost", lost, "waiting", delay.String())
		s.viewMu.Lock()
		s.replaceEncoderLocked(v.codec, now, false)
		s.viewMu.Unlock()
	}
}

// ---- small things ----

// watchersLocked counts the viewers being shown the game. Controllers don't count: with only
// them, nobody can see it, so it waits, and it ends after a minute as with nobody at all.
func (s *Session) watchersLocked() int {
	n := 0
	for v := range s.viewers {
		if v.watch {
			n++
		}
	}
	return n
}

// endWith notes why the game is ending, for the "ended" event. The first reason given stands.
func (s *Session) endWith(code string) { s.endCode.CompareAndSwap(nil, code) }

func (s *Session) endCodeNow() string {
	if c, ok := s.endCode.Load().(string); ok {
		return c
	}
	if s.stopping.Load() {
		return EndStopped
	}
	return EndCrashed
}
