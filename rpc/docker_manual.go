// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/wittywilla/wittyplay/emulators"
)

// Emulators an administrator installs themselves (emulators/manual.go): the Switch's and the
// Switch 2's. WittyPlay never downloads them. The page shows what it finds where people install
// them, or in the module's drop folder (the way in for a server in a container), and adds the
// one the administrator names after checking it really is that emulator.

// dropFolder is where an administrator puts an emulator for WittyPlay to find: a Linux AppImage
// or unpacked folder for a server in a container, or any build for this machine.
func (b *DockerBackend) dropFolder(id string) string {
	if b.dataDir == "" {
		return ""
	}
	return filepath.Join(b.dataDir, "manual", id)
}

// ManualView is what the page shows about adding one.
type ManualView struct {
	Download   string                `json:"download"`
	Notes      string                `json:"notes,omitempty"`
	Programs   []string              `json:"programs"`
	DropFolder string                `json:"dropFolder,omitempty"`
	File       string                `json:"file,omitempty"` // manual.json, for setting it up without the page
	System     string                `json:"system"`         // the system it will run on: linux in a container
	Detected   *emulators.Identified `json:"detected,omitempty"`
	Installed  *emulators.Installed  `json:"installed,omitempty"`
}

func (b *DockerBackend) manualView(e *emulators.Emulator) ManualView {
	goos, _ := b.platform()
	v := ManualView{Download: e.Manual.Download, Notes: e.Manual.Notes, Programs: e.Manual.Programs[goos], DropFolder: b.dropFolder(e.ID), System: goos}
	if b.inst != nil {
		v.File = b.inst.ManualFile()
		v.Installed = b.inst.Get(e.ID)
	}
	if id, ok := emulators.Detect(e, v.DropFolder, goos); ok {
		v.Detected = &id
	}
	return v
}

func (b *DockerBackend) serveManual(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	e := emulators.Find(r.PathValue("id"))
	if e == nil || e.Manual == nil {
		writeJSONError(w, http.StatusNotFound, "no-emulator", "No emulator by that name is added by hand.")
		return
	}
	writeJSON(w, http.StatusOK, b.manualView(e))
}

// serveAddManual adds it: the program named, or with none named, the one found.
func (b *DockerBackend) serveAddManual(w http.ResponseWriter, r *http.Request, _ int64, _ bool) {
	e := emulators.Find(r.PathValue("id"))
	if e == nil || e.Manual == nil {
		writeJSONError(w, http.StatusNotFound, "no-emulator", "No emulator by that name is added by hand.")
		return
	}
	if b.inst == nil {
		writeJSONError(w, http.StatusConflict, "no-data", "This module has no folder of its own to keep it in.")
		return
	}
	var body struct {
		Program string `json:"program"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad-request", "That isn't a program's path.")
		return
	}
	goos, _ := b.platform()
	var id emulators.Identified
	if body.Program == "" {
		found, ok := emulators.Detect(e, b.dropFolder(e.ID), goos)
		if !ok {
			writeJSONError(w, http.StatusNotFound, "not-found", e.Name+" wasn't found where it's usually installed, or in "+b.dropFolder(e.ID)+". Give its full path.")
			return
		}
		id = found
	} else {
		found, err := emulators.IdentifyFor(e, body.Program, goos)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "not-it", err.Error())
			return
		}
		id = found
	}
	if _, err := b.inst.RegisterManual(e, id); err != nil {
		writeJSONError(w, http.StatusConflict, "register", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, b.manualView(e))
}
