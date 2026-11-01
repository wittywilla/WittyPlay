package stream

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/wittywilla/wittyplay/ipc"
)

// websocketBinary keeps the timing code from needing to know about WebSockets.
const websocketBinary = websocket.MessageBinary

// viewer is one browser watching a session.
type viewer struct {
	s       *Session
	codec   string // h264 | vp8 | jpeg
	waitKey atomic.Bool

	// How frames are timed for this viewer.
	wantTiming atomic.Bool
	timing     atomic.Pointer[webrtc.DataChannel]
	connected  atomic.Value // when, a time.Time
	direct     atomic.Bool  // it decodes frames itself, and asks for keyframes itself
	videoSSRC  atomic.Uint32

	// WebRTC
	pc     *webrtc.PeerConnection
	video  *videoSender
	audio  *webrtc.TrackLocalStaticSample
	events atomic.Pointer[webrtc.DataChannel]

	// WebSocket
	ws  *websocket.Conn
	out chan wsMessage

	seq       uint64 // order of attaching; the oldest gives way
	closed    atomic.Bool
	evicted   atomic.Bool
	closeOnce sync.Once
}

// How many browsers one game takes. Each offer costs a peer connection, with its sockets and
// goroutines, whether or not anything ever connects, and anyone allowed to see a game can send
// as many as they like. Over either limit the oldest goes, so a reload or another device simply
// carries on.
const (
	maxViewers        = 8 // watching
	maxPendingViewers = 4 // offered, and not connected yet
)

type wsMessage struct {
	typ  websocket.MessageType
	data []byte
}

// What a WebSocket viewer receives; the first byte says which.
const (
	wsH264 = 1 // then one byte, 1 for a keyframe, then Annex B
	wsJPEG = 2
	wsPCM  = 3 // then a u32 sample rate, then s16le stereo
)

// attach counts a viewer in and wakes the game up if it was the first.
func (s *Session) attach(v *viewer) {
	s.viewMu.Lock()
	s.dropPendingLocked(v)
	if v.closed.Load() { // let go while it was connecting
		s.viewMu.Unlock()
		return
	}
	s.viewSeq++
	v.seq = s.viewSeq
	s.viewers[v] = true
	var oldest *viewer
	if len(s.viewers) > maxViewers {
		for o := range s.viewers {
			if oldest == nil || o.seq < oldest.seq {
				oldest = o
			}
		}
	}
	s.lastInput = time.Now()
	// A running encoder's next keyframe may be a second away, and this viewer can
	// show nothing until then. Starting it again sends one straight away.
	switch {
	case v.codec == "h264" && s.h264 != nil:
		s.h264.stop()
		s.h264 = nil
	case v.codec == "vp8" && s.vp8 != nil:
		s.vp8.stop()
		s.vp8 = nil
	}
	if v.pc != nil {
		s.startOpusLocked()
	}
	s.applyPauseLocked()
	s.viewMu.Unlock()
	if oldest != nil {
		oldest.evicted.Store(true)
		oldest.close()
	}
	// A paused game makes no new frames, but a new viewer still needs a picture.
	s.signalFrame()
}

// holdPending notes a peer that isn't connected yet, and lets the oldest go over the limit.
func (s *Session) holdPending(v *viewer) {
	s.viewMu.Lock()
	s.pending = append(s.pending, v)
	var oldest *viewer
	if len(s.pending) > maxPendingViewers {
		oldest = s.pending[0]
		s.pending = s.pending[1:]
	}
	s.viewMu.Unlock()
	if oldest != nil {
		oldest.close()
	}
}

func (s *Session) dropPendingLocked(v *viewer) {
	for i, o := range s.pending {
		if o == v {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			return
		}
	}
}

// applyPauseLocked tells the runner whether to run: only with someone watching,
// and not while a menu has paused it. It says so only when that has changed.
func (s *Session) applyPauseLocked() {
	paused := s.menuPaused || len(s.viewers) == 0
	if paused == s.paused {
		return
	}
	s.paused = paused
	b := byte(0)
	if paused {
		b = 1
	} else if s.lastFrame.Load() != 0 {
		s.lastFrame.Store(time.Now().UnixNano())
	}
	_ = s.command(ipc.CmdPause, []byte{b})
}

