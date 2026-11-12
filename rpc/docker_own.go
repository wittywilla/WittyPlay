// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/emulators"
)

// An administrator's own emulator programs (emulators/own.go): listed and replaced whole.

func (b *DockerBackend) ownPath() string { return filepath.Join(b.dataDir, "docker", "own.json") }

// installed is what's installed for an emulator: an own program is installed where it is.
func (b *DockerBackend) installed(e *emulators.Emulator) *emulators.Installed {
	if e.Own {
		for _, o := range emulators.OwnList() {
			if o.ID == e.ID {
				in := o.Installed()
				return &in
			}
		}
		return nil
	}
	if b.inst == nil {
		return nil
	}
	return b.inst.Get(e.ID)
}

func (b *DockerBackend) serveOwn(w http.ResponseWriter, _ *http.Request, _ int64, _ bool) {
	writeJSON(w, http.StatusOK, map[string]any{"own": emulators.OwnList(), "max": emulators.MaxOwn})
}

func (b *DockerBackend) serveSetOwn(w http.ResponseWriter, r *http.Request, user int64, _ bool) {
	if b.dataDir == "" {
		writeJSONError(w, http.StatusConflict, "no-data", "This module has no folder of its own to keep them in.")
		return
	}
	var list []emulators.Own
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&list); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-request", "That isn't a list of programs.")
		return
	}
	if len(list) > emulators.MaxOwn {
		writeJSONError(w, http.StatusBadRequest, "too-many", "That's more programs than a server keeps.")
		return
	}
	ids := map[string]bool{}
	for i := range list {
		o, err := emulators.CheckOwn(list[i], func(c string) bool { return wittyplay.ByID(c) != nil })
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad-program", list[i].Name+": "+err.Error())
			return
		}
		if ids[o.ID] {
			writeJSONError(w, http.StatusBadRequest, "duplicate", "Two programs have the same name.")
			return
		}
		ids[o.ID] = true
		list[i] = o
	}
	// One being played can't be taken away under its player.
	b.mu.Lock()
	for _, s := range b.sessions {
		if emulators.Find(s.Emulator) != nil && emulators.Find(s.Emulator).Own && !slices.ContainsFunc(list, func(o emulators.Own) bool { return o.ID == s.Emulator }) {
			b.mu.Unlock()
			writeJSONError(w, http.StatusConflict, "in-use", "Someone is playing with "+s.Name+". Stop that game first.")
			return
		}
	}
	b.mu.Unlock()
	if err := emulators.SaveOwn(b.ownPath(), list); err != nil {
		b.log.WarnContext(r.Context(), "your own emulator programs couldn't be saved", "err", err)
		writeJSONError(w, http.StatusInternalServerError, "save", err.Error())
		return
	}
	emulators.SetOwn(list)
	names := make([]string, len(list))
	for i, o := range list {
		names[i] = o.ID
	}
	b.log.InfoContext(r.Context(), "own emulator programs saved", "count", len(list), "ids", strings.Join(names, ","), "by", user, "audit", "yes")
	writeJSON(w, http.StatusOK, map[string]any{"own": list, "max": emulators.MaxOwn})
}
