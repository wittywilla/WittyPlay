// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/patch"
)

// A person's own patches for a game (IPS, BPS): listed in wittyplay_patches, the files in their
// folder for that game (personGameDir/patches, under numbers), applied to a copy of the game when
// it starts. Moved from the server (api_game_options.go, library/game_options.go).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/{id}/patches", w(handleGamePatches))
		mux.Handle("POST /games/{id}/patches", w(handleGamePatchAdd))
		mux.Handle("PUT /games/{id}/patches/{patch}", w(handleGamePatchPut))
		mux.Handle("DELETE /games/{id}/patches/{patch}", w(handleGamePatchDelete))
	})
	onForget(func(a *App, user int64) {
		// The rows went with perUser; the files with them.
		dirs, _ := filepath.Glob(filepath.Join(a.Dir, "saves", "u"+strconv.FormatInt(user, 10), "i*", "patches"))
		for _, d := range dirs {
			_ = os.RemoveAll(d)
		}
	})
}

// maxGamePatches is how many patches one game may have for one person.
const maxGamePatches = 16

// maxAccountGameBytes is what one account's imports and patches can bring the files it keeps for
// its games up to, across all of them (the server's limit, unchanged).
var maxAccountGameBytes int64 = 10 << 30

// GamePatch is a patch file of a person's own for one game.
type GamePatch struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // ips | bps
	Size      int64  `json:"size"`
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt"`
}

var errTooManyPatches = errors.New("too many patches")

func (a *App) patchDir(userID, itemID int64) string {
	return filepath.Join(a.personGameDir(userID, itemID), "patches")
}

func patchFile(dir string, id int64) string {
	return filepath.Join(dir, "p"+strconv.FormatInt(id, 10)+".patch")
}

// gamePatches lists a person's patches for a game, in the order they apply.
func (a *App) gamePatches(ctx context.Context, userID, itemID int64) ([]GamePatch, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT id, name, kind, size, enabled, created_at FROM wittyplay_patches WHERE user_id = ? AND item_id = ? ORDER BY id`, userID, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GamePatch{}
	for rows.Next() {
		var p GamePatch
		var on int
		if err := rows.Scan(&p.ID, &p.Name, &p.Kind, &p.Size, &on, &p.CreatedAt); err != nil {
			return nil, err
		}
		p.Enabled = on != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// addGamePatch records one, turned on, and says its number. The count and the adding are one
// statement, so requests arriving together can't all fit in the last place.
func (a *App) addGamePatch(ctx context.Context, userID, itemID int64, p GamePatch) (int64, error) {
	res, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_patches(user_id, item_id, name, kind, size, enabled, created_at)
		SELECT ?,?,?,?,?,1,? WHERE (SELECT COUNT(*) FROM wittyplay_patches WHERE user_id = ? AND item_id = ?) < ?`,
		userID, itemID, p.Name, p.Kind, p.Size, time.Now().Unix(), userID, itemID, maxGamePatches)
	if err != nil {
		return 0, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, errTooManyPatches
	}
	return res.LastInsertId()
}

// changed says whether a statement touched a row of theirs.
func changed(res interface{ RowsAffected() (int64, error) }, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// accountGameRoom says whether this much more fits in what an account may keep for its games, and
// answers the request itself when it doesn't.
func (a *App) accountGameRoom(w http.ResponseWriter, userID, size int64) bool {
	n := size
	_ = filepath.WalkDir(filepath.Join(a.Dir, "saves", "u"+strconv.FormatInt(userID, 10)), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				n += info.Size()
			}
		}
		return nil
	})
	if n <= maxAccountGameBytes {
		return true
	}
	writeErr(w, 409, fmt.Sprintf("your saved games and patches have reached the %d GB this server allows for each person. Delete some first", maxAccountGameBytes>>30))
	return false
}

