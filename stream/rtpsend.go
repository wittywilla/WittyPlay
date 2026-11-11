// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

// videoSender cuts encoded frames into RTP packets for one viewer. It's here rather than
// left to a ready-made track for two reasons: each frame's timestamp is known, which ties
// a frame on the screen back to its record, and each frame can say how long it may wait.
type videoSender struct {
	track *webrtc.TrackLocalStaticRTP
	mu    sync.Mutex
	pack  rtp.Packetizer

	playoutID atomic.Uint32 // the header extension's number on this connection; 0 if it has none
	playout   atomic.Pointer[[]byte]
}

const (
	// rtpMTU leaves room for encryption and extensions inside a packet that crosses any
	// network without being split.
	rtpMTU       = 1200
	videoRTPRate = 90000

	// playoutDelayURI is the extension that tells a browser how long a frame may wait
	// before it's shown. A page can only ask for a smaller buffer. This is the sender saying
	// there is none to keep: with both limits at zero, Chromium's decoder takes each frame
	// as it arrives and its renderer shows the newest at the next chance. Without it, a
	// frame still waits for an estimate of the network's jitter, and ten milliseconds more.
	playoutDelayURI = "http://www.webrtc.org/experiments/rtp-hdrext/playout-delay"
)

// How long a viewer's browser may hold frames, for each choice between speed and steadiness,
// in the extension's units of 10 ms.
func playoutLimits(mode LatencyMode) (minDelay, maxDelay uint16, on bool) {
	switch mode {
	case LatencySteady:
		return 0, 0, false // the browser's own judgement: its jitter estimate, in full
	case LatencyBalanced:
		return 0, 8, true // no extra wait, but it may ride out a bad moment
	}
	return 0, 0, true
}

// LatencyMode is the choice a person makes between speed and steadiness.
type LatencyMode byte

const (
	LatencyFastest LatencyMode = iota
	LatencyBalanced
	LatencySteady
)

func newVideoSender(codec string, capability webrtc.RTPCodecCapability) (*videoSender, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(capability, "video", "wittyplay-video")
	if err != nil {
		return nil, err
	}
	var payloader rtp.Payloader = &codecs.H264Payloader{}
	if strings.EqualFold(codec, "vp8") {
		payloader = &codecs.VP8Payloader{EnablePictureID: true}
	}
	// The track writes the numbers each connection has agreed on over the two left at zero.
	v := &videoSender{track: track, pack: rtp.NewPacketizer(rtpMTU, 0, 0, payloader, rtp.NewRandomSequencer(), videoRTPRate)}
	v.setLatency(LatencyFastest)
	return v, nil
}

// setLatency says how long the browser may hold frames from now on.
func (v *videoSender) setLatency(mode LatencyMode) {
	minDelay, maxDelay, on := playoutLimits(mode)
	if !on {
		v.playout.Store(nil)
		return
	}
	b, err := rtp.PlayoutDelayExtension{MinDelay: minDelay, MaxDelay: maxDelay}.Marshal()
	if err == nil {
		v.playout.Store(&b)
	}
}

// useExtensions finds out what this connection agreed to.
func (v *videoSender) useExtensions(sender *webrtc.RTPSender) {
	for _, ext := range sender.GetParameters().HeaderExtensions {
		if ext.URI == playoutDelayURI {
			v.playoutID.Store(uint32(ext.ID))
		}
	}
}

// write sends one frame and returns its RTP timestamp.
func (v *videoSender) write(frame []byte, dur time.Duration) uint32 {
	v.mu.Lock()
	defer v.mu.Unlock()
	packets := v.pack.Packetize(frame, uint32(dur.Seconds()*videoRTPRate))
	var ts uint32
	for i, p := range packets {
		if i == 0 {
			ts = p.Timestamp
			// A frame takes what its first packet says.
			if id, delay := v.playoutID.Load(), v.playout.Load(); id != 0 && delay != nil {
				_ = p.Header.SetExtension(uint8(id), *delay)
			}
		}
		if err := v.track.WriteRTP(p); err != nil {
			return ts
		}
	}
	return ts
}
