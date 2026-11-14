// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"strconv"
	"testing"
	"time"
)

func TestPeerLimitsRefillAndForget(t *testing.T) {
	var p peerLimits
	now := time.Unix(1000, 0)
	for range peerOfferBurst {
		if !p.offer("a", now) {
			t.Fatal("within the burst")
		}
	}
	if p.offer("a", now) {
		t.Error("past the burst")
	}
	if !p.offer("b", now) {
		t.Error("another session isn't touched")
	}
	if p.offer("a", now.Add(peerOfferEvery-time.Second)) {
		t.Error("too soon")
	}
	if !p.offer("a", now.Add(peerOfferEvery)) {
		t.Error("one more after the wait")
	}
	if p.offer("a", now.Add(peerOfferEvery)) {
		t.Error("only one more")
	}
	// Sessions come and go, and the list doesn't keep them.
	for i := range peerSessionsKept * 3 {
		p.offer(strconv.Itoa(i), now.Add(time.Duration(i)*time.Second))
	}
	if len(p.m) > peerSessionsKept {
		t.Errorf("%d kept", len(p.m))
	}
	release, ok := p.socket("x", now)
	if !ok {
		t.Fatal("first socket")
	}
	release()
	release() // once is what counts
	if p.m["x"].sockets != 0 {
		t.Errorf("%d", p.m["x"].sockets)
	}
}
