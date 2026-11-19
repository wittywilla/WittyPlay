// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/gamefiles"
	"github.com/wittywilla/wittyplay/wittyplayfile"
)

// A game's own .wittyplay, for the editor (desktop only: TV mode says so) and for sharing. Only an
// administrator may use these: saving writes in the owner's games folder.
//
//	GET  /games/{id}/wittyplay              the file (or a draft gathered from beside the game), its state
//	GET  /games/{id}/wittyplay/picture      ?name=images/boxfront.png: one picture in it
//	PUT  /games/{id}/wittyplay              {doc, images: {role: base64}, add: {role: [base64]}, screenshots: [base64]}: save
//	GET  /games/{id}/wittyplay/download     the file as an archive, to share
//	POST /games/{id}/wittyplay/import       a .wittyplay as the body: checked, and saved when it is for this dump
//	POST /games/{id}/wittyplay/writeout     {overwrite}: write it out to the standard files beside the game
func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/{id}/wittyplay", w(adminRoute(handleSidecarGet)))
		mux.Handle("GET /games/{id}/wittyplay/picture", w(adminRoute(handleSidecarPicture)))
		mux.Handle("PUT /games/{id}/wittyplay", w(adminRoute(handleSidecarPut)))
		mux.Handle("GET /games/{id}/wittyplay/download", w(adminRoute(handleSidecarDownload)))
		mux.Handle("POST /games/{id}/wittyplay/import", w(adminRoute(handleSidecarImport)))
		mux.Handle("POST /games/{id}/wittyplay/writeout", w(adminRoute(handleSidecarWriteOut)))
	})
}

// sidecarGame is the game a route names, with its file.
func (a *App) sidecarGame(w http.ResponseWriter, r *http.Request) (convertGame, bool) {
	it, ok := a.personalGame(r)
	if !ok || it == nil {
		writeErr(w, 404, "game not found")
		return convertGame{}, false
	}
	row, err := a.gameRow(r.Context(), it.ID)
	if err != nil || row.Path == "" {
		writeErr(w, 404, "this game's file isn't known yet: scan its library first")
		return convertGame{}, false
	}
	return convertGame{ID: it.ID, Library: row.LibraryID, Console: row.Console, Title: row.Title, Path: row.Path}, true
}

// sidecarView is what the editor gets.
type sidecarView struct {
	Exists   bool              `json:"exists"`
	Name     string            `json:"name"`            // the file's name beside the game
	Draft    bool              `json:"draft,omitempty"` // not saved yet: gathered from beside the game
	Found    *gamefiles.Found  `json:"found,omitempty"`
	State    *sidecarState     `json:"state,omitempty"`
	Verdict  string            `json:"verdict,omitempty"`
	Doc      json.RawMessage   `json:"doc"`
	Pictures map[string]string `json:"pictures"` // member name -> address to show it
	Console  string            `json:"console"`
	Limits   map[string]any    `json:"limits"`
	// Version: the newest version of the format WittyPlay writes. Newer: the file follows a later
	// one; what WittyPlay doesn't know of it is kept as it is when it's saved.
	Version int  `json:"version"`
	Newer   bool `json:"newer,omitempty"`
}

func handleSidecarGet(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	v := sidecarView{Name: filepath.Base(gamefiles.SidecarPath(g.Path)), Console: g.Console, Pictures: map[string]string{},
		State:   a.sidecarStateOf(ctx, g.ID),
		Version: wittyplayfile.Version,
		Limits: map[string]any{"maxFile": wittyplayfile.MaxFile, "maxImage": wittyplayfile.MaxImage, "artworkRoles": wittyplayfile.ArtworkRoles, "maxPerRole": wittyplayfile.MaxPicturesPerRole,
			"targets": wittyplayfile.Targets, "hotkeyActions": wittyplayfile.HotkeyActions, "devices": wittyplayfile.Devices}}
	var d *wittyplayfile.Doc
	if p := findSidecar(g.Path); p != "" {
		v.Exists, v.Name = true, filepath.Base(p)
		var err error
		if d, err = readSidecar(p); err != nil {
			writeJSON(w, 422, map[string]any{"error": err.Error(), "problems": problemsOf(err), "view": v})
			return
		}
	} else {
		gathered, found, _ := gamefiles.Gather(g.Path)
		d = a.libraryDoc(ctx, g, gathered)
		v.Draft, v.Found = true, &found
	}
	v.Newer = d.Newer()
	if d.Game != nil {
		v.Verdict = string(wittyplayfile.Compare(d.Game.Identity, a.gameFacts(ctx, g.ID, g.Path, d.Game.Identity)))
	}
	man, err := json.Marshal(d)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	v.Doc = man
	for name := range d.Files {
		if v.Draft {
			v.Pictures[name] = "data:" + http.DetectContentType(d.Files[name]) + ";base64," + base64.StdEncoding.EncodeToString(d.Files[name])
		} else {
			v.Pictures[name] = gmBase + "/games/" + r.PathValue("id") + "/wittyplay/picture?name=" + name
		}
	}
	writeJSON(w, 200, v)
}

