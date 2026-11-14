// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/rpc"
)

// Playing a game in a standalone emulator (PCSX2, PPSSPP and the like), which the Docker backend
// (rpc.Docker) runs in a container or a sandbox of its own and streams. Moved from the server's
// api_games_emulators.go: the server had to hand the module the game's file; now the module gets
// it from the item the route names, which the server has checked this profile may see. It's a
// host session like any game (sessions.go), so the server's emulator_sessions.go, which watched
// screen time and sign-outs for these alone, is no longer needed.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("POST /games/{id}/play/emulator", w(handleGamePlayEmulator))
	})
}

// emulatorID says a name could be an emulator's: what the backend's own list allows.
func emulatorID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || i > 0 && (c == '-' || c == '_')) {
			return false
		}
	}
	return true
}

// launchEmulator starts a game in an emulator: rpc.Docker.Launch, or a test's stand-in.
var launchEmulator = func(ctx context.Context, o rpc.LaunchOptions) (*rpc.DockerSession, error) {
	return rpc.Docker.Launch(ctx, o)
}

// emulatorDone is closed when an emulator's game has gone.
var emulatorDone = func(ds *rpc.DockerSession) <-chan struct{} { return ds.Done() }

// stopEmulator ends one.
var stopEmulator = func(ctx context.Context, id string) error { return rpc.Docker.Stop(ctx, id) }

// emulatorSession is what a client gets back: the backend's session, with its paths under the
// server's.
type emulatorSession struct {
	ID       string   `json:"id"`
	Backend  string   `json:"backend"`
	Emulator string   `json:"emulator"`
	Name     string   `json:"name"`
	Connect  string   `json:"connect,omitempty"`
	KasmURL  string   `json:"kasmUrl,omitempty"`
	Stop     string   `json:"stop"`
	Pause    string   `json:"pause"`
	Resume   string   `json:"resume"`
	Width    int      `json:"width"`
	Height   int      `json:"height"`
	FPS      int      `json:"fps"`
	Players  int      `json:"players"`
	Notices  []string `json:"notices"`
}

// handleGamePlayEmulator starts a game in a standalone emulator. The profile's limits were
// checked by the server for the item; the file is the item's own; and the backend is told only
// that file, this account and the emulator's name.
func handleGamePlayEmulator(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok || itemConsole(it) == rigConsole {
		writeErr(w, 404, "not found")
		return
	}
	c := console(itemConsole(it))
	if c == nil {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Emulator string `json:"emulator"`
		Device   string `json:"device"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !emulatorID(b.Emulator) {
		writeErr(w, 400, "emulator: which one?")
		return
	}
	ctx := r.Context()
	user := who(r).User
	systemDir := a.SystemDir()
	if err := os.MkdirAll(systemDir, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if st, err := os.Stat(gamePath(it)); err != nil || st.IsDir() {
		writeErr(w, 404, "the game file is missing")
		return
	}
	if err := a.Configure(ctx); err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	// Firmware left in a folder beside the games is brought to the BIOS folder first, as for a core.
	a.FindBIOSNear(ctx, gamePath(it))
	rom, cleanup, err := a.prepareGame(ctx, it, c)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		writeErr(w, 422, err.Error())
		return
	}
	done := func() {
		if cleanup != nil {
			cleanup()
		}
	}
	hid, err := a.beginSession(r, it.ID, it.Title)
	if err != nil {
		done()
		if errors.Is(err, errSessionRefused) {
			writeErr(w, 403, err.Error())
			return
		}
		writeErr(w, 503, "This game could not start: the server didn't answer. Try again.")
		return
	}
	// Starting can mean pulling an image: minutes, the first time.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	ds, err := launchEmulator(cctx, rpc.LaunchOptions{UserID: user, ItemID: it.ID, Title: it.Title, Console: c.ID, ROM: rom, SystemDir: systemDir, Emulator: b.Emulator})
	var no *rpc.Refusal
	switch {
	case errors.As(err, &no):
		done()
		a.abandonSession(hid)
		// The refusal's words name what is missing (a BIOS's or firmware's file name), never what is in a file.
		a.Log.WarnContext(ctx, "wittyplay: an emulator refused to start a game", "cat", "emulators", "item", it.ID, "console", c.ID,
			"emulator", b.Emulator, "code", no.Code, "reason", no.Text, "busy", no.Busy)
		status := 409
		if no.Busy {
			status = 503
		}
		writeJSON(w, status, map[string]any{"error": no.Text, "code": no.Code})
		return
	case err != nil:
		done()
		a.abandonSession(hid)
		a.Log.WarnContext(ctx, "wittyplay: could not start a game in an emulator", "cat", "emulators", "item", it.ID, "emulator", b.Emulator, "err", err)
		writeErr(w, 500, "This game could not start. "+err.Error())
		return
	case ds == nil || ds.ID == "":
		done()
		a.abandonSession(hid)
		writeErr(w, 500, "This game could not start: the emulator answered without a game.")
		return
	}
	ended := make(chan struct{})
	sid := ds.ID
	go func() {
		<-emulatorDone(ds)
		done()
		close(ended)
	}()
	a.followSession(hid, sid, user, it.ID, ended, func() {
		sctx, scancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scancel()
		if err := stopEmulator(sctx, sid); err != nil {
			a.Log.Warn("wittyplay: could not stop a game in an emulator", "cat", "emulators", "session", sid, "err", err)
		}
	})
	if err := a.noteGamePlayed(ctx, user, it.ID); err != nil {
		a.Log.WarnContext(ctx, "wittyplay: recording a play", "cat", "emulators", "session", sid, "err", err)
	}
	a.Log.InfoContext(ctx, "wittyplay: a game started in an emulator", "cat", "emulators", "session", sid, "item", it.ID, "console", c.ID, "game", it.Title,
		"emulator", ds.Emulator, "backend", string(ds.Backend), "device", deviceName(b.Device, r))
	writeJSON(w, 201, emulatorAnswer(ds))
}

// emulatorAnswer is the backend's session as a client reaches it: its paths are the module's
// own, which a client reaches under the server's.
func emulatorAnswer(ds *rpc.DockerSession) emulatorSession {
	var s emulatorSession
	if b, err := ds.MarshalJSON(); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	under := func(p string) string {
		if p == "" || !strings.HasPrefix(p, "/") {
			return ""
		}
		return "/api/v1/modules/wittyplay" + p
	}
	s.Connect, s.KasmURL, s.Stop, s.Pause, s.Resume = under(s.Connect), under(s.KasmURL), under(s.Stop), under(s.Pause), under(s.Resume)
	if s.Notices == nil {
		s.Notices = []string{}
	}
	return s
}
