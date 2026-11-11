package stream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/wittywilla/wittyplay/ipc"
)

// A Relay is a game that isn't run by a runner: a standalone emulator in a container
// (sandbox/docker), whose own FFmpeg captures its display and sound and sends H.264 and Opus over
// RTP. The relay hands those packets, as they are, to the same kind of WebRTC connection a
// libretro game has, with the same data channels, so a client connects to either the same way.
// Input that arrives on the "input" channel goes to Input, which sends it to the container's
// input agent.
//
// Nothing is encoded here. The container keeps its keyframes close together (a GOP of a second),
// as there's no way to ask its FFmpeg for one; a browser that joins waits at most that long.
type Relay struct {
	id    string
	m     *Manager
	log   *slog.Logger
	opt   RelayOptions
	video *net.UDPConn
	audio *net.UDPConn

	vtrack *webrtc.TrackLocalStaticRTP
	atrack *webrtc.TrackLocalStaticRTP

	mu      sync.Mutex
	viewers map[*relayViewer]bool
	done    chan struct{}
	stop    sync.Once

	lastInput atomic.Int64 // Unix nanoseconds
	packets   atomic.Int64
	seq       atomic.Uint64
	from      atomic.Pointer[netip.Addr] // the only sender; nil: see RelayOptions.From
}

// RelayOptions start a relay.
type RelayOptions struct {
	UserID, ItemID int64
	Title, Console string
	Emulator       string
	Width, Height  int
	FPS            float64
	// Bind is the address the RTP arrives at: the host's address on WittyPlay's Docker network,
	// or 127.0.0.1. Never an address anything outside this machine can reach.
	Bind netip.Addr
	// From is the container's address. Packets from anywhere else are dropped. When it isn't
	// known yet, nothing is passed on until AllowFrom says it, unless Bind is loopback, where only
	// this machine can send anyway (Docker Desktop, whose containers arrive from its own proxy).
	From netip.Addr
	// Input receives each pad, pointer or touch message a viewer is allowed to send.
	Input func([]byte)
}

type relayViewer struct {
	pc     *webrtc.PeerConnection
	conn   string
	closed atomic.Bool
}

// maxRelays caps relays the same way MaxSessions caps games.
const maxRelays = 4

// StartRelay opens the two RTP ports for a container to send to. VideoPort and AudioPort say
// where they are; the container is started with them after this.
func (m *Manager) StartRelay(opt RelayOptions) (*Relay, error) {
	if !opt.Bind.IsValid() || !(opt.Bind.IsLoopback() || opt.Bind.IsPrivate()) {
		return nil, errors.New("a relay only listens on loopback or a private address")
	}
	m.mu.Lock()
	if m.relays == nil {
		m.relays = map[string]*Relay{}
	}
	if len(m.relays) >= maxRelays {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	m.mu.Unlock()
	listen := func() (*net.UDPConn, error) {
		return net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.AddrPortFrom(opt.Bind, 0)))
	}
	vc, err := listen()
	if err != nil {
		return nil, err
	}
	ac, err := listen()
	if err != nil {
		vc.Close()
		return nil, err
	}
	vtrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000,
		SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f"}, "video", "wittyplay-video")
	if err != nil {
		vc.Close()
		ac.Close()
		return nil, err
	}
	atrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "audio", "wittyplay-audio")
	if err != nil {
		vc.Close()
		ac.Close()
		return nil, err
	}
	id := newID()
	r := &Relay{id: id, m: m, log: m.log.With("relay", id, "emulator", opt.Emulator), opt: opt, video: vc, audio: ac, vtrack: vtrack, atrack: atrack,
		viewers: map[*relayViewer]bool{}, done: make(chan struct{})}
	r.lastInput.Store(time.Now().UnixNano())
	if opt.From.IsValid() {
		r.AllowFrom(opt.From)
	}
	m.mu.Lock()
	m.relays[id] = r
	m.mu.Unlock()
	go r.pump(vc, vtrack)
	go r.pump(ac, atrack)
	return r, nil
}

// GetRelay finds a relay.
func (m *Manager) GetRelay(id string) *Relay {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.relays[id]
}

// ID is the relay's unguessable name.
func (r *Relay) ID() string { return r.id }

// MarshalJSON names a relay to a server.
func (r *Relay) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID string `json:"id"`
	}{r.id})
}

// VideoPort and AudioPort are where the container sends RTP.
func (r *Relay) VideoPort() int { return r.video.LocalAddr().(*net.UDPAddr).Port }
func (r *Relay) AudioPort() int { return r.audio.LocalAddr().(*net.UDPAddr).Port }

// AllowFrom sets the one address RTP is taken from: the container's, once it has started.
func (r *Relay) AllowFrom(a netip.Addr) {
	a = a.Unmap()
	r.from.Store(&a)
}

// sender says whether a packet's source may send.
func (r *Relay) sender(from netip.Addr) bool {
	if want := r.from.Load(); want != nil {
		return from.Unmap() == *want
	}
	return r.opt.Bind.IsLoopback()
}

// Done is closed when the relay has stopped.
func (r *Relay) Done() <-chan struct{} { return r.done }

// LastInput is when someone last pressed something, for the container's idle limit.
func (r *Relay) LastInput() time.Time { return time.Unix(0, r.lastInput.Load()) }

// Packets counts RTP packets passed on, for a health check.
func (r *Relay) Packets() int64 { return r.packets.Load() }

