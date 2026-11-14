// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// A person's saves of a game: its save states, their pictures, and its battery save, all in the
// person's folder for it (saveDir). Moved from the server's api_games.go (the list, a picture,
// deleting one) and api_game_options.go (a state or a battery save out and back in).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/{id}/saves", w(handleGameSaves))
		mux.Handle("GET /games/{id}/saves/{slot}/thumb", w(handleGameSaveThumb))
		mux.Handle("DELETE /games/{id}/saves/{slot}", w(handleGameSaveDelete))
		mux.Handle("GET /games/{id}/saves/{slot}/file", w(handleSaveExport))
		mux.Handle("PUT /games/{id}/saves/{slot}/file", w(handleSaveImport))
		mux.Handle("GET /games/{id}/battery", w(handleBatteryExport))
		mux.Handle("PUT /games/{id}/battery", w(handleBatteryImport))
	})
}

type saveSlot struct {
	Slot      int    `json:"slot"`
	UpdatedAt int64  `json:"updatedAt"`
	Size      int64  `json:"size"`
	HasThumb  bool   `json:"hasThumb"`
	Core      string `json:"core,omitempty"` // the core that made it, when known
}

var (
	slotNamesOnce sync.Once
	slotNames     map[int]string // slot → file name, as the stream names them
)

// slotFileNames is every save slot's file name. They never change, so they're asked once.
func slotFileNames() map[int]string {
	slotNamesOnce.Do(func() {
		names := map[int]string{}
		for slot := 0; slot <= stream.SlotPrevAuto; slot++ {
			if name, err := stream.SlotName(slot); err == nil {
				names[slot] = name
			}
		}
		slotNames = names
	})
	return slotNames
}

// listSaves is a game's saves in a profile's folder: one look at the folder, rather than asking
// after every slot there could be.
func listSaves(dir string) []saveSlot {
	found := map[string]os.FileInfo{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !e.IsDir() {
				found[e.Name()] = info
			}
		}
	}
	out := []saveSlot{}
	names := slotFileNames()
	for slot := 0; slot <= stream.SlotPrevAuto; slot++ {
		name, ok := names[slot]
		if !ok {
			continue
		}
		st, ok := found[name+".state"]
		if !ok {
			continue
		}
		_, thumb := found[name+".png"]
		out = append(out, saveSlot{Slot: slot, UpdatedAt: st.ModTime().Unix(), Size: st.Size(), HasThumb: thumb, Core: slotCore(dir, name)})
	}
	return out
}

// slotCore is the core that made a save, from the note beside it.
func slotCore(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil || len(b) > 4096 {
		return ""
	}
	var m stream.SlotMeta
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m.Core
}

// slotOf reads a route's {slot} as a slot's file name; false for anything that isn't one.
func slotOf(r *http.Request) (int, string, bool) {
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil {
		return 0, "", false
	}
	name, err := stream.SlotName(slot)
	if err != nil {
		return 0, "", false
	}
	return slot, name, true
}

func handleGameSaves(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, listSaves(a.saveDir(who(r).User, it.ID)))
}

