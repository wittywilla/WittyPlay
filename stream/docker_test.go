// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// The server passes the stream on with the helper's own address as the Host, so a browser's
// Origin never matches it. That has to be let through, or the WebSocket never works; a page on
// another site still isn't.
func TestStreamThroughTheServersProxy(t *testing.T) {
	m := newManager(t)
	s := startGame(t, m, t.TempDir(), false)
	defer s.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.ServeStream(w, r, false) }))
	defer srv.Close()
	h := http.Header{"Origin": {"http://192.168.1.20:9797"}, ReachedHeader: {"192.168.1.20:9797"}}
	c, _, err := websocket.Dial(ctx, srv.URL, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("the server's own page was refused: %v", err)
	}
	c.CloseNow()
	h.Set("Origin", "https://elsewhere.example")
	if c, resp, err := websocket.Dial(ctx, srv.URL, &websocket.DialOptions{HTTPHeader: h}); err == nil {
		c.CloseNow()
		t.Fatal("a page on another site was let in")
	} else if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another site: %v", err)
	}
}

// Behind Docker's bridge, with no address set for browsers, WebRTC isn't offered: it can't get
// through, and trying it first only kept the picture away.
func TestNoWebRTCBehindABridge(t *testing.T) {
	was := behindBridge
	t.Cleanup(func() { behindBridge = was })
	enc := func(n string) bool { return n == "libx264" }
	behindBridge = func() bool { return true }
	if got := NewManager(Config{HaveEncoder: enc}).Modes(ClientBrowser); len(got) != 2 || got[0].ID != "ws-h264" {
		t.Fatalf("behind a bridge: %+v", got)
	}
	if got := NewManager(Config{HaveEncoder: enc, PublicIPs: []string{"192.168.1.20"}, UDPPort: 9798}).Modes(ClientBrowser); got[0].ID != "webrtc" {
		t.Fatalf("with the address set: %+v", got)
	}
	if got := NewManager(Config{HaveEncoder: enc, PublicIPs: []string{"192.168.1.20"}}).Modes(ClientBrowser); got[0].ID != "ws-h264" {
		t.Fatalf("an address but any port, which nobody published: %+v", got)
	}
	behindBridge = func() bool { return false }
	if got := NewManager(Config{HaveEncoder: enc}).Modes(ClientBrowser); got[0].ID != "webrtc" {
		t.Fatalf("on the host's network: %+v", got)
	}
}

// What the helper lets open a game's WebSocket, as the server passes it on: Host is always the
// helper's own address, and X-Witty-Reached is what the browser reached the server by.
func TestStreamOriginCheck(t *testing.T) {
	for _, c := range []struct {
		name                   string
		origin, fetch, reached string
		ok                     bool
	}{
		{"an app sends no Origin", "", "", "nas:9797", true},
		{"the server's own page", "http://nas:9797", "", "nas:9797", true},
		{"the browser says same-origin (a proxy in front changed Host)", "https://media.example.com", "same-origin", "127.0.0.1:9797", true},
		{"another site", "https://evil.example", "", "nas:9797", false},
		{"another site, the browser says so", "https://evil.example", "cross-site", "nas:9797", false},
		{"a sibling site claiming same Origin is still told by the browser", "http://nas:9797", "same-site", "nas:9797", false},
		{"a sandboxed page", "null", "", "nas:9797", false},
		{"an older server with no Reached: another site", "https://evil.example", "", "", false},
		{"no Reached, Origin is this Host", "http://127.0.0.1:46773", "", "", true},
		// Only "same-origin" lets a browser through on its word; anything else is checked by address.
		{"Sec-Fetch-Site none, another site", "https://evil.example", "none", "nas:9797", false},
		{"Sec-Fetch-Site none, the server's own page", "http://nas:9797", "none", "nas:9797", true},
		{"an unknown Sec-Fetch-Site, another site", "https://evil.example", "same-origin-ish", "nas:9797", false},
		{"no Sec-Fetch-Site, Origin not Reached though it is Host", "http://127.0.0.1:46773", "", "nas:9797", false},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:46773/play/x/stream", nil)
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if c.fetch != "" {
			r.Header.Set("Sec-Fetch-Site", c.fetch)
		}
		if c.reached != "" {
			r.Header.Set(ReachedHeader, c.reached)
		}
		if got := sameOrigin(r); got != c.ok {
			t.Errorf("%s: %v, want %v", c.name, got, c.ok)
		}
	}
}