// detach counts a viewer out; with none left the game pauses and encoders stop.
func (s *Session) detach(v *viewer) {
	s.viewMu.Lock()
	s.dropPendingLocked(v)
	if !s.viewers[v] {
		s.viewMu.Unlock()
		return
	}
	delete(s.viewers, v)
	if len(s.viewers) == 0 {
		s.noViewerSince = time.Now()
		s.stopEncodersLocked()
	} else if !s.anyWebRTCLocked() {
		s.stopOpusLocked()
	}
	s.applyPauseLocked()
	s.viewMu.Unlock()
}

func (s *Session) anyWebRTCLocked() bool {
	for v := range s.viewers {
		if v.pc != nil {
			return true
		}
	}
	return false
}

func (v *viewer) close() {
	v.closeOnce.Do(func() {
		v.closed.Store(true)
		v.s.detach(v)
		if v.pc != nil {
			_ = v.pc.Close()
		}
		if v.ws != nil {
			reason := "the game has ended"
			if v.evicted.Load() {
				reason = "too many people are watching this game"
			}
			_ = v.ws.Close(websocket.StatusNormalClosure, reason)
		}
	})
}

func (s *Session) snapshotViewers() []*viewer {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	out := make([]*viewer, 0, len(s.viewers))
	for v := range s.viewers {
		out = append(out, v)
	}
	return out
}

func (s *Session) deliverVideo(codec string, au AU, tr *frameTrace) {
	dur := s.auDuration(codec)
	s.bytesOut.Add(int64(len(au.Data)))
	tr.Size, tr.Key = int32(len(au.Data)), au.Key
	for _, v := range s.snapshotViewers() {
		if v.codec != codec {
			continue
		}
		if v.video != nil {
			tr.RTP = v.video.write(au.Data, dur)
			tr.Sent = time.Now().UnixNano()
			v.sendTiming(tr)
			continue
		}
		// A viewer that fell behind waits for the next keyframe, because its
		// decoder can't use anything before that.
		if v.waitKey.Load() && !au.Key {
			continue
		}
		flags := byte(0)
		if au.Key {
			flags = 1
		}
		msg := append([]byte{wsH264, flags}, au.Data...)
		tr.Sent = time.Now().UnixNano()
		v.sendTiming(tr) // first, so that it's known about when the picture arrives
		if v.send(websocket.MessageBinary, msg) {
			v.waitKey.Store(false)
		} else {
			v.waitKey.Store(true)
		}
	}
}

func (s *Session) deliverJPEG(b []byte) {
	s.bytesOut.Add(int64(len(b)))
	for _, v := range s.snapshotViewers() {
		if v.codec == "jpeg" {
			v.send(websocket.MessageBinary, append([]byte{wsJPEG}, b...))
		}
	}
}

// deliverOpus queues a packet of sound for sending. Sound comes from a game in lumps, a
// frame's worth at a time, and a browser takes lumps for an uneven network and holds back
// enough to cover them: over 100 ms. Sent evenly, it holds a fraction of that.
func (s *Session) deliverOpus(p []byte) {
	select {
	case s.opusOut <- p:
	default: // something has stuck, and the sound is better for losing a moment than for lagging
	}
}

// opusSender sends sound evenly, as fast as it plays. A backlog is sent as it comes, so that
// delay can't grow. The lump an encoder gives when it starts is dropped, as a browser would
// only hold on to it and be that much later for good.
func (s *Session) opusSender() {
	const packet = 10 * time.Millisecond
	var next time.Time
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		var p []byte
		select {
		case <-s.done:
			return
		case p = <-s.opusOut:
		}
		if time.Since(time.Unix(0, s.opusStarted.Load())) < opusSettle {
			continue
		}
		// A steady schedule, not a gap after each: timers run late, and sound mustn't.
		now := time.Now()
		// After a gap, the sound's own clock has to jump over it. A game that stalls for a
		// second otherwise looks like a network that held a packet up for a second, and a
		// browser then holds back up to a second of sound for half a minute, in case.
		var gap uint16
		if late := now.Sub(next); late > 3*packet {
			if !next.IsZero() {
				gap = uint16(min(late/packet, 60000))
			}
			next = now
		}
		if wait := next.Sub(now); wait > 0 && len(s.opusOut) < opusBacklog {
			timer.Reset(wait)
			select {
			case <-s.done:
				return
			case <-timer.C:
			}
		}
		next = next.Add(packet)
		for _, v := range s.snapshotViewers() {
			if v.audio != nil {
				_ = v.audio.WriteSample(media.Sample{Data: p, Duration: packet, PrevDroppedPackets: gap})
			}
		}
	}
}

