package stream

import (
	"sync"
	"time"

	"github.com/pion/rtcp"
)

// noteFeedback acts on what a browser says about its video. A browser that has lost track
// of the picture asks for a keyframe, and it would otherwise be looking at a broken or
// frozen picture until the next one comes round. Its regular reports say how the network
// is doing, and what's sent is matched to that.
func (s *Session) noteFeedback(v *viewer, pkts []rtcp.Packet) {
	for _, p := range pkts {
		switch r := p.(type) {
		case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
			if !v.direct.Load() {
				s.keyframeAsked(v.codec)
			}
		case *rtcp.ReceiverReport:
			// What was sent before the browser had finished connecting counts as lost, so the
			// first reports say nothing about the network.
			if time.Since(v.connected.Load().(time.Time)) < rateSettle {
				continue
			}
			for _, rep := range r.Reports {
				if rep.SSRC == v.videoSSRC.Load() {
					s.noteReport(v, rep)
				}
			}
		}
	}
}

// keyMinGap keeps a browser that keeps asking from holding the stream in a restart loop.
const (
	keyMinGap  = 2 * time.Second
	keyDueSoon = 300 * time.Millisecond
)

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
	// The picture stops for a moment while another encoder starts, so if a keyframe is due
	// about now anyway, it's better to wait for that.
	if due := time.Unix(0, s.lastKey.Load()).Add(time.Second); s.lastKey.Load() != 0 && due.Sub(now) < keyDueSoon {
		return
	}
	s.restartEncoderLocked(codec, now)
}

func (s *Session) restartEncoderLocked(codec string, now time.Time) {
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

// How what's sent follows the network. A steady picture matters more than a sharp one, so
// it comes down quickly and goes back up slowly. A change means a new encoder and so a
// keyframe, which is itself a burst on the network, so changes are kept far apart.
//
// Round trips that grow say a queue is filling, but only when they stay grown: a queue takes
// seconds to fill and to drain. A machine that's busy for a moment, the server's or the
// browser's, answers late once or twice and then catches up, and that's no reason to make the
// picture worse and stop it while a new encoder starts. Measured on one busy machine, the
// browser on loopback reported round trips of 45 ms, 133 ms, 3.6 s and 4.3 s against a usual
// 1 to 3, each followed by a step down and back.
const (
	rateLossLimit   = 0.03                    // more than this lost means the network is full
	rateRTTGrowth   = 40 * time.Millisecond   // round trips this far over the best seen mean its queues are filling
	rateChangeEvery = 5 * time.Second         // the least time between changes
	rateRecovery    = 20 * time.Second        // how long it has to be calm before going back up
	rateDown        = 0.7                     // the step down
	rateUp          = 1.25                    // the step up
	rateFloor       = 0.1                     // never less than this share of the full rate
	rateHeavyLoss   = 0.15                    // more than this lost is a network far too small
	rateMargin      = 0.85                    // how far under what got through to aim
	rateChangeHeavy = 1500 * time.Millisecond // the least time between changes for that
	rateRTTMemory   = 30 * time.Second        // how long the best round trip counts for
	rateSettle      = 3 * time.Second         // how long after connecting before reports count
	rateRTTReports  = 4                       // reports in a row with grown round trips before they count
	rateRTTSane     = time.Second             // a longer one is a stalled machine, not a queue
)

// rateControl is how much of its full bitrate the game's video gets, for all its viewers.
type rateControl struct {
	mu           sync.Mutex
	scale        float64
	badReports   int
	heavyReports int
	slowReports  int // in a row, with round trips grown
	lastChange   time.Time
	lastBad      time.Time
	bestRTT      time.Duration
	bestRTTAt    time.Time
}

// rate is the share of the full bitrate to encode at.
func (c *rateControl) rate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scale == 0 {
		return 1
	}
	return c.scale
}

// note takes one report and says whether the rate has changed.
func (c *rateControl) note(lost float64, rtt time.Duration, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.scale == 0 {
		c.scale = 1
	}
	// Heavy loss says how much is getting through, so it can go straight there rather
	// than finding it a step at a time while nothing arrives. That's worth a change at once.
	if lost > rateHeavyLoss {
		c.heavyReports++
		if c.heavyReports >= 2 && now.Sub(c.lastChange) >= rateChangeHeavy && c.scale > rateFloor {
			c.scale = max(c.scale*(1-lost)*rateMargin, rateFloor)
			c.lastChange, c.lastBad, c.badReports, c.heavyReports = now, now, 0, 0
			return true
		}
	} else {
		c.heavyReports = 0
	}
	if rtt > rateRTTSane {
		rtt, c.slowReports = 0, 0 // a stalled machine, which also explains the slow ones before it
	}
	if rtt > 0 && (c.bestRTT == 0 || rtt < c.bestRTT || now.Sub(c.bestRTTAt) > rateRTTMemory) {
		c.bestRTT, c.bestRTTAt = rtt, now
	}
	if rtt > 0 && c.bestRTT > 0 && rtt > c.bestRTT+rateRTTGrowth+c.bestRTT/2 {
		c.slowReports++
	} else if rtt > 0 {
		c.slowReports = 0
	}
	bad := lost > rateLossLimit || c.slowReports >= rateRTTReports
	if bad {
		c.badReports++
		c.lastBad = now
	} else {
		c.badReports = 0
	}
	if now.Sub(c.lastChange) < rateChangeEvery {
		return false
	}
	switch {
	case c.badReports >= 2 && c.scale > rateFloor:
		c.scale = max(c.scale*rateDown, rateFloor)
	case !bad && c.scale < 1 && now.Sub(c.lastBad) > rateRecovery:
		c.scale = min(c.scale*rateUp, 1)
	default:
		return false
	}
	c.lastChange, c.badReports = now, 0
	return true
}

// noteReport takes a browser's report on one stream. Only the video's counts: sound is too
// little to fill a network, and its reports say nothing about what the picture is doing.
func (s *Session) noteReport(v *viewer, rep rtcp.ReceptionReport) {
	now := time.Now()
	var rtt time.Duration
	if rep.LastSenderReport != 0 {
		// Both are the middle 32 bits of an NTP time: 1/65536ths of a second.
		ntp := uint32(toNTP(now) >> 16)
		rtt = time.Duration(uint64(ntp-rep.LastSenderReport-rep.Delay)) * time.Second / 65536
		if rtt > 10*time.Second {
			rtt = 0 // a clock problem, not a network
		}
	}
	if s.rates.note(float64(rep.FractionLost)/256, rtt, now) {
		s.log.Info("matching the video to the network", "share", s.rates.rate(), "lost", float64(rep.FractionLost)/256, "rtt", rtt.String())
		s.viewMu.Lock()
		s.restartEncoderLocked(v.codec, now)
		s.viewMu.Unlock()
	}
}

// toNTP is a time in the form RTCP uses.
func toNTP(t time.Time) uint64 {
	const ntpEpochOffset = 2208988800
	secs := uint64(t.Unix()) + ntpEpochOffset
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	return secs<<32 | frac
}
