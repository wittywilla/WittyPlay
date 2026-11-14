// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyPlay - Console games streamed to a browser or an app, with every controller and a touch layout editor.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Package api is WittyPlay's own HTTP API (module API 5): the routes WittyMediaServer passes
// to it once it has checked who may call them, at /api/modules/wittyplay/... and at the
// server's own paths it takes over (integration.json, alias). It keeps its data in WittyPlay's
// own database (package store), and asks the server only what only the server knows, through the
// host API.
//
// Moved from the server route group by route group (WittyMediaServer docs/plans/MODULAR-MODULES.md):
// controller layouts, controller profiles and the controller screen's background first.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/store"
)

// DataEnv is where the server says WittyPlay's own folder is.
const DataEnv = "WITTYMODULE_DATA"

// App is what the routes need: WittyPlay's database and folder, and the way back to the server.
type App struct {
	DB   *store.DB
	Dir  string
	Host *host.Client
	Log  *slog.Logger
}

var (
	appMu  sync.Mutex
	shared *App
)

// Get opens WittyPlay's database and reaches the server the first time something needs them.
func Get() (*App, error) {
	appMu.Lock()
	defer appMu.Unlock()
	if shared != nil {
		return shared, nil
	}
	dir := os.Getenv(DataEnv)
	db, err := store.Open(dir)
	if err != nil {
		return nil, err
	}
	h, err := host.New()
	if err != nil {
		db.Close()
		return nil, err
	}
	shared = &App{DB: db, Dir: dir, Host: h, Log: slog.Default()}
	return shared, nil
}

// Routes adds the routes to a service's mux (service.Config.HTTP).
func Routes(mux *http.ServeMux) {
	with := func(f func(*App, http.ResponseWriter, *http.Request)) http.Handler {
		return service.People(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a, err := Get()
			if err != nil {
				slog.Error("wittyplay: its database or the server can't be reached", "cat", "games", "err", err)
				writeErr(w, 503, "WittyPlay can't reach its data right now")
				return
			}
			f(a, w, r)
		}))
	}
	controlRoutes(mux, with)
	profileRoutes(mux, with)
	backgroundRoutes(mux, with)
	for _, g := range routeGroups {
		g(mux, with)
	}
}

// with gives a route WittyPlay's App, once its database and the server can be reached.
type with = func(func(*App, http.ResponseWriter, *http.Request)) http.Handler

// routeGroups are the route groups that add themselves (addRoutes, in an init in their own file).
var routeGroups []func(*http.ServeMux, with)

func addRoutes(g func(*http.ServeMux, with)) { routeGroups = append(routeGroups, g) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	b, err := json.Marshal(v)
	if err != nil {
		code, b = 500, []byte(`{"error":"the answer could not be written"}`)
	}
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

// writeErr answers as the server's own routes do: {"error": "..."}.
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func pathInt(r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return n, err == nil && n > 0
}

func who(r *http.Request) service.Identity { return service.Who(r) }

// console is a console this server plays, or nil: a hidden one (the test core's) only when the
// server says it is testing games.
func console(id string) *wittyplay.Console {
	c := wittyplay.ByID(id)
	if c == nil || (c.Hidden && os.Getenv("WMS_GAMING_TESTCORE") != "1") {
		return nil
	}
	return c
}

func consoleKnown(id string) bool { return console(id) != nil }

// visibleGame is a game the person asking may see, from the server.
func (a *App) visibleGame(ctx context.Context, r *http.Request, id int64) (*service.Item, error) {
	it, err := a.Host.Item(ctx, host.For(r), id)
	if err != nil {
		return nil, err
	}
	if it.Kind != "game" {
		return nil, errors.New("not a game")
	}
	return &it, nil
}