// opusBacklog is how many packets may wait before sending stops being paced: a game's frame
// of sound can be 2 of them at 60 frames a second, and 4 at 30. opusSettle is how long after
// an encoder starts that what it gives is dropped.
const (
	opusBacklog = 6
	opusSettle  = 300 * time.Millisecond
)

func (s *Session) deliverPCM(b []byte) {
	vs := s.snapshotViewers()
	var msg []byte
	for _, v := range vs {
		if v.ws == nil {
			continue
		}
		if msg == nil {
			msg = make([]byte, 5, 5+len(b))
			msg[0] = wsPCM
			s.viewMu.Lock()
			binary.LittleEndian.PutUint32(msg[1:], uint32(s.rate))
			s.viewMu.Unlock()
			msg = append(msg, b...)
		}
		v.send(websocket.MessageBinary, msg)
	}
}

func (s *Session) broadcastEvent(b []byte) {
	for _, v := range s.snapshotViewers() {
		if dc := v.events.Load(); dc != nil {
			_ = dc.SendText(string(b))
		} else if v.ws != nil {
			v.send(websocket.MessageText, b)
		}
	}
}

// send queues a message without waiting, and reports whether it fit.
func (v *viewer) send(t websocket.MessageType, b []byte) bool {
	select {
	case v.out <- wsMessage{t, b}:
		return true
	default:
		return false
	}
}

// ---- WebRTC ----

// udpShared is the one UDP port all sessions share when one is configured.
type udpShared struct {
	mux  ice.UDPMux
	conn net.PacketConn
	port int
}