// Stop closes the ports and every viewer.
func (r *Relay) Stop() {
	r.stop.Do(func() {
		close(r.done)
		r.video.Close()
		r.audio.Close()
		r.mu.Lock()
		vs := make([]*relayViewer, 0, len(r.viewers))
		for v := range r.viewers {
			vs = append(vs, v)
		}
		r.viewers = map[*relayViewer]bool{}
		r.mu.Unlock()
		for _, v := range vs {
			v.close()
		}
		r.m.mu.Lock()
		delete(r.m.relays, r.id)
		r.m.mu.Unlock()
	})
}

// pump passes RTP from one port to its track, as it comes. The track gives each connection the
// payload type and SSRC that connection agreed on.
func (r *Relay) pump(c *net.UDPConn, track *webrtc.TrackLocalStaticRTP) {
	defer r.guard()
	buf := make([]byte, 1500)
	var pkt rtp.Packet
	for {
		n, from, err := c.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if !r.sender(from.Addr()) {
			continue // only the container may send
		}
		if err := pkt.Unmarshal(buf[:n]); err != nil {
			continue
		}
		r.packets.Add(1)
		if err := track.WriteRTP(&pkt); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			r.log.Debug("relay write", "err", err)
		}
	}
}

func (r *Relay) guard() {
	if p := recover(); p != nil {
		r.log.Error("a relay crashed", "panic", p)
		r.Stop()
	}
}

// Connect is a client's WebRTC connection, as Session.Connect is for a game: the same hello, the
// same answer, the same data channels. Video is always H.264 and sound Opus, whatever the client
// prefers, as that's what the container sends.
func (r *Relay) Connect(ctx context.Context, hello Hello, offer string) (Connected, error) {
	hello, err := cleanHello(hello)
	if err != nil {
		return Connected{}, err
	}
	if len(offer) > MaxOfferSize {
		return Connected{}, streamErr(http.StatusRequestEntityTooLarge, CodeTooLarge, "the connection offer is too large")
	}
	select {
	case <-r.done:
		return Connected{}, streamErr(http.StatusNotFound, CodeNoSession, "this game has ended")
	default:
	}
	me := &webrtc.MediaEngine{}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: r.vtrack.Codec(), PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return Connected{}, err
	}
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: r.atrack.Codec(), PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return Connected{}, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		return Connected{}, err
	}
	se, ice := r.m.networking(r.log)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return Connected{}, err
	}
	v := &relayViewer{pc: pc, conn: "k" + strconv.FormatUint(r.seq.Add(1), 36)}
	fail := func(err error) (Connected, error) {
		v.close()
		return Connected{}, streamErr(http.StatusInternalServerError, CodeFailed, err.Error())
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer}); err != nil {
		return fail(err)
	}
	if hello.watching() {
		for _, t := range []webrtc.TrackLocal{r.vtrack, r.atrack} {
			sender, err := pc.AddTrack(t)
			if err != nil {
				return fail(err)
			}
			go func() {
				for {
					if _, _, err := sender.ReadRTCP(); err != nil {
						return
					}
				}
			}()
		}
	}
	info := r.streamInfo(v, hello.watching())
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "input":
			dc.OnMessage(func(m webrtc.DataChannelMessage) {
				if !m.IsString {
					r.input(m.Data)
				}
			})
		case "events":
			dc.OnOpen(func() {
				b, _ := json.Marshal(info)
				_ = dc.SendText(string(b))
			})
		}
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		switch st {
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			r.drop(v)
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
	select {
	case <-gathered:
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-time.After(6 * time.Second):
	}
	r.mu.Lock()
	var oldest *relayViewer
	r.viewers[v] = true
	if len(r.viewers) > maxViewers {
		for o := range r.viewers {
			if o != v {
				oldest = o
				break
			}
		}
		delete(r.viewers, oldest)
	}
	r.mu.Unlock()
	if oldest != nil {
		oldest.close()
	}
	return Connected{Stream: info, SDP: pc.LocalDescription().SDP}, nil
}

func (r *Relay) streamInfo(v *relayViewer, watch bool) StreamInfo {
	out := StreamInfo{Type: "stream", V: StreamVersion, Conn: v.conn, Transport: "webrtc", Width: r.opt.Width, Height: r.opt.Height, FPS: r.opt.FPS, Aspect: 0}
	if r.opt.Height > 0 {
		out.Aspect = float64(r.opt.Width) / float64(r.opt.Height)
	}
	if watch {
		out.Video, out.Audio, out.Rate = "h264", "opus", 48000
	}
	return out
}

// input passes on what the input agent understands: pad states, a pointer, touches. Anything
// else is dropped here, so the agent only ever parses fixed shapes.
func (r *Relay) input(p []byte) {
	ok := false
	switch {
	case len(p) == ipc.PointerSize && p[0] == ipc.PointerMark:
		_, _, _, ok = ipc.ParsePointer(p)
	case len(p) > 0 && p[0] == ipc.TouchesMark:
		_, ok = ipc.ParseTouches(p)
	default:
		var port int
		port, _, _, _, _, _, ok = ipc.ParseInput(p)
		ok = ok && port < MaxPlayers
	}
	if !ok || r.opt.Input == nil {
		return
	}
	r.lastInput.Store(time.Now().UnixNano())
	r.opt.Input(append([]byte(nil), p...))
}

func (r *Relay) drop(v *relayViewer) {
	r.mu.Lock()
	delete(r.viewers, v)
	r.mu.Unlock()
	v.close()
}

func (v *relayViewer) close() {
	if v.closed.CompareAndSwap(false, true) {
		_ = v.pc.Close()
	}
}

// Viewers counts connections.
func (r *Relay) Viewers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.viewers)
}