func handleGamePatches(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	list, err := a.gamePatches(r.Context(), who(r).User, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

func handleGamePatchAdd(a *App, w http.ResponseWriter, r *http.Request) {
	// A patch changes what a core is given to run: that's for grown-up accounts (the server's
	// canPost: an administrator or an adult; never a Kids profile or a guest).
	if id := who(r); !id.Admin && id.Kind != "adult" && id.Kind != "" {
		writeErr(w, 403, "this profile can't add patches")
		return
	}
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	ctx, id := r.Context(), who(r).User
	existing, err := a.gamePatches(ctx, id, it.ID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(existing) >= maxGamePatches {
		writeErr(w, 409, fmt.Sprintf("a game can have %d patches at most; remove one first", maxGamePatches))
		return
	}
	if !takeUploadSlot(w) {
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, patch.MaxPatch))
	if err != nil {
		writeErr(w, 413, fmt.Sprintf("this patch is too large: %d MB at most", patch.MaxPatch>>20))
		return
	}
	kind, err := patch.Detect(b)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	if !a.accountGameRoom(w, id, int64(len(b))) {
		return
	}
	name := cleanUploadName(r.URL.Query().Get("name"), "patch."+kind)
	dir := a.patchDir(id, it.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	pid, err := a.addGamePatch(ctx, id, it.ID, GamePatch{Name: name, Kind: kind, Size: int64(len(b))})
	if errors.Is(err, errTooManyPatches) {
		writeErr(w, 409, fmt.Sprintf("a game can have %d patches at most; remove one first", maxGamePatches))
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := writeFileAtomic(patchFile(dir, pid), b); err != nil {
		_, _ = a.DB.ExecContext(ctx, `DELETE FROM wittyplay_patches WHERE id = ?`, pid)
		writeErr(w, 500, err.Error())
		return
	}
	list, _ := a.gamePatches(ctx, id, it.ID)
	writeJSON(w, 201, list)
}

func handleGamePatchPut(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	pid, ok := pathInt(r, "patch")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(capBody(w, r, 4<<10), &b); err != nil {
		writeErr(w, 400, "this can't be read")
		return
	}
	found, err := changed(a.DB.ExecContext(r.Context(), `UPDATE wittyplay_patches SET enabled = ? WHERE id = ? AND user_id = ? AND item_id = ?`, boolInt(b.Enabled), pid, who(r).User, it.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !found {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func handleGamePatchDelete(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	pid, ok := pathInt(r, "patch")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	id := who(r).User
	found, err := changed(a.DB.ExecContext(r.Context(), `DELETE FROM wittyplay_patches WHERE id = ? AND user_id = ? AND item_id = ?`, pid, id, it.ID))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !found {
		writeErr(w, 404, "not found")
		return
	}
	_ = os.Remove(patchFile(a.patchDir(id, it.ID), pid))
	writeJSON(w, 200, map[string]any{"ok": true})
}

var patchSlot = make(chan struct{}, 1)

// patchROM makes a patched copy of a game for this session when its player has patches turned
// on (for the group that starts games). The original is never touched. A patch that won't apply
// leaves the game unpatched and playable, with a notice saying so.
func (a *App) patchROM(ctx context.Context, userID, itemID int64, rom string, cleanup func()) (string, func(), []string) {
	list, err := a.gamePatches(ctx, userID, itemID)
	if err != nil {
		return rom, cleanup, nil
	}
	var on []GamePatch
	for _, p := range list {
		if p.Enabled {
			on = append(on, p)
		}
	}
	if len(on) == 0 {
		return rom, cleanup, nil
	}
	ext := strings.ToLower(filepath.Ext(rom))
	switch ext {
	case ".cue", ".m3u", ".gdi", ".chd", ".pbp", ".iso":
		return rom, cleanup, []string{"Patches only work on games that are a single file, so this one started unpatched."}
	}
	// A game and its patched copy are both in memory here, so only one at a time.
	select {
	case patchSlot <- struct{}{}:
		defer func() { <-patchSlot }()
	case <-ctx.Done():
		return rom, cleanup, nil
	}
	st, err := os.Stat(rom)
	if err != nil || st.Size() > patch.MaxOutput {
		return rom, cleanup, []string{"This game is too large to patch, so it started unpatched."}
	}
	data, err := os.ReadFile(rom)
	if err != nil {
		return rom, cleanup, []string{"This game couldn't be read to patch it, so it started unpatched."}
	}
	dir := a.patchDir(userID, itemID)
	var notices []string
	applied := 0
	for _, p := range on {
		b, err := os.ReadFile(patchFile(dir, p.ID))
		if err == nil {
			var out []byte
			if out, err = patch.Apply(data, b); err == nil {
				data = out
				applied++
				continue
			}
		}
		notices = append(notices, fmt.Sprintf("The patch %s wasn’t applied: %v.", p.Name, err))
	}
	if applied == 0 {
		return rom, cleanup, notices
	}
	nowhere := "There was nowhere to put the patched game, so it started unpatched."
	tmpRoot := filepath.Join(a.Dir, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o700); err != nil {
		return rom, cleanup, append(notices, nowhere)
	}
	tmp, err := os.MkdirTemp(tmpRoot, "rom-")
	if err != nil {
		return rom, cleanup, append(notices, nowhere)
	}
	out := filepath.Join(tmp, "game"+ext)
	if err := os.WriteFile(out, data, 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return rom, cleanup, append(notices, "The patched game couldn't be written, so it started unpatched.")
	}
	next := func() {
		_ = os.RemoveAll(tmp)
		if cleanup != nil {
			cleanup()
		}
	}
	return out, next, notices
}
