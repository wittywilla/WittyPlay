// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import "time"

// A rateLadder decides the bitrate of a relayed stream from what browsers say they lost.
//
// A core's game is encoded here, so its bitrate can follow the connection frame by frame. A
// standalone emulator's picture is encoded by an FFmpeg beside it, and changing its bitrate
// means starting that FFmpeg again: a short freeze. So this moves in a few large steps, and
// slowly: down after loss has stayed high for several reports in a row, up only after a long
// clean stretch, and never twice within holdFor.
type rateLadder struct {
	top, now int
	bad      int
	good     int
	changed  time.Time
}

const (
	lossHigh    = 0.08 // more than this lost, report after report, and the picture is breaking up
	lossLow     = 0.01
	badReports  = 3  // browsers report about once a second
	goodReports = 45 // most of a minute without loss before trying more
	holdFor     = 20 * time.Second
	minKbps     = 800 // below this 720p isn't worth watching; the stream stays here
)

func newRateLadder(kbps int) *rateLadder {
	if kbps <= 0 {
		kbps = 6000
	}
	return &rateLadder{top: kbps, now: kbps}
}

// report takes one loss fraction (0 to 1) and says what the bitrate should be, and whether
// that's a change.
func (l *rateLadder) report(loss float64, at time.Time) (int, bool) {
	switch {
	case loss > lossHigh:
		l.bad, l.good = l.bad+1, 0
	case loss < lossLow:
		l.good, l.bad = l.good+1, 0
	default:
		l.bad, l.good = 0, 0
	}
	if !l.changed.IsZero() && at.Sub(l.changed) < holdFor {
		return l.now, false
	}
	next := l.now
	switch {
	case l.bad >= badReports:
		next = max(l.now*7/10, min(minKbps, l.top))
	case l.good >= goodReports:
		next = min(l.now*13/10, l.top)
	}
	if next == l.now {
		return l.now, false
	}
	l.now, l.bad, l.good, l.changed = next, 0, 0, at
	return next, true
}