func handleGameSaveThumb(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	_, name, ok := slotOf(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	p := filepath.Join(a.saveDir(who(r).User, it.ID), name+".png")
	if _, err := os.Stat(p); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeFile(w, r, p)
}

func handleGameSaveDelete(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	_, name, ok := slotOf(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	dir := a.saveDir(who(r).User, it.ID)
	_ = os.Remove(filepath.Join(dir, name+".png"))
	_ = os.Remove(filepath.Join(dir, name+".json"))
	if err := os.Remove(filepath.Join(dir, name+".state")); err != nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- a state out and back in

// A state file on its way out carries a header saying where it came from, so an import can check
// it arrived whole. Plain state files from elsewhere are accepted too: a state is only ever read
// by a core in its sandbox.
var stateFileMagic = []byte("WPSTATE1")

// Limits on imports, as the server had them. The 10 GB each account may keep across its games
// is maxAccountGameBytes (patches.go), checked by accountGameRoom.
const (
	maxImportState = 256 << 20
	maxImportSRM   = 64 << 20
	maxStateHeader = 4 << 10
)

type stateHeader struct {
	Console string `json:"console"`
	Core    string `json:"core,omitempty"`
	Title   string `json:"title,omitempty"`
	At      int64  `json:"at"`
	Size    int64  `json:"size"`
	CRC32   uint32 `json:"crc32"`
}

// parseStateFile reads an exported state, checking everything it says. A file without the header
// is taken as a plain state, which no header could vouch for anyway.
func parseStateFile(b []byte) (body []byte, hdr *stateHeader, err error) {
	if !bytes.HasPrefix(b, stateFileMagic) {
		return b, nil, nil
	}
	rest := b[len(stateFileMagic):]
	if len(rest) < 4 {
		return nil, nil, errors.New("this state file is cut short")
	}
	n := binary.LittleEndian.Uint32(rest)
	if n > maxStateHeader || int(n) > len(rest)-4 {
		return nil, nil, errors.New("this state file's header is damaged")
	}
	var h stateHeader
	if err := json.Unmarshal(rest[4:4+n], &h); err != nil {
		return nil, nil, errors.New("this state file's header is damaged")
	}
	body = rest[4+n:]
	if int64(len(body)) != h.Size {
		return nil, nil, errors.New("this state file is cut short")
	}
	if crc32.ChecksumIEEE(body) != h.CRC32 {
		return nil, nil, errors.New("this state file is damaged: its checksum doesn't match")
	}
	return body, &h, nil
}

var downloadNameRe = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// downloadName is a file name for a download: the game's title, made safe, and what it is.
func downloadName(title, part string) string {
	t := strings.TrimSpace(downloadNameRe.ReplaceAllString(title, ""))
	if len(t) > 80 {
		t = t[:80]
	}
	if t == "" {
		t = "game"
	}
	return t + " " + part
}

// saveRoom says whether a person has space for a file in a game's folder.
func saveRoom(dir string, size int64) bool {
	var n int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !info.IsDir() {
			n += info.Size()
		}
	}
	return n+size <= stream.MaxSaveBytes
}

// onlyLooks is a guest: it can look, and bring nothing (the server's canReact). A Kids profile may
// bring its own saves.
func onlyLooks(r *http.Request) bool {
	id := who(r)
	return !id.Admin && id.Kind == "guest"
}

func handleSaveExport(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	_, name, ok := slotOf(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	dir := a.saveDir(who(r).User, it.ID)
	path := filepath.Join(dir, name+".state")
	st, err := os.Stat(path)
	if err != nil || st.Size() > ipc.MaxBlob {
		writeErr(w, 404, "not found")
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		writeErr(w, 404, "not found")
		return
	}
	h, _ := json.Marshal(stateHeader{Console: itemConsole(it), Core: slotCore(dir, name), Title: it.Title, At: st.ModTime().Unix(), Size: int64(len(body)), CRC32: crc32.ChecksumIEEE(body)})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.wpstate"`, downloadName(it.Title, name)))
	var head bytes.Buffer
	head.Write(stateFileMagic)
	_ = binary.Write(&head, binary.LittleEndian, uint32(len(h)))
	head.Write(h)
	_, _ = w.Write(head.Bytes())
	_, _ = w.Write(body)
}

func handleSaveImport(a *App, w http.ResponseWriter, r *http.Request) {
	if onlyLooks(r) {
		writeErr(w, 403, "a guest can only look")
		return
	}
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	slot, err := strconv.Atoi(r.PathValue("slot"))
	if err != nil || slot < 1 || slot > stream.MaxSlot {
		writeErr(w, 400, fmt.Sprintf("a state can be imported into slots 1 to %d", stream.MaxSlot))
		return
	}
	name, _ := stream.SlotName(slot)
	if !takeUploadSlot(w) {
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportState))
	if err != nil {
		writeErr(w, 413, fmt.Sprintf("this file is too large: %d MB at most", maxImportState>>20))
		return
	}
	body, hdr, err := parseStateFile(b)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	if len(body) == 0 {
		writeErr(w, 422, "this file is empty")
		return
	}
	warning := ""
	if hdr == nil {
		warning = "This file has no header to check. If it came from a different core or game, it won’t load."
	} else if hdr.Console != itemConsole(it) {
		writeErr(w, 422, "this state is from another console's game")
		return
	} else if hdr.Title != it.Title {
		t := hdr.Title
		if len(t) > 80 {
			t = t[:80]
		}
		warning = "This state was saved from " + t + ". If that’s a different game, it won’t load."
	}
	id := who(r).User
	dir := a.saveDir(id, it.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !saveRoom(dir, int64(len(body))) {
		writeErr(w, 409, stream.ErrSaveFull.Error())
		return
	}
	if !a.accountGameRoom(w, id, int64(len(body))) {
		return
	}
	if err := writeFileAtomic(filepath.Join(dir, name+".state"), body); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	_ = os.Remove(filepath.Join(dir, name+".png")) // the old picture isn't of this
	core := ""
	if hdr != nil {
		core = hdr.Core
	}
	meta, _ := json.Marshal(stream.SlotMeta{Core: core})
	_ = writeFileAtomic(filepath.Join(dir, name+".json"), meta)
	writeJSON(w, 200, map[string]any{"ok": true, "warning": warning})
}

// ---------------------------------------------------------------- the battery save

func handleBatteryExport(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	path := filepath.Join(a.saveDir(who(r).User, it.ID), "save.srm")
	if _, err := os.Stat(path); err != nil {
		writeErr(w, 404, "this game has no battery save yet")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.srm"`, downloadName(it.Title, "save")))
	http.ServeFile(w, r, path)
}

// handleBatteryImport replaces a game's battery save. It's refused while that person is playing
// it, because the running game would write over it anyway. The old one is kept beside it.
func handleBatteryImport(a *App, w http.ResponseWriter, r *http.Request) {
	if onlyLooks(r) {
		writeErr(w, 403, "a guest can only look")
		return
	}
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	id := who(r).User
	if sessionFor(id, it.ID) != nil {
		writeErr(w, 409, "leave the game first: it would write over this")
		return
	}
	if !takeUploadSlot(w) {
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxImportSRM))
	if err != nil {
		writeErr(w, 413, fmt.Sprintf("this file is too large: %d MB at most", maxImportSRM>>20))
		return
	}
	if len(b) == 0 {
		writeErr(w, 422, "this file is empty")
		return
	}
	dir := a.saveDir(id, it.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !saveRoom(dir, int64(len(b))) {
		writeErr(w, 409, stream.ErrSaveFull.Error())
		return
	}
	if !a.accountGameRoom(w, id, int64(len(b))) {
		return
	}
	path := filepath.Join(dir, "save.srm")
	if old, err := os.ReadFile(path); err == nil {
		_ = writeFileAtomic(filepath.Join(dir, "save.srm.bak"), old)
	}
	if err := writeFileAtomic(path, b); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
