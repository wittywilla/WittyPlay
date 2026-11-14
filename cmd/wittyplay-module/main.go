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

// Command wittyplay-module is WittyPlay's service: the only way a server reaches it. It holds the
// games being played and their streams, and starts a game runner for each. It runs while it's in
// use, and listens on this machine only. See package service in WittyModule.
package main

import (
	"net/http"

	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/api"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/docker/engine"
)

func main() {
	// The optional Docker backend. Connecting doesn't reach the daemon; nothing does until an
	// administrator turns the backend on.
	rpc.ConnectDocker = func() (docker.Runtime, error) { return engine.Connect() }
	service.Main(service.Config{
		Manifest:  wittyplay.ManifestJSON,
		Handlers:  rpc.Handlers,
		ErrorCode: rpc.ErrorCode,
		Busy:      rpc.Busy,
		OnStop:    rpc.Stop,
		UI:        rpc.UI,
		// Module API 5: WittyPlay's own routes and data (package api).
		HTTP:      func(mux *http.ServeMux) { api.Routes(mux); api.Watch(); rpc.Start() },
		Lifecycle: api.Lifecycle,
	})
}
