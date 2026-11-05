package stream

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func TestTheHelloIsChecked(t *testing.T) {
	bad := []Hello{
		{V: 1},
		{V: 3},
		{V: 2, Device: "has spaces"},
		{V: 2, Device: strings.Repeat("a", 65)},
		{V: 2, Seats: []int{4}},
		{V: 2, Seats: []int{-1}},
		{V: 2, Latency: "soon"},
	}
	for _, h := range bad {
		_, err := cleanHello(h)
		var se *StreamError
		if !errors.As(err, &se) || se.Code != CodeBadHello || se.Status != http.StatusBadRequest {
			t.Errorf("%+v: got %v, want a bad-hello", h, err)
		}
	}
	h, err := cleanHello(Hello{V: 2, Name: "Living\nroom " + strings.Repeat("x", 60), Video: []string{"H264", "mpeg2", "h264", "vp8"}, Audio: []string{"aac", "opus"},
		Seats: []int{1, 1, 0}, Display: Display{Hz: 1000}, MaxKbps: -5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(h.Name, '\n') || len([]rune(h.Name)) != 40 {
		t.Errorf("name %q: no control characters, at most 40", h.Name)
	}
	if strings.Join(h.Video, ",") != "h264,vp8" || strings.Join(h.Audio, ",") != "opus" {
		t.Errorf("codecs %v %v: known ones, once, in order", h.Video, h.Audio)
	}
	if len(h.Seats) != 2 || h.Seats[0] != 1 || h.Seats[1] != 0 {
		t.Errorf("seats %v: each once", h.Seats)
	}
	if h.Display.Hz != 0 || h.MaxKbps != 0 {
		t.Errorf("a refresh rate or a limit that can't be is none: %+v", h)
	}
	// No seats is version 1's way; an empty list is a device that drives nobody.
	if h, _ := cleanHello(Hello{V: 2}); h.Seats != nil {
		t.Error("no seats should stay nil")
	}
	if h, _ := cleanHello(Hello{V: 2, Seats: []int{}}); h.Seats == nil {
		t.Error("an empty list of seats should stay a list")
	}
}

func TestAFrameKeepsToTheScreenWhenItsClose(t *testing.T) {
	// The runner's side is in host; the session decides when, and says so in "pace".
	for _, c := range []struct {
		game, screen float64
		want         bool
	}{{60.0988, 60, true}, {59.8261, 59.94, true}, {50.007, 60, false}, {60, 0, false}, {60, 120, false}} {
		pace := paceFor(c.game, c.screen)
		if got := pace != 0; got != c.want || (got && pace != c.screen) {
			t.Errorf("game %v on a %v Hz screen: pace %v", c.game, c.screen, pace)
		}
	}
}

func TestAWebSocketQueueDropsRatherThanWaits(t *testing.T) {
	q := newWSQueue()
	now := time.Now()
	if !q.pushVideo([]byte{1}, true, now) || !q.pushVideo([]byte{2}, false, now) {
		t.Fatal("two frames may wait")
	}
	if q.pushVideo([]byte{3}, false, now) {
		t.Fatal("a third must be dropped, not queued")
	}
	q.pushCtrl(websocket.MessageText, []byte("event"))
	if m, f, _ := q.next(); f != nil || string(m.data) != "event" {
		t.Fatal("sound and events go before frames")
	}
	if !q.pushVideo([]byte{4}, true, now) {
		t.Fatal("a keyframe is always taken")
	}
	if _, f, _ := q.next(); f == nil || !f.key || f.b[0] != 4 {
		t.Fatal("a keyframe replaces the frames waiting before it, which nothing needs any more")
	}
	if q.dropped.Load() != 3 {
		t.Errorf("dropped %d, want 3", q.dropped.Load())
	}
}

func TestAwayStartsLowerUntilTheNetworkSaysOtherwise(t *testing.T) {
	var c rateControl
	c.startAt(awayStart)
	if c.rate() != awayStart {
		t.Fatalf("rate %v", c.rate())
	}
	// Once something has been learnt, a new device away doesn't undo it.
	c.note(0, 10*time.Millisecond, time.Now())
	c.scale, c.lastChange = 0.8, time.Now()
	c.startAt(awayStart)
	if c.rate() != 0.8 {
		t.Fatalf("rate %v after learning", c.rate())
	}
}

// Two devices of one account in one game: a TV over WebRTC holding the first player, and a
// phone over a WebSocket that asks for the same player. The phone is refused it, its presses
// don't reach the game, and the TV's rumble isn't sent to it.
func TestTwoDevicesShareAGameBySeats(t *testing.T) {
	m := newManager(t)
	save := t.TempDir()
	s := startGame(t, m, save, false)
	defer s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The TV.
	tv := dialV2WebRTC(t, ctx, s, Hello{V: 2, Device: "tv-1", Name: "Living room", Seats: []int{0}, Display: Display{Hz: 60}})
	defer tv.pc.Close()
	if tv.got.Stream.Transport != "webrtc" || tv.got.Stream.Audio != "opus" || tv.got.Stream.Ports != MaxPlayers || tv.got.Stream.V != 2 {
		t.Fatalf("stream: %+v", tv.got.Stream)
	}
	if len(tv.got.Stream.Seats) != 1 || !tv.got.Stream.Seats[0].You || tv.got.Stream.Seats[0].Port != 0 {
		t.Fatalf("the TV should hold the first player: %+v", tv.got.Stream.Seats)
	}
	waitFor(t, 20*time.Second, "the TV's picture", func() bool { return tv.video.Load() > 20 })
	first := tv.event(t, ctx, "stream")
	if first["conn"] != tv.got.Stream.Conn {
		t.Fatalf("the first event is the stream's description: %v", first)
	}
	waitFor(t, 5*time.Second, "pacing to the TV", func() bool { s.viewMu.Lock(); defer s.viewMu.Unlock(); return s.pace == 60 })

	// The phone, over a WebSocket.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeStream(w, r, true) }))
	defer srv.Close()
	phone, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer phone.CloseNow()
	phone.SetReadLimit(1 << 24)
	hello, _ := json.Marshal(Hello{Type: "hello", V: 2, Device: "phone-1", Name: "Phone", Seats: []int{0}, Audio: []string{"pcm"}})
	if err := phone.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	ph := newWSReader(phone)
	desc := ph.text(t, ctx, "stream")
	if desc["transport"] != "websocket" || desc["video"] != "h264" || desc["audio"] != "pcm" || desc["away"] != true {
		t.Fatalf("the phone's stream: %v", desc)
	}
	if e := ph.text(t, ctx, "error"); e["code"] != CodeSeatTaken || e["port"] != float64(0) {
		t.Fatalf("the first player is the TV's: %v", e)
	}
	seats := tv.event(t, ctx, "seats")
	if list, _ := seats["seats"].([]any); len(list) != 1 {
		t.Fatalf("the TV hears who has which player: %v", seats)
	}
	// The phone's picture starts with a keyframe, in version 2's framing.
	key := ph.binary(t, ctx, wsVideo)
	if key[1]&1 != 1 || !bytes.HasPrefix(key[10:], []byte{0, 0, 0, 1}) {
		t.Fatal("the first frame is a keyframe in Annex B, after flags, frame number and time")
	}
	if pcm := ph.binary(t, ctx, wsPCM); binary.LittleEndian.Uint32(pcm[1:]) == 0 {
		t.Fatal("PCM says its rate")
	}

	// The phone presses Start twice, which the game never sees; the TV presses it once.
	for range 2 {
		_ = phone.Write(ctx, websocket.MessageBinary, input(0, 1<<3))
		time.Sleep(100 * time.Millisecond)
		_ = phone.Write(ctx, websocket.MessageBinary, input(0, 0))
		time.Sleep(100 * time.Millisecond)
	}
	_ = tv.input.Send(input(0, 1<<3))
	time.Sleep(150 * time.Millisecond)
	_ = tv.input.Send(input(0, 0))
	time.Sleep(150 * time.Millisecond)

	// The TV presses A, and the test core rumbles the first player: only the TV hears it.
	_ = tv.input.Send(input(0, 1<<8))
	time.Sleep(150 * time.Millisecond)
	_ = tv.input.Send(input(0, 0))
	if e := tv.event(t, ctx, "rumble"); e["strength"] != float64(0xffff) {
		t.Fatalf("rumble: %v", e)
	}
	if ph.sawText("rumble") {
		t.Fatal("the phone doesn't hold the first player, so it mustn't shake")
	}

	// Stats, when asked for.
	_ = phone.Write(ctx, websocket.MessageText, []byte(`{"type":"stats","on":true}`))
	st := ph.text(t, ctx, "stats")
	if c, _ := st["conn"].(map[string]any); c["transport"] != "websocket" || c["video"] != "h264" {
		t.Fatalf("stats: %v", st)
	}

	// Taking the player hands it over, and lets the TV's buttons go.
	_ = phone.Write(ctx, websocket.MessageText, []byte(`{"type":"seats","ports":[0],"take":true}`))
	waitFor(t, 5*time.Second, "the phone taking the first player", func() bool { return s.mayDrive(tv.viewer(s), 0) == false })

	s.Stop()
	ram, err := os.ReadFile(filepath.Join(save, "save.srm"))
	if err != nil {
		t.Fatal(err)
	}
	if n := binary.LittleEndian.Uint32(ram); n != 1 {
		t.Fatalf("Start presses = %d, want only the TV's one", n)
	}
	if e := ph.text(t, ctx, "ended"); e["code"] != EndStopped {
		t.Fatalf("ended: %v", e)
	}
}

// A phone held as a controller gets no picture or sound, and nobody watching means the game
// waits, whatever controllers are connected.
func TestAControllerOnlyGetsNoPicture(t *testing.T) {
	m := newManager(t)
	s := startGame(t, m, t.TempDir(), false)
	defer s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeStream(w, r, false) }))
	defer srv.Close()
	c, _, err := websocket.Dial(ctx, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	no := false
	hello, _ := json.Marshal(Hello{Type: "hello", V: 2, Watch: &no, Seats: []int{1}})
	_ = c.Write(ctx, websocket.MessageText, hello)
	r := newWSReader(c)
	if d := r.text(t, ctx, "stream"); d["video"] != "" || d["audio"] != "" {
		t.Fatalf("a controller's stream: %v", d)
	}
	time.Sleep(time.Second)
	if n := r.binaries.Load(); n > 0 {
		t.Fatalf("%d pictures or sounds sent to a controller", n)
	}
	s.viewMu.Lock()
	paused := s.paused
	s.viewMu.Unlock()
	if !paused {
		t.Fatal("with nobody watching, the game waits")
	}
}

