// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KasmProxy is the only way to a container's KasmVNC: a reverse proxy that lets a request
// through only with that session's ticket in its path, and adds the container's own per-session
// password on the way in. The container's port is never published beyond 127.0.0.1, and the
// browser never learns the password.
//
// Why the ticket is in the path, and is the whole of the way in. The desktop's page and its
// scripts come from inside the container, and a container is treated as something that may
// have been taken over. So that page must never run as one of the server's own: it's shown in
// a sandboxed frame with an origin of its own (SandboxPolicy), where it can't read the app's
// storage or ask the server anything as the person signed in. A browser sends no sign-in cookie
// from such a frame, so the page's own requests, and its WebSocket, can't be told apart by who
// is signed in. They're told apart by the address: a random 256-bit ticket made for one
// session, given only to the person who started it, and gone when the game ends.
type KasmProxy struct {
	mu      sync.Mutex
	targets map[string]*kasmTarget // by session
}

// SandboxPolicy is the content policy every answer from a desktop carries, whatever the
// container sent: its own scripts and its own connection, in a sandbox without the server's
// origin. A frame that shows it sets the same sandbox.
const SandboxPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"connect-src 'self'; font-src 'self' data:; media-src 'self' blob:; worker-src 'self' blob:; object-src 'none'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts allow-pointer-lock"

type kasmTarget struct {
	upstream *url.URL
	user     string
	password string
	ticket   string
	expires  time.Time
	proxy    *httputil.ReverseProxy
}

const kasmUser = "wittyplay"

// NewSecret makes a random secret, 32 bytes as hex.
func NewSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the system's randomness is gone; nothing is safe to hand out
	}
	return hex.EncodeToString(b[:])
}

// Register makes a session reachable through the proxy. upstream is the container's KasmVNC:
// its address on WittyPlay's network, or 127.0.0.1 and its published port. password is the
// secret the container was started with. It returns the ticket for the browser.
func (p *KasmProxy) Register(session string, upstream *url.URL, password string, ttl time.Duration) (string, error) {
	if upstream == nil || upstream.Scheme != "http" && upstream.Scheme != "https" {
		return "", errors.New("bad KasmVNC address")
	}
	if host := upstream.Hostname(); !isPrivate(host) {
		return "", errors.New("KasmVNC is only ever reached on loopback or WittyPlay's own network")
	}
	if len(password) < 32 {
		return "", errors.New("KasmVNC needs a per-session secret")
	}
	t := &kasmTarget{upstream: upstream, user: kasmUser, password: password, ticket: NewSecret(), expires: time.Now().Add(ttl)}
	rp := httputil.NewSingleHostReverseProxy(upstream)
	orig := rp.Director
	rp.Director = func(r *http.Request) {
		orig(r)
		r.Host = upstream.Host
		// Nothing of the browser's own reaches the container but the request itself.
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		for k := range r.Header {
			if strings.HasPrefix(k, "X-Witty") || strings.HasPrefix(k, "X-Forwarded") {
				r.Header.Del(k)
			}
		}
		r.SetBasicAuth(t.user, t.password)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		// KasmVNC must never be able to ask the browser for its password.
		resp.Header.Del("WWW-Authenticate")
		resp.Header.Del("Set-Cookie")
		// The container doesn't choose how its own page is boxed in.
		resp.Header.Set("Content-Security-Policy", SandboxPolicy)
		resp.Header.Del("Content-Security-Policy-Report-Only")
		resp.Header.Del("Access-Control-Allow-Origin")
		resp.Header.Del("Access-Control-Allow-Credentials")
		resp.Header.Set("X-Content-Type-Options", "nosniff")
		resp.Header.Set("Referrer-Policy", "no-referrer")
		resp.Header.Set("Cache-Control", "no-store")
		// A redirect within the desktop stays within this session's address.
		if loc := resp.Header.Get("Location"); loc != "" {
			if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
				resp.Header.Set("Location", "/kasm/"+session+"/"+t.ticket+loc)
			} else if u, err := url.Parse(loc); err != nil || u.IsAbs() || strings.HasPrefix(loc, "//") {
				resp.Header.Del("Location") // never anywhere else
			}
		}
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Security-Policy", SandboxPolicy)
		http.Error(w, "the emulator's desktop isn't answering", http.StatusBadGateway)
	}
	t.proxy = rp
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.targets == nil {
		p.targets = map[string]*kasmTarget{}
	}
	p.targets[session] = t
	return t.ticket, nil
}

// Forget closes a session's way in.
func (p *KasmProxy) Forget(session string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.targets, session)
}

// Ticket returns a session's ticket, for the server to hand to the person playing.
func (p *KasmProxy) Ticket(session string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := p.targets[session]
	if t == nil || time.Now().After(t.expires) {
		return "", false
	}
	return t.ticket, true
}

// Path is where a session's desktop is, on the module's own routes: the session, then its ticket.
func (p *KasmProxy) Path(session string) (string, bool) {
	t, ok := p.Ticket(session)
	if !ok {
		return "", false
	}
	return "/kasm/" + session + "/" + t + "/", true
}

// ServeHTTP serves "<prefix>/<session>/<ticket>/...". prefix is stripped by the caller with
// http.StripPrefix. Anything without the session's ticket gets the same "not found" a session
// that doesn't exist gets.
func (p *KasmProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// An answer of this proxy's own; one from a container gets the same headers on its way out.
	none := func() {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", SandboxPolicy)
		http.Error(w, "not found", http.StatusNotFound)
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		none()
		return
	}
	session, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	given, rest, _ := strings.Cut(rest, "/")
	p.mu.Lock()
	t := p.targets[session]
	p.mu.Unlock()
	// Compared in constant time even when there's no such session, so the answer's timing says
	// nothing about which sessions exist.
	want := strings.Repeat("0", 64)
	if t != nil {
		want = t.ticket
	}
	ok := subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
	if t == nil || !ok || time.Now().After(t.expires) {
		none()
		return
	}
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rest
	r2.URL.RawPath = ""
	t.proxy.ServeHTTP(w, r2)
}

// isPrivate says a host is loopback or a private address: where a container can be.
func isPrivate(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// BasicAuthHeader is what the proxy adds, for a test or a health check.
func BasicAuthHeader(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(kasmUser+":"+password))
}

// LoopbackURL is a published KasmVNC port on this machine.
func LoopbackURL(port int) *url.URL {
	return &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
}
