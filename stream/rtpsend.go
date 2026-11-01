package stream

import (
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
)

// videoSender cuts encoded frames into RTP packets for one viewer. It's here rather than
// left to a ready-made track so that each frame's timestamp is known, which is what ties
// a frame on the screen back to its record.
type videoSender struct {
	track *webrtc.TrackLocalStaticRTP
	mu    sync.Mutex
	pack  rtp.Packetizer
}

// rtpMTU leaves room for encryption and extensions inside a packet that crosses any
// network without being split.
const (
	rtpMTU       = 1200
	videoRTPRate = 90000
)

func newVideoSender(codec string, capability webrtc.RTPCodecCapability) (*videoSender, error) {
	track, err := webrtc.NewTrackLocalStaticRTP(capability, "video", "wittyplay")
	if err != nil {
		return nil, err
	}
	var payloader rtp.Payloader = &codecs.H264Payloader{}
	if strings.EqualFold(codec, "vp8") {
		payloader = &codecs.VP8Payloader{EnablePictureID: true}
	}
	// The track writes the numbers each connection has agreed on over the two left at zero.
	return &videoSender{track: track, pack: rtp.NewPacketizer(rtpMTU, 0, 0, payloader, rtp.NewRandomSequencer(), videoRTPRate)}, nil
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
		}
		if err := v.track.WriteRTP(p); err != nil {
			return ts
		}
	}
	return ts
}