func TestAWebSocketMustSayHelloFirst(t *testing.T) {
	s := viewerTestSession(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeStream(w, r, false) }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, first := range [][]byte{[]byte(`{"type":"hello","v":1}`), []byte("not json")} {
		c, _, err := websocket.Dial(ctx, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Write(ctx, websocket.MessageText, first)
		_, b, err := c.Read(ctx)
		if err != nil || !strings.Contains(string(b), CodeBadHello) {
			t.Fatalf("%s: got %s, %v", first, b, err)
		}
		c.CloseNow()
	}
}

// ---- helpers ----

type v2Peer struct {
	pc     *webrtc.PeerConnection
	input  *webrtc.DataChannel
	got    Connected
	video  atomicCount
	events chan map[string]any
}

type atomicCount struct {
	mu sync.Mutex
	n  int
}

func (a *atomicCount) Add(n int) { a.mu.Lock(); a.n += n; a.mu.Unlock() }
func (a *atomicCount) Load() int { a.mu.Lock(); defer a.mu.Unlock(); return a.n }

func dialV2WebRTC(t *testing.T, ctx context.Context, s *Session, hello Hello) *v2Peer {
	t.Helper()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	p := &v2Peer{pc: pc, events: make(chan map[string]any, 64)}
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		buf := make([]byte, 1500)
		for {
			if _, _, err := tr.Read(buf); err != nil {
				return
			}
			if tr.Kind() == webrtc.RTPCodecTypeVideo {
				p.video.Add(1)
			}
		}
	})
	if p.input, err = pc.CreateDataChannel("input", &webrtc.DataChannelInit{Ordered: boolp(false), MaxRetransmits: uint16p(0)}); err != nil {
		t.Fatal(err)
	}
	ev, err := pc.CreateDataChannel("events", nil)
	if err != nil {
		t.Fatal(err)
	}
	ev.OnMessage(func(m webrtc.DataChannelMessage) {
		var e map[string]any
		if json.Unmarshal(m.Data, &e) == nil {
			select {
			case p.events <- e:
			default:
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	if p.got, err = s.Connect(ctx, hello, pc.LocalDescription().SDP, false); err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: p.got.SDP}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the input channel", func() bool { return p.input.ReadyState() == webrtc.DataChannelStateOpen })
	return p
}

// event waits for an event of this type, passing over others.
func (p *v2Peer) event(t *testing.T, ctx context.Context, typ string) map[string]any {
	t.Helper()
	for {
		select {
		case e := <-p.events:
			if e["type"] == typ {
				return e
			}
		case <-ctx.Done():
			t.Fatalf("no %q event", typ)
		}
	}
}

// viewer finds the server's side of this peer.
func (p *v2Peer) viewer(s *Session) *viewer {
	for _, v := range s.snapshotViewers() {
		if v.conn == p.got.Stream.Conn {
			return v
		}
	}
	return nil
}

type wsMsg struct {
	typ websocket.MessageType
	b   []byte
}

// wsReader reads a connection in the background: a read that's given up on closes a
// coder/websocket connection, so waiting has to happen here instead.
type wsReader struct {
	c        *websocket.Conn
	in       chan wsMsg
	held     []map[string]any // events passed over while waiting for another
	binaries atomic.Int64
	err      error
}

func newWSReader(c *websocket.Conn) *wsReader {
	r := &wsReader{c: c, in: make(chan wsMsg, 4096)}
	go func() {
		defer close(r.in)
		for {
			typ, b, err := c.Read(context.Background())
			if err != nil {
				r.err = err
				return
			}
			if typ == websocket.MessageBinary {
				r.binaries.Add(1)
			}
			r.in <- wsMsg{typ, b}
		}
	}()
	return r
}

func (r *wsReader) next(ctx context.Context, wait time.Duration) (wsMsg, bool) {
	select {
	case m, ok := <-r.in:
		return m, ok
	case <-ctx.Done():
	case <-time.After(wait):
	}
	return wsMsg{}, false
}

func (r *wsReader) text(t *testing.T, ctx context.Context, typ string) map[string]any {
	t.Helper()
	for i, e := range r.held {
		if e["type"] == typ {
			r.held = append(r.held[:i], r.held[i+1:]...)
			return e
		}
	}
	for {
		m, ok := r.next(ctx, 20*time.Second)
		if !ok {
			t.Fatalf("no %q event (%v)", typ, r.err)
		}
		if m.typ != websocket.MessageText {
			continue
		}
		var e map[string]any
		if json.Unmarshal(m.b, &e) != nil {
			continue
		}
		if e["type"] == typ {
			return e
		}
		r.held = append(r.held, e)
	}
}

func (r *wsReader) binary(t *testing.T, ctx context.Context, first byte) []byte {
	t.Helper()
	for {
		m, ok := r.next(ctx, 20*time.Second)
		if !ok {
			t.Fatalf("no 0x%02x message (%v)", first, r.err)
		}
		if m.typ == websocket.MessageText {
			var e map[string]any
			if json.Unmarshal(m.b, &e) == nil {
				r.held = append(r.held, e)
			}
			continue
		}
		if len(m.b) > 0 && m.b[0] == first {
			return m.b
		}
	}
}

// sawText waits a second and says whether an event of this type came.
func (r *wsReader) sawText(typ string) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m, ok := r.next(context.Background(), time.Until(deadline))
		if !ok {
			break
		}
		var e map[string]any
		if m.typ == websocket.MessageText && json.Unmarshal(m.b, &e) == nil {
			r.held = append(r.held, e)
		}
	}
	for _, e := range r.held {
		if e["type"] == typ {
			return true
		}
	}
	return false
}

