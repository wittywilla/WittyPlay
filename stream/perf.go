// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"slices"
	"sync"
)

// frameTimes keeps the last few seconds of how long the core took over each frame (from the
// runner starting it to the picture being ready, a graphics card's read-back included) and how
// long the picture took to reach the server. It's for measuring: wittyplay-probe reads it, and
// it costs two numbers a frame.
type frameTimes struct {
	mu      sync.Mutex
	core    [512]float64
	transit [512]float64
	n       int
}

func (f *frameTimes) add(coreMS, transitMS float64) {
	f.mu.Lock()
	i := f.n % len(f.core)
	f.core[i], f.transit[i] = coreMS, transitMS
	f.n++
	f.mu.Unlock()
}

// FrameTimes is the median and the 95th percentile, in milliseconds, of the core's time on a
// frame and of the picture's way to the server, over the newest frames (up to 512).
type FrameTimes struct {
	CoreP50, CoreP95       float64
	TransitP50, TransitP95 float64
	Frames                 int
}

func (f *frameTimes) take() FrameTimes {
	f.mu.Lock()
	n := min(f.n, len(f.core))
	core := slices.Clone(f.core[:n])
	transit := slices.Clone(f.transit[:n])
	f.n = 0
	f.mu.Unlock()
	if n == 0 {
		return FrameTimes{}
	}
	slices.Sort(core)
	slices.Sort(transit)
	at := func(v []float64, q float64) float64 { return v[min(len(v)-1, int(float64(len(v))*q))] }
	return FrameTimes{CoreP50: at(core, 0.5), CoreP95: at(core, 0.95), TransitP50: at(transit, 0.5), TransitP95: at(transit, 0.95), Frames: n}
}

// FrameTimes says how long the newest frames took in the core and on their way to the server,
// and starts counting again.
func (s *Session) FrameTimes() FrameTimes { return s.frameTimes.take() }

// RunnerPID is the game runner's process, for measuring what it uses; 0 when there's none.
func (s *Session) RunnerPID() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}
