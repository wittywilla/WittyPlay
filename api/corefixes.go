// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"net/http"

	"github.com/wittywilla/wittyplay"
)

// The runner's core fixes (wittyplay.CoreFixes): on unless an administrator sets gamingCoreFixes
// to "0" in Settings, Games, Advanced.
//
// GET /gaming/core-fixes → {on, fixes: [{id, title, about, cores}]} (administrators)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /gaming/core-fixes", w(adminRoute(handleCoreFixes)))
	})
}

func (a *App) coreFixesOn(ctx context.Context) bool {
	return a.configText(ctx, "gamingCoreFixes", "1") != "0"
}

func handleCoreFixes(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"on": a.coreFixesOn(r.Context()), "fixes": wittyplay.CoreFixes})
}
