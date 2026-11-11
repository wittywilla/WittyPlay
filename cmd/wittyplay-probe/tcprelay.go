// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"io"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

// The same network as relay, for what goes over TCP: the page, its offer and answer, and the
// WebSocket modes. Loss can't be made on a TCP connection from outside the kernel, so it's
// modelled the way TCP behaves after a loss: the lost segment arrives late, after a
// retransmission, and everything behind it waits for it, because TCP hands over bytes only
// in order. A segment with at least three behind it is resent after one round trip (fast
// retransmit). One at the end of a burst has nothing behind it to say it's missing, and is
// resent after a tail-loss probe: two round trips, and one more for the resend. That's kinder
// than real TCP, which also halves its window after a loss; it's the least a loss costs.

const tcpSegment = 1448 // what one packet carries on most networks

type tcpRelay struct {
	im    impairment
	ln    net.Listener
	back  string
	start time.Time
	busy  int64 // when the limited link is free, in nanoseconds since start
	mu    sync.Mutex
}

func startTCPRelay(bind, back string, im impairment) (*tcpRelay, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, "0"))
	if err != nil {
		return nil, err
	}
	r := &tcpRelay{im: im, ln: ln, back: back, start: time.Now()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.carry(c)
		}
	}()
	return r, nil
}

func (r *tcpRelay) addr() string { return r.ln.Addr().String() }

func (r *tcpRelay) carry(front net.Conn) {
	back, err := net.Dial("tcp", r.back)
	if err != nil {
		front.Close()
		return
	}
	done := make(chan struct{}, 2)
	go func() { r.pipe(front, back, false); done <- struct{}{} }()
	go func() { r.pipe(back, front, true); done <- struct{}{} }()
	<-done
	front.Close()
	back.Close()
	<-done
}

type tcpChunk struct {
	b  []byte
	at time.Time
}

// pipe copies one direction. Toward the browser it adds jitter, the link's speed and loss;
// the other way, only delay.
func (r *tcpRelay) pipe(from, to net.Conn, down bool) {
	q := make(chan tcpChunk, 4096)
	go func() {
		defer to.Close()
		for c := range q {
			if d := time.Until(c.at); d > 0 {
				time.Sleep(d)
			}
			if _, err := to.Write(c.b); err != nil {
				for range q {
				}
				return
			}
		}
	}()
	defer close(q)
	rtt := max(r.im.delay, 10*time.Millisecond)
	var last time.Time // TCP hands over bytes in order: nothing goes before what's ahead of it
	buf := make([]byte, 64<<10)
	for {
		n, err := from.Read(buf)
		if n > 0 {
			now := time.Now()
			segs := (n + tcpSegment - 1) / tcpSegment
			for i := 0; i < segs; i++ {
				seg := append([]byte(nil), buf[i*tcpSegment:min((i+1)*tcpSegment, n)]...)
				at := now.Add(r.im.delay / 2)
				if down {
					if r.im.jitter > 0 {
						at = at.Add(time.Duration(rand.Float64() * float64(r.im.jitter)))
					}
					if r.im.kbps > 0 {
						at = at.Add(r.link(len(seg)))
					}
					if r.im.loss > 0 && rand.Float64() < r.im.loss {
						if segs-i > 3 {
							at = at.Add(rtt)
						} else {
							at = at.Add(3 * rtt)
						}
					}
				}
				if at.Before(last) {
					at = last
				}
				last = at
				q <- tcpChunk{seg, at}
			}
			// A full link pushes back on the sender, as TCP's window would, rather than
			// queueing without end: the server sees its writes wait.
			if down && r.im.kbps > 0 {
				if ahead := time.Until(last) - r.im.delay/2 - r.im.queue; ahead > 0 {
					time.Sleep(ahead)
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

// link is how long a segment waits for the limited link, including its own time on it.
func (r *tcpRelay) link(n int) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Since(r.start).Nanoseconds()
	free := max(r.busy, now)
	took := int64(n*8) * 1e6 / int64(r.im.kbps)
	r.busy = free + took
	return time.Duration(r.busy - now)
}
