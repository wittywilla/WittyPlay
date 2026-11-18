// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// gpuLoad samples an NVIDIA card's load, and its video encoder's, once a second while a game
// plays, through the driver's own tool. Other cards aren't sampled, and say nothing.
type gpuLoad struct {
	mu       sync.Mutex
	gpu, enc []float64
	cancel   context.CancelFunc
}

func startGPULoad() *gpuLoad {
	g := &gpuLoad{}
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return g
	}
	ctx, cancel := context.WithCancel(context.Background())
	g.cancel = cancel
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			out, err := exec.CommandContext(ctx, path, "--query-gpu=utilization.gpu,utilization.encoder", "--format=csv,noheader,nounits", "-i", "0").Output()
			if err != nil {
				continue
			}
			a, b, _ := strings.Cut(strings.TrimSpace(string(out)), ",")
			gv, err1 := strconv.ParseFloat(strings.TrimSpace(a), 64)
			ev, err2 := strconv.ParseFloat(strings.TrimSpace(b), 64)
			if err1 == nil && err2 == nil {
				g.mu.Lock()
				g.gpu, g.enc = append(g.gpu, gv), append(g.enc, ev)
				g.mu.Unlock()
			}
		}
	}()
	return g
}

// reset forgets what was sampled so far: the start of a game isn't what it plays like.
func (g *gpuLoad) reset() {
	g.mu.Lock()
	g.gpu, g.enc = nil, nil
	g.mu.Unlock()
}

// stop ends the sampling and gives the median load of the card and of its encoder, in percent,
// or -1 for a card that wasn't sampled.
func (g *gpuLoad) stop() (gpu, enc float64) {
	if g.cancel != nil {
		g.cancel()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.gpu) == 0 {
		return -1, -1
	}
	return median(g.gpu), median(g.enc)
}
