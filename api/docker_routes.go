// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/wittywilla/wittyplay/rpc"
)

// Standalone emulators' routes (rpc.DockerBackend.Routes): their settings, installs and sessions,
// and the KasmVNC desktop. The server used to pass these through itself (its
// api_module_proxy.go); now they are ordinary routes of the module's (integration.json), and the
// server checks sign-in and administrators as for any: the backend checks them again, and who owns
// a session, and whose settings a request changes.
//
// The desktop is a ticket route: it carries nobody's sign-in, because it's shown in a sandboxed
// frame. The server used to check that it had started the game and that the profile could still
// play; the server now ends a game whose profile may not go on (session.stop, sessions.go), so
// here a desktop is only let through while its game is still one of ours under a live session.

// dockerMux is the backend's own routes, which these pass to.
var dockerMux = sync.OnceValue(func() *http.ServeMux {
	m := http.NewServeMux()
	rpc.Docker.Routes(m)
	return m
})

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		// Every /docker/ route: the backend's mux says which there are, and refuses the rest.
		mux.Handle("/docker/", w(handleDocker))
		mux.Handle("GET /kasm/{sid}/{ticket}/{rest...}", http.HandlerFunc(handleKasm))
	})
}

// handleDocker passes a signed-in person's request to the backend, telling it where the BIOS
// folder is (which its pages say firmware is in), over anything a client sent.
func handleDocker(a *App, w http.ResponseWriter, r *http.Request) {
	r.Header.Set(rpc.HeaderSystemDir, a.SystemDir())
	dockerMux().ServeHTTP(w, r)
}

// kasmName is the shape of a session's name and of its ticket: hex, as the backend makes them.
// Anything else isn't passed on at all.
func kasmName(v string, least, most int) bool {
	if len(v) < least || len(v) > most {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// handleKasm passes the KasmVNC desktop through, WebSocket included, to whoever holds the
// session's ticket (rpc's KasmProxy checks the ticket). Every refusal is the same "not found": the
// address is all a caller has shown.
func handleKasm(w http.ResponseWriter, r *http.Request) {
	sid, ticket := r.PathValue("sid"), r.PathValue("ticket")
	if !kasmName(sid, 8, 64) || !kasmName(ticket, 64, 64) {
		writeErr(w, 404, "not found")
		return
	}
	// Only a game this module started for someone, that is still going under the server's session.
	if rpc.Docker.Session(sid) == nil || !live.has(sid) {
		writeErr(w, 404, "not found")
		return
	}
	out := r.Clone(r.Context())
	for k := range out.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-witty") {
			out.Header.Del(k)
		}
	}
	out.Header.Del("Cookie")
	out.Header.Del("Authorization")
	// The backend's redirects within the desktop name it from the module's root; a browser reaches
	// it under the server's prefix, so they're made relative to where it asked.
	dockerMux().ServeHTTP(&kasmAnswer{ResponseWriter: w, base: "/kasm/" + sid + "/" + ticket + "/", path: r.URL.Path}, out)
}

// kasmAnswer turns a redirect within the session's own address into a relative one, and drops any
// other.
type kasmAnswer struct {
	http.ResponseWriter
	base, path string
	wrote      bool
}

func (k *kasmAnswer) WriteHeader(code int) {
	if !k.wrote {
		k.wrote = true
		if loc := k.Header().Get("Location"); loc != "" {
			if rest, ok := strings.CutPrefix(loc, k.base); ok {
				depth := strings.Count(strings.TrimPrefix(k.path, k.base), "/")
				rel := strings.Repeat("../", depth) + rest
				if rel == "" {
					rel = "./"
				}
				k.Header().Set("Location", rel)
			} else {
				k.Header().Del("Location")
			}
		}
	}
	k.ResponseWriter.WriteHeader(code)
}

func (k *kasmAnswer) Write(b []byte) (int, error) {
	if !k.wrote {
		k.WriteHeader(http.StatusOK)
	}
	return k.ResponseWriter.Write(b)
}

func (k *kasmAnswer) Flush() {
	if f, ok := k.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets the desktop's WebSocket through.
func (k *kasmAnswer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := k.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("this connection can't be taken over")
}

func (k *kasmAnswer) Unwrap() http.ResponseWriter { return k.ResponseWriter }