func problemsOf(err error) []wittyplayfile.Problem {
	var e *wittyplayfile.Error
	if errors.As(err, &e) {
		return e.Problems
	}
	return []wittyplayfile.Problem{{What: err.Error()}}
}

func handleSidecarPicture(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	p := findSidecar(g.Path)
	if p == "" {
		writeErr(w, 404, "no .wittyplay")
		return
	}
	d, err := readSidecar(p)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	b, ok := d.Files[r.URL.Query().Get("name")]
	if !ok || wittyplayfile.ImageType(b) == "" {
		writeErr(w, 404, "no such picture")
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// PUT /games/{id}/wittyplay: the editor's document, with pictures to add. images puts a picture
// as the one shown for a role, add puts more after a role's others. Pictures the file has and the
// document still names are kept, in the order the document lists them (the first shown); the rest
// are dropped. Everything is checked before the
// file is written, and the problems come back together.
func handleSidecarPut(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	var b struct {
		Doc         json.RawMessage     `json:"doc"`
		Images      map[string]string   `json:"images"`
		Add         map[string][]string `json:"add"`
		Screenshots []string            `json:"screenshots"`
	}
	if err := readJSON(capBody(w, r, wittyplayfile.MaxFile*4/3+(1<<20)), &b); err != nil {
		writeErr(w, 400, "this can't be read: is it too large?")
		return
	}
	d, err := wittyplayfile.ParseManifest(b.Doc)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "problems": problemsOf(err)})
		return
	}
	d.Files = map[string][]byte{}
	if p := findSidecar(g.Path); p != "" {
		if old, err := readSidecar(p); err == nil {
			d.Files = old.Files
		}
	} else {
		// A draft's pictures came from beside the game (or WittyPlay's own): gather them again.
		gathered, _, _ := gamefiles.Gather(g.Path)
		d.Files = a.libraryDoc(r.Context(), g, gathered).Files
	}
	for role, data := range b.Images {
		raw, err := base64.StdEncoding.DecodeString(stripDataURL(data))
		if err == nil {
			_, err = d.SetImage(role, raw)
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": role + ": " + err.Error(), "problems": []wittyplayfile.Problem{{Where: "game.artwork." + role, What: err.Error()}}})
			return
		}
	}
	for role, list := range b.Add {
		for _, data := range list {
			raw, err := base64.StdEncoding.DecodeString(stripDataURL(data))
			if err == nil {
				_, err = d.AddImage(role, raw)
			}
			if err != nil {
				writeJSON(w, 400, map[string]any{"error": role + ": " + err.Error(), "problems": []wittyplayfile.Problem{{Where: "game.artwork." + role, What: err.Error()}}})
				return
			}
		}
	}
	for _, data := range b.Screenshots {
		raw, err := base64.StdEncoding.DecodeString(stripDataURL(data))
		if err == nil {
			_, err = d.AddScreenshot(raw)
		}
		if err != nil {
			writeJSON(w, 400, map[string]any{"error": err.Error(), "problems": []wittyplayfile.Problem{{Where: "game.screenshots", What: err.Error()}}})
			return
		}
	}
	a.saveSidecar(w, r, g, d)
}

func stripDataURL(s string) string {
	if strings.HasPrefix(s, "data:") {
		if i := strings.Index(s, ","); i > 0 {
			return s[i+1:]
		}
	}
	return s
}

