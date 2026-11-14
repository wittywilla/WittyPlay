// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/wittywilla/wittyplay/ipc"
)

// What a client that asks for everything gets: frames that say they mustn't wait, a record of
// each frame that ties up with its RTP timestamp, a press followed through to a frame, and
// sound in a stream of its own.
func TestFramesAreMeasuredAndMayNotWait(t *testing.T) {
	m := newManager(t)
	s := startGame(t, m, t.TempDir(), false)
	defer s.Stop()

	me := &webrtc.MediaEngine{}
	if err := me.RegisterDefaultCodecs(); err != nil {
		t.Fatal(err)
	}
	if err := me.RegisterHeaderExtension(webrtc.RTPHeaderExtensionCapability{URI: playoutDelayURI}, webrtc.RTPCodecTypeVideo); err != nil {
		t.Fatal(err)
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, ir); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(ir)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	timestamps := map[uint32]bool{}
	streams := map[webrtc.RTPCodecType]string{}
	var withDelay, frames atomic.Int64
	var extID atomic.Uint32
	pc.OnTrack(func(tr *webrtc.TrackRemote, r *webrtc.RTPReceiver) {
		mu.Lock()
		streams[tr.Kind()] = tr.StreamID()
		mu.Unlock()
		for _, e := range r.GetParameters().HeaderExtensions {
			if e.URI == playoutDelayURI {
				extID.Store(uint32(e.ID))
			}
		}
		var last uint32
		for {
			p, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			if tr.Kind() != webrtc.RTPCodecTypeVideo || p.Timestamp == last {
				continue
			}
			// The first packet of each frame.
			last = p.Timestamp
			frames.Add(1)
			mu.Lock()
			timestamps[p.Timestamp] = true
			mu.Unlock()
			if id := extID.Load(); id != 0 {
				if b := p.Header.GetExtension(uint8(id)); b != nil {
					var d rtp.PlayoutDelayExtension
					if d.Unmarshal(b) == nil && d.MinDelay == 0 && d.MaxDelay == 0 {
						withDelay.Add(1)
					}
				}
			}
		}
	})
	input, err := pc.CreateDataChannel("input", &webrtc.DataChannelInit{Ordered: boolp(false), MaxRetransmits: uint16p(0)})
	if err != nil {
		t.Fatal(err)
	}
	timing, err := pc.CreateDataChannel("timing", &webrtc.DataChannelInit{Ordered: boolp(false), MaxRetransmits: uint16p(0)})
	if err != nil {
		t.Fatal(err)
	}
	var pongs, records, matched, sawPress atomic.Int64
	const pressNumber = 4242
	timing.OnMessage(func(msg webrtc.DataChannelMessage) {
		switch {
		case len(msg.Data) == 17 && msg.Data[0] == timingPong:
			if math.Float64frombits(binary.LittleEndian.Uint64(msg.Data[1:])) == 77 {
				pongs.Add(1)
			}
		case len(msg.Data) == traceRecordSize && msg.Data[0] == timingRecord:
			records.Add(1)
			mu.Lock()
			ok := timestamps[binary.LittleEndian.Uint32(msg.Data[10:])]
			mu.Unlock()
			if ok {
				matched.Add(1)
			}
			if binary.LittleEndian.Uint32(msg.Data[6:]) == pressNumber {
				sawPress.Add(1)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connected, err := s.Connect(ctx, Hello{V: StreamVersion}, pc.LocalDescription().SDP, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: connected.SDP}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "the channels", func() bool {
		return timing.ReadyState() == webrtc.DataChannelStateOpen && input.ReadyState() == webrtc.DataChannelStateOpen
	})
	ping := make([]byte, 9)
	ping[0] = timingPing
	binary.LittleEndian.PutUint64(ping[1:], math.Float64bits(77))
	if err := timing.Send(ping); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "an answer to the ping", func() bool { return pongs.Load() > 0 })
	if err := timing.Send([]byte{timingSubscribe, 1}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "frames and their records", func() bool { return frames.Load() > 30 && matched.Load() > 20 })
	if extID.Load() != 0 && withDelay.Load() == 0 {
		t.Fatal("no frame said it may not wait")
	}

	press := make([]byte, ipc.InputSizeStamped)
	binary.LittleEndian.PutUint16(press[1:], 1<<8)
	binary.LittleEndian.PutUint32(press[13:], pressNumber)
	if err := input.Send(press); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "a frame that saw the press", func() bool { return sawPress.Load() > 0 })

	mu.Lock()
	video, audio := streams[webrtc.RTPCodecTypeVideo], streams[webrtc.RTPCodecTypeAudio]
	mu.Unlock()
	if video == "" || video == audio {
		t.Fatalf("sound and picture should be separate streams, and are %q and %q", video, audio)
	}

	// Asking to stop means no more.
	if err := timing.Send([]byte{timingSubscribe, 0}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	before := records.Load()
	time.Sleep(300 * time.Millisecond)
	if after := records.Load(); after != before {
		t.Fatalf("records kept coming: %d then %d", before, after)
	}
}

// An encoder that dies should be replaced with no help from anyone, however many times.
func TestAStoppedEncoderIsReplaced(t *testing.T) {
	m := newManager(t)
	s := startGame(t, m, t.TempDir(), false)
	defer s.Stop()
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	for _, kind := range []webrtc.RTPCodecType{webrtc.RTPCodecTypeVideo, webrtc.RTPCodecTypeAudio} {
		if _, err := pc.AddTransceiverFromKind(kind, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	var packets atomic.Int64
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		buf := make([]byte, 1500)
		for {
			if _, _, err := tr.Read(buf); err != nil {
				return
			}
			if tr.Kind() == webrtc.RTPCodecTypeVideo {
				packets.Add(1)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connected, err := s.Connect(ctx, Hello{V: StreamVersion}, pc.LocalDescription().SDP, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: connected.SDP}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "video", func() bool { return packets.Load() > 30 })

	for i := 0; i < 2; i++ { // twice, as one that came back once might not again
		pid := s.Processes().Video
		if pid == 0 {
			t.Fatal("no encoder to stop")
		}
		kill(t, pid)
		waitFor(t, 10*time.Second, "another encoder", func() bool {
			p := s.Processes().Video
			return p != 0 && p != pid
		})
		before := packets.Load()
		waitFor(t, 10*time.Second, "the picture to carry on", func() bool { return packets.Load() > before+20 })
	}
}
