// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/pion/webrtc/v4"
)

func viewerTestSession(t *testing.T) *Session {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(Config{Runner: self, RunnerArgs: []string{"-test.run=^TestFakeRunner$", "--", "fine"}, FFmpeg: "ffmpeg-is-not-needed",
		HaveEncoder: func(string) bool { return true }, Log: slog.New(slog.DiscardHandler)})
	s, err := m.Start(Options{UserID: 1, ItemID: 7, Title: "Test", Core: "fake", ROM: "fake", SystemDir: t.TempDir(), SaveDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// One account can open a game once and then connect to it without end. Over the limit
// the oldest goes, so a reload or another device simply carries on.
func TestAGameTakesOnlySoManyViewersOverWebSocket(t *testing.T) {
	s := viewerTestSession(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeWS(w, r, "jpeg") }))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var conns []*websocket.Conn
	for i := range maxViewers + 6 {
		c, _, err := websocket.Dial(ctx, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer c.CloseNow()
		conns = append(conns, c)
		deadline := time.Now().Add(5 * time.Second)
		for len(s.snapshotViewers()) < min(i+1, maxViewers) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(s.snapshotViewers()); n > maxViewers {
		t.Fatalf("%d viewers, most allowed %d", n, maxViewers)
	}
	// The first was let go and told, and the newest is still there.
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
	defer rcancel()
	for {
		if _, _, err := conns[0].Read(rctx); err != nil {
			if rctx.Err() != nil {
				t.Fatal("the oldest viewer is still connected")
			}
			break
		}
	}
	found := false
	for _, v := range s.snapshotViewers() {
		if v.seq == s.viewSeq {
			found = true
		}
	}
	if !found {
		t.Error("the newest viewer isn't one of them")
	}
}

// An offer costs a peer connection, with its sockets and goroutines, whether or not anything ever
// connects. The test core isn't needed: this is only about what a hostile client can hold open.
func TestOffersThatNeverConnectAreLetGo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gathering listens on the network, which asks the firewall on Windows")
	}
	s := viewerTestSession(t)
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-gathered
	sdp := pc.LocalDescription().SDP

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	before := runtime.NumGoroutine()
	const offers = 40
	for range offers {
		if _, err := s.Answer(ctx, sdp, ""); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	grew := 0
	for time.Now().Before(deadline) {
		grew = runtime.NumGoroutine() - before
		if grew < 400 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// About 20 goroutines a peer, measured: 40 held would be 800.
	if grew >= 400 {
		t.Fatalf("%d goroutines after %d offers that never connected", grew, offers)
	}
}
