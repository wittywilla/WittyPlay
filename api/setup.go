// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/wittywilla/wittymodule/host"
)

// WittyPlay's first-run step (integration.json setup "games", the SDUI page wittyplay-setup):
// finishing it or putting it off tells the server, on the administrator's behalf, which stops
// showing "Finish setting up WittyPlay" on Home.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("POST /setup/games", w(handleSetupGames))
	})
}

func handleSetupGames(a *App, w http.ResponseWriter, r *http.Request) {
	id := who(r)
	if !id.Admin {
		writeErr(w, 403, "Only an administrator sets up WittyPlay.")
		return
	}
	var in struct {
		Skip bool `json:"skip"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&in); err != nil && err != io.EOF {
		writeErr(w, 400, "A step's end is {skip}.")
		return
	}
	if err := a.Host.Do(r.Context(), host.On{User: id.User}, "POST", "/setup/games", in, nil); err != nil {
		writeErr(w, 502, "The server didn't take it: "+err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "skipped": in.Skip})
}