func (m *Manager) sharedUDP(port int) (ice.UDPMux, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.udp != nil && m.udp.port == port {
		return m.udp.mux, nil
	}
	if m.udp != nil {
		_ = m.udp.mux.Close()
		m.udp = nil
	}
	bind := ""
	if m.cfg.LoopbackOnly {
		bind = "127.0.0.1"
	}
	conn, err := net.ListenPacket("udp", net.JoinHostPort(bind, itoa(port)))
	if err != nil {
		return nil, err
	}
	mux := webrtc.NewICEUDPMux(nil, conn)
	m.udp = &udpShared{mux: mux, conn: conn, port: port}
	return mux, nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// MaxOfferSize bounds an SDP offer; a real one is a few kilobytes.
const MaxOfferSize = 64 << 10

const (
	iceDisconnected = 3 * time.Second
	iceFailed       = 6 * time.Second
	iceKeepalive    = time.Second
)

// Answer connects a browser over WebRTC: it takes the browser's offer and returns
// the answer. prefer is "" or "vp8". Which codec is used depends on what the
// browser offered and what FFmpeg here can encode.
func (s *Session) Answer(ctx context.Context, offer, prefer string) (string, error) {
	if len(offer) > MaxOfferSize {
		return "", errors.New("the connection offer is too large")
	}
	select {
	case <-s.done:
		return "", ErrStopped
	default:
	}
	codec := s.pickCodec(offer, prefer)
	if codec == "" {
		return "", errors.New("this browser and this server have no video format in common")
	}
	me := &webrtc.MediaEngine{}
	videoCap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}
	if codec == "vp8" {
		videoCap = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}
	}
	audioCap := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: videoCap, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return "", err
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: audioCap, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return "", err
	}
	if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		return "", err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return "", err
	}
	var se webrtc.SettingEngine
	cfg := s.m.config()
	if cfg.UDPPort > 0 {
		mux, err := s.m.sharedUDP(cfg.UDPPort)
		if err != nil {
			s.log.Warn("the WebRTC UDP port is unavailable; using any free port", "port", cfg.UDPPort, "err", err)
		} else {
			se.SetICEUDPMux(mux)
		}
	}
	if len(cfg.PublicIPs) > 0 {
		se.SetNAT1To1IPs(cfg.PublicIPs, webrtc.ICECandidateTypeHost)
	}
	// A connection that has really gone should pause the game quickly, not run it unwatched
	// for the half minute the defaults allow.
	se.SetICETimeouts(iceDisconnected, iceFailed, iceKeepalive)
	if cfg.LoopbackOnly {
		se.SetIncludeLoopbackCandidate(true)
		se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	var ice []webrtc.ICEServer
	if len(cfg.STUN) > 0 {
		ice = append(ice, webrtc.ICEServer{URLs: cfg.STUN})
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return "", err
	}
	v := &viewer{s: s, codec: codec, pc: pc}
	v.connected.Store(time.Now())
	s.holdPending(v)
	fail := func(err error) (string, error) { v.close(); return "", err }

	video, err := newVideoSender(codec, videoCap)
	if err != nil {
		return fail(err)
	}
	// Its own stream, not the video's: tracks of one stream are held back to match each
	// other, and sound's small buffer would become the picture's delay. Both follow the
	// same clock in real time, so they can't drift apart.
	audio, err := webrtc.NewTrackLocalStaticSample(audioCap, "audio", "wittyplay-audio")
	if err != nil {
		return fail(err)
	}
	v.video, v.audio = video, audio
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return fail(err)
	}
	var videoSender *webrtc.RTPSender
	for _, track := range []webrtc.TrackLocal{video.track, audio} {
		sender, err := pc.AddTrack(track)
		if err != nil {
			return fail(err)
		}
		if track == webrtc.TrackLocal(video.track) {
			videoSender = sender
		}
		go func() { // the interceptors act on what arrives here
			for {
				pkts, _, err := sender.ReadRTCP()
				if err != nil {
					return
				}
				if track == webrtc.TrackLocal(video.track) {
					s.noteFeedback(v, pkts)
				}
			}
		}()
	}
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "input":
			dc.OnMessage(func(m webrtc.DataChannelMessage) {
				if !m.IsString {
					s.Input(m.Data)
				}
			})
		case "events":
			dc.OnOpen(func() { v.events.Store(dc) })
		case "timing":
			dc.OnOpen(func() { v.timing.Store(dc) })
			dc.OnMessage(func(m webrtc.DataChannelMessage) {
				if !m.IsString {
					v.timingMessage(m.Data, func(b []byte) { _ = dc.Send(b) })
				}
			})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateConnected:
			v.connected.Store(time.Now())
			s.attach(v)
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			v.close()
		}
	})
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return fail(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return fail(err)
	}
	video.useExtensions(videoSender)
	if enc := videoSender.GetParameters().Encodings; len(enc) > 0 {
		v.videoSSRC.Store(uint32(enc[0].SSRC))
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-time.After(6 * time.Second): // answer with what has been found so far
	}
	go func() { // a browser that never connects must not hold this forever
		select {
		case <-time.After(30 * time.Second):
			if pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
				v.close()
			}
		case <-s.done:
			v.close()
		}
	}()
	return pc.LocalDescription().SDP, nil
}

func (s *Session) pickCodec(offer, prefer string) string {
	hasH264 := strings.Contains(offer, "H264/90000") && len(s.chain("h264")) > 0
	hasVP8 := strings.Contains(offer, "VP8/90000") && len(s.chain("vp8")) > 0
	switch {
	case prefer == "vp8" && hasVP8:
		return "vp8"
	case hasH264:
		return "h264"
	case hasVP8:
		return "vp8"
	}
	return ""
}

// ---- WebSocket: the path that works wherever HTTP does ----

// ServeWS is the fallback transport. mode is "h264" (decoded with WebCodecs) or
// "jpeg". Origin checks are done by Accept, as for the rest of the API.
func (s *Session) ServeWS(w http.ResponseWriter, r *http.Request, mode string) {
	if mode != "h264" {
		mode = "jpeg"
	}
	if mode == "h264" && len(s.chain("h264")) == 0 {
		mode = "jpeg"
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	c.SetReadLimit(4096)
	v := &viewer{s: s, codec: mode, ws: c, out: make(chan wsMessage, 16)}
	v.waitKey.Store(true)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer v.close()
	hello, _ := json.Marshal(map[string]any{"type": "hello", "video": mode})
	v.send(websocket.MessageText, hello)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				_ = c.Close(websocket.StatusNormalClosure, "the game has ended")
				return
			case m := <-v.out:
				wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
				err := c.Write(wctx, m.typ, m.data)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	s.attach(v)
	for {
		t, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		if t == websocket.MessageBinary {
			if !v.timingMessage(b, func(out []byte) { v.send(websocket.MessageBinary, out) }) {
				s.Input(b)
			}
		}
	}
}
