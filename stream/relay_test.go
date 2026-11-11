package stream

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/wittywilla/wittyplay/ipc"
)

// TestRelayCarriesRTPToWebRTC sends H.264 RTP as a container's FFmpeg would, and checks a WebRTC
// peer gets it, and that input from the peer reaches the input callback, filtered.
func TestRelayCarriesRTPToWebRTC(t *testing.T) {
	m := NewManager(Config{LoopbackOnly: true})
	inputs := make(chan []byte, 8)
	lo := netip.MustParseAddr("127.0.0.1")
	r, err := m.StartRelay(RelayOptions{Title: "t", Emulator: "test", Width: 640, Height: 480, FPS: 60, Bind: lo, From: lo, Input: func(p []byte) { inputs <- p }})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	if m.GetRelay(r.ID()) != r {
		t.Fatal("not found by id")
	}

	// The browser's side.
	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	var se webrtc.SettingEngine
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	api := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithSettingEngine(se))
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *webrtc.TrackRemote, 2)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		got <- tr
		for {
			if _, _, err := tr.ReadRTP(); err != nil {
				return
			}
		}
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	_ = pc.SetLocalDescription(offer)
	<-gathered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := r.Connect(ctx, Hello{V: StreamVersion}, pc.LocalDescription().SDP)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Stream.Video != "h264" || conn.Stream.Audio != "opus" || conn.Stream.Width != 640 {
		t.Fatalf("stream info: %+v", conn.Stream)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: conn.SDP}); err != nil {
		t.Fatal(err)
	}

	// The container's side: RTP to the relay's port, from the allowed address.
	out, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.VideoPort()})
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		seq := uint16(0)
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			seq++
			p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: uint32(seq) * 1500, SSRC: 1234, Marker: true},
				Payload: []byte{0x65, 0x88, 0x84, 0x00}} // an IDR slice's NAL header and a little of it
			b, _ := p.Marshal()
			_, _ = out.Write(b)
		}
	}()

	select {
	case tr := <-got:
		if tr.Kind() != webrtc.RTPCodecTypeVideo {
			t.Fatalf("got %s first", tr.Kind())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no video reached the peer")
	}
	if r.Packets() == 0 {
		t.Fatal("no packets counted")
	}

	// Input: a pad state goes through, garbage doesn't.
	open := make(chan struct{})
	dc.OnOpen(func() { close(open) })
	select {
	case <-open:
	case <-time.After(10 * time.Second):
		t.Fatal("the input channel never opened")
	}
	_ = dc.Send([]byte{1, 2, 3}) // no such shape
	pad := make([]byte, ipc.InputSize)
	pad[1] = 1
	_ = dc.Send(pad)
	select {
	case p := <-inputs:
		if len(p) != ipc.InputSize {
			t.Fatalf("input %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no input arrived")
	}
	select {
	case p := <-inputs:
		t.Fatalf("garbage was passed on: %v", p)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestRelayRefusesPublicAddresses(t *testing.T) {
	m := NewManager(Config{})
	if _, err := m.StartRelay(RelayOptions{Bind: netip.MustParseAddr("0.0.0.0")}); err == nil {
		t.Error("bound to every address")
	}
	if _, err := m.StartRelay(RelayOptions{Bind: netip.MustParseAddr("8.8.8.8")}); err == nil {
		t.Error("bound to a public address")
	}
}

func TestRelayDropsStrangers(t *testing.T) {
	m := NewManager(Config{})
	r, err := m.StartRelay(RelayOptions{Bind: netip.MustParseAddr("127.0.0.1"), From: netip.MustParseAddr("127.0.0.2")})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Stop()
	out, _ := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.VideoPort()})
	defer out.Close()
	p := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: 1, SSRC: 1}, Payload: []byte{1}}
	b, _ := p.Marshal()
	for range 5 {
		_, _ = out.Write(b)
	}
	time.Sleep(100 * time.Millisecond)
	if r.Packets() != 0 {
		t.Fatalf("%d packets from a stranger were passed on", r.Packets())
	}
}
