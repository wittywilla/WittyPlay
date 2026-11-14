// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/rpc"
)

// A person's cheats for a game: kept with what they keep about it (wittyplay_game_meta), given to
// the core when the game starts, and to the running game at once when they change. Moved from
// the server (api_game_options.go).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/{id}/cheats", w(handleGameCheats))
		mux.Handle("PUT /games/{id}/cheats", w(handleGameCheatsPut))
		mux.Handle("POST /games/{id}/cheats/import", w(handleGameCheatsImport))
	})
}

func handleGameCheats(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	m, err := a.gameMeta(r.Context(), who(r).User, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, m.Cheats)
}

func handleGameCheatsPut(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Cheats []wittyplay.Cheat `json:"cheats"`
	}
	if err := readJSON(capBody(w, r, wittyplay.MaxCheatFile), &b); err != nil {
		writeErr(w, 400, "this list can't be read")
		return
	}
	clean, err := wittyplay.CleanCheats(b.Cheats)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	id := who(r).User
	if err := a.setGameMetaColumn(r.Context(), id, it.ID, "cheats", string(raw)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	// The game they're playing takes them at once.
	if sess := rpc.Mgr.ForUser(id); sess != nil && sess.ItemID() == it.ID {
		_ = sess.SetCheats(wittyplay.CheatsForCore(clean))
	}
	writeJSON(w, 200, clean)
}

// handleGameCheatsImport reads a person's own cheat file and says what's in it. Nothing is kept
// until they save the list.
func handleGameCheatsImport(a *App, w http.ResponseWriter, r *http.Request) {
	if _, ok := a.personalGame(r); !ok {
		writeErr(w, 404, "not found")
		return
	}
	if !takeUploadSlot(w) {
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wittyplay.MaxCheatFile))
	if err != nil {
		writeErr(w, 413, "this file is too large to be a cheat list")
		return
	}
	list, skipped, err := wittyplay.ParseCheatFile(b)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"cheats": list, "skipped": skipped})
}

// takeUploadSlot takes one of the places for a file being read in (importSlots, shared with the
// controller screen's pictures), or answers the request itself when they're all taken.
func takeUploadSlot(w http.ResponseWriter) bool {
	select {
	case importSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "10")
		writeErr(w, 503, "other files are being imported right now. Try again in a moment")
		return false
	}
}