// Over TCP the system's buffers hide a full network from the server, so a WebSocket's rate
// follows what the client says about how late frames arrive. A report counts at every look for
// a second (the client reports twice a second, the server looks four times), and a delay that
// stays up brings the rate down.
func TestAWebSocketFallingBehindBringsTheRateDown(t *testing.T) {
	s := &Session{viewers: map[*viewer]bool{}, log: slog.New(slog.DiscardHandler)}
	v := &viewer{s: s, v2: true, watch: true, codec: "h264", q: newWSQueue()}
	v.connected.Store(time.Now().Add(-time.Minute))
	var worst time.Duration
	var dropped0, sent0 int64
	look := func() { s.reportQueue(v, &worst, &dropped0, &sent0) }
	look() // nothing said yet: the network's own time is the best seen
	if s.rates.rate() != 1 {
		t.Fatal("nothing to act on yet")
	}
	s.clientText(v, []byte(`{"type":"report","delayMs":300}`))
	for range 6 {
		look()
	}
	if r := s.rates.rate(); r >= 1 {
		t.Fatalf("a delay that stays at 300 ms should bring the rate down: %v", r)
	}
	// A report goes stale after a second, so a client that stops saying it's late isn't held to it.
	v.clientDelayAt.Store(time.Now().Add(-2 * time.Second).UnixNano())
	worst = 0
	look()
	if v.clientDelay.Load() == 0 {
		t.Fatal("the last report is kept, only not used")
	}
}
