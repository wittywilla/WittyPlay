package stream

import (
	"time"

	"github.com/pion/rtcp"
)

// noteFeedback acts on what a browser says about its video. A browser that has lost track
// of the picture asks for a keyframe, and it would otherwise be looking at a broken or
// frozen picture until the next one comes round.
func (s *Session) noteFeedback(v *viewer, pkts []rtcp.Packet) {
	for _, p := range pkts {
		switch p.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			s.keyframeAsked(v.codec)
		}
	}
}

// keyMinGap keeps a browser that keeps asking from holding the stream in a restart loop.
const keyMinGap = 2 * time.Second

// keyframeAsked gets a keyframe out soon. FFmpeg's command line has no way to be told, so
// the encoder is started again, which begins with one.
func (s *Session) keyframeAsked(codec string) {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	now := time.Now()
	if now.Sub(s.lastKeyAsk) < keyMinGap {
		return
	}
	s.lastKeyAsk = now
	switch {
	case codec == "h264" && s.h264 != nil:
		if now.Sub(s.h264.started) < keyMinGap {
			return // it has only just begun, with a keyframe
		}
		s.h264.stop()
		s.h264 = nil
	case codec == "vp8" && s.vp8 != nil:
		if now.Sub(s.vp8.started) < keyMinGap {
			return
		}
		s.vp8.stop()
		s.vp8 = nil
	}
}
