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
// through only with that session's ticket, and adds the container's own per-session password
// on the way in. The container's port is never published beyond 127.0.0.1, and the browser
// never learns the password.
//
// A ticket is a random string the server gets for one person's session (Ticket) and gives to
// that person's browser. The route that serves the proxy sits behind WittyMediaServer's own
// sign-in like every other module route, so the ticket is a second lock, not the only one.
type KasmProxy struct {
	mu      sync.Mutex
	targets map[string]*kasmTarget // by session
}

type kasmTarget struct {
	upstream *url.URL
	user     string
	password string
	ticket   string
	expires  time.Time
	proxy    *httputil.ReverseProxy
}

// TicketHeader or the "ticket" query parameter carries a session's ticket; a WebSocket from a
// browser can't set headers, so the first request sets a cookie scoped to the session's path.
const (
	TicketHeader = "X-WittyPlay-Kasm-Ticket"
	ticketCookie = "wittyplay_kasm"
	kasmUser     = "wittyplay"
)

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
		// What the browser sent for WittyPlay stays with WittyPlay.
		r.Header.Del("Cookie")
		r.Header.Del(TicketHeader)
		r.Header.Del("Authorization")
		q := r.URL.Query()
		q.Del("ticket")
		r.URL.RawQuery = q.Encode()
		r.SetBasicAuth(t.user, t.password)
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		// KasmVNC must never be able to ask the browser for its password.
		resp.Header.Del("WWW-Authenticate")
		resp.Header.Del("Set-Cookie")
		return nil
	}
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
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

// ServeHTTP serves "<prefix>/<session>/...". prefix is stripped by the caller with
// http.StripPrefix; the first path element is the session.
func (p *KasmProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	session, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	p.mu.Lock()
	t := p.targets[session]
	p.mu.Unlock()
	if t == nil || time.Now().After(t.expires) {
		http.NotFound(w, r)
		return
	}
	given := r.Header.Get(TicketHeader)
	fromQuery := false
	if given == "" {
		if given = r.URL.Query().Get("ticket"); given != "" {
			fromQuery = true
		}
	}
	if given == "" {
		if c, err := r.Cookie(ticketCookie); err == nil {
			given = c.Value
		}
	}
	if subtle.ConstantTimeCompare([]byte(given), []byte(t.ticket)) != 1 {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	if fromQuery {
		// The page's own requests, and its WebSocket, carry it from here.
		http.SetCookie(w, &http.Cookie{Name: ticketCookie, Value: t.ticket, Path: pathFor(r, session), HttpOnly: true, Secure: r.TLS != nil,
			SameSite: http.SameSiteStrictMode, Expires: t.expires})
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + rest
	r2.URL.RawPath = ""
	t.proxy.ServeHTTP(w, r2)
}

// pathFor is the cookie's path: the part of the request's path up to and including the session.
func pathFor(r *http.Request, session string) string {
	full := r.URL.Path
	if r.RequestURI != "" {
		if u, err := url.ParseRequestURI(r.RequestURI); err == nil {
			full = u.Path
		}
	}
	if i := strings.Index(full, "/"+session); i >= 0 {
		return full[:i+1+len(session)]
	}
	return "/"
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