func (a *App) saveSidecar(w http.ResponseWriter, r *http.Request, g convertGame, d *wittyplayfile.Doc) {
	ctx := r.Context()
	d.Stamp("", "WittyPlay", moduleVersion(), time.Now())
	out, err := d.EncodeAny()
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "problems": problemsOf(err)})
		return
	}
	p := findSidecar(g.Path)
	if p == "" {
		p = gamefiles.SidecarPath(g.Path)
	}
	if err := writeSidecarFile(p, out); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	// The look is the game's at once; details, pictures and the core come with the next scan.
	_ = a.setGamePicture(ctx, g.ID, pictureByFile, d.For(0, g.Console, filepath.Base(g.Path), g.Title))
	st := &sidecarState{Path: p, Verdict: string(wittyplayfile.Unknown)}
	if d.Game != nil {
		st.Verdict = string(wittyplayfile.Compare(d.Game.Identity, a.gameFacts(ctx, g.ID, g.Path, d.Game.Identity)))
	}
	a.setSidecarState(ctx, g.ID, st)
	writeJSON(w, 200, map[string]any{"saved": filepath.Base(p), "size": len(out), "verdict": st.Verdict})
}

// GET /games/{id}/wittyplay/download: the game's .wittyplay as an archive, pictures and all, to
// share. A game without one gets a draft gathered from beside it.
func handleSidecarDownload(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	var d *wittyplayfile.Doc
	if p := findSidecar(g.Path); p != "" {
		var err error
		if d, err = readSidecar(p); err != nil {
			writeErr(w, 422, err.Error())
			return
		}
	} else {
		gathered, _, _ := gamefiles.Gather(g.Path)
		d = a.libraryDoc(r.Context(), g, gathered)
		if d.Game.Identity == nil {
			d.Game.Identity = a.identityFor(r.Context(), g, nil)
		}
		d.Stamp("", "WittyPlay", moduleVersion(), time.Now())
	}
	b, err := d.EncodeArchive()
	if err != nil {
		writeJSON(w, 422, map[string]any{"error": err.Error(), "problems": problemsOf(err)})
		return
	}
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == '/' {
			return '_'
		}
		return r
	}, gameBaseName(filepath.Base(g.Path))) + wittyplayfile.Ext
	w.Header().Set("Content-Type", wittyplayfile.ArchiveMediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// POST /games/{id}/wittyplay/import[?force=1]: a .wittyplay someone shared. It is checked whole,
// and compared with this game's file: one made for another dump answers 409 and is kept only
// with force.
func handleSidecarImport(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, wittyplayfile.MaxFile+1))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	d, err := wittyplayfile.Parse(b)
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": err.Error(), "problems": problemsOf(err)})
		return
	}
	if d.Game != nil {
		v := wittyplayfile.Compare(d.Game.Identity, a.gameFacts(r.Context(), g.ID, g.Path, d.Game.Identity))
		if v == wittyplayfile.Other && r.URL.Query().Get("force") != "1" {
			writeJSON(w, 409, map[string]any{"error": "this .wittyplay was made for another dump of this game (its size or checksum differs)", "verdict": v})
			return
		}
	}
	a.saveSidecar(w, r, g, d)
}

// POST /games/{id}/wittyplay/writeout {overwrite}: the game's .wittyplay out to the standard files.
func handleSidecarWriteOut(a *App, w http.ResponseWriter, r *http.Request) {
	g, ok := a.sidecarGame(w, r)
	if !ok {
		return
	}
	var b struct {
		Overwrite bool `json:"overwrite"`
	}
	_ = readJSON(capBody(w, r, 1<<10), &b)
	p := findSidecar(g.Path)
	if p == "" {
		writeErr(w, 404, "this game has no .wittyplay to write out")
		return
	}
	d, err := readSidecar(p)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	out, err := gamefiles.WriteOut(g.Path, d, g.Console, gamefiles.Options{Overwrite: b.Overwrite})
	if err != nil {
		writeJSON(w, 409, map[string]any{"error": err.Error(), "written": out})
		return
	}
	writeJSON(w, 200, out)
}
