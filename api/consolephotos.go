// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/gamelogos"
	"github.com/wittywilla/wittyplay/ui"
)

// Console photos for WittyPlay's Consoles tab (moved from the server's api_console_photos.go): a
// picture of each console's hardware from Wikimedia Commons (by its Wikidata entity), fetched by
// WittyPlay once and kept, never linked from the browser, with its author and licence to show
// beside it. They are off until an administrator opts in, having been told what is fetched, from
// where and under which licences (ui.ConsolePhotoNotice), on WittyPlay's Settings or its first-run
// step: nothing is asked of Wikimedia before that, or while Wikidata may not be asked (WittyPlay's
// sources). Turning them off deletes every photo kept. With none, the app draws the console's own
// mark.
//
// GET /console-photos/{id}        → {imageUrl, credit, license, licenseUrl, pageUrl, source}, or 404
// GET /console-photos/{id}/image  → the picture
// GET /games/console-photos       → {enabled, notice, agreedAt}
// PUT /games/console-photos       {enabled} → the same (administrators)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /console-photos/{id}", w(handleConsolePhoto))
		mux.Handle("GET /console-photos/{id}/image", w(handleConsolePhotoImage))
		mux.Handle("GET /games/console-photos", w(handleConsolePhotos))
		mux.Handle("PUT /games/console-photos", w(adminRoute(handleConsolePhotosPut)))
	})
}

// cfgConsolePhotos is the administrator's opt-in to console photos: the version of the notice they
// agreed to ("" or "0" for none), and when they last changed it (cfgConsolePhotosAt).
const (
	cfgConsolePhotos     = "consolePhotos"
	cfgConsolePhotosAt   = "consolePhotosAt"
	consolePhotosVersion = "1"
)

// consolePhotoSource finds a console's picture; a fake in tests.
type consolePhotoSource interface {
	ConsolePhoto(ctx context.Context, console, name string) (gamelogos.LogoCandidate, error)
	Fetch(ctx context.Context, c gamelogos.LogoCandidate) ([]byte, error)
}

// photoSource is where console photos come from.
var photoSource = func() consolePhotoSource { return logoSource() }

// consolePhotoRetry: how long a console with no free picture is left before Commons is asked again.
const consolePhotoRetry = 7 * 24 * time.Hour

var consoleIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// consolePhotoFacts is what is kept beside a picture.
type consolePhotoFacts struct {
	None       bool
	At         int64
	File       string
	Credit     string
	License    string
	LicenseURL string
	PageURL    string
}

var consolePhotoBusy sync.Map // console id → *sync.Mutex

func (a *App) consolePhotoPath(id string) string {
	return filepath.Join(a.Dir, "console-photos", id+".img")
}

// consolePhotosOn says an administrator opted in to console photos.
func (a *App) consolePhotosOn(ctx context.Context) bool {
	var v string
	ok, err := a.Config(ctx, cfgConsolePhotos, &v)
	return ok && err == nil && v == consolePhotosVersion
}

// consolePhotoAllowed says whether Wikimedia may be asked now.
func (a *App) consolePhotoAllowed(ctx context.Context) bool {
	return a.consolePhotosOn(ctx) && a.wikidataOn(ctx)
}

// consolePhoto is the console's picture's facts, asking Commons when there are none yet (and it
// may), or ok false for none. Off, there are none.
func (a *App) consolePhoto(ctx context.Context, id string) (consolePhotoFacts, bool) {
	if !a.consolePhotosOn(ctx) {
		return consolePhotoFacts{}, false
	}
	m, _ := consolePhotoBusy.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	facts, kept := a.readConsolePhoto(ctx, id)
	if kept && !facts.None {
		return facts, true
	}
	if kept && facts.None && time.Since(time.Unix(facts.At, 0)) < consolePhotoRetry {
		return facts, false
	}
	if !a.consolePhotoAllowed(ctx) {
		return consolePhotoFacts{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	src := photoSource()
	c, err := src.ConsolePhoto(ctx, id, consoleLogoNameOf(id))
	if errors.Is(err, gamelogos.ErrNoPhoto) {
		_ = a.writeConsolePhoto(ctx, id, consolePhotoFacts{None: true, At: time.Now().Unix()}, nil)
		return consolePhotoFacts{}, false
	}
	if err != nil {
		return consolePhotoFacts{}, false // offline, or Wikimedia busy: asked again next time
	}
	body, err := src.Fetch(ctx, c)
	if err != nil || len(body) == 0 || !isPhoto(body) {
		return consolePhotoFacts{}, false
	}
	facts = consolePhotoFacts{At: time.Now().Unix(), File: c.SourceID, Credit: c.Credit, License: c.License, LicenseURL: c.LicenseURL, PageURL: c.PageURL}
	if a.writeConsolePhoto(ctx, id, facts, body) != nil {
		return consolePhotoFacts{}, false
	}
	return facts, true
}

// isPhoto: a JPEG, PNG or WebP, by its first bytes (what Commons' thumbnails are).
func isPhoto(b []byte) bool {
	switch http.DetectContentType(b) {
	case "image/jpeg", "image/png", "image/webp":
		return true
	}
	return false
}

func (a *App) readConsolePhoto(ctx context.Context, id string) (consolePhotoFacts, bool) {
	var f consolePhotoFacts
	var none int
	err := a.DB.QueryRowContext(ctx, `SELECT none, at, file, credit, license, license_url, page_url FROM wittyplay_console_photos WHERE console = ?`, id).
		Scan(&none, &f.At, &f.File, &f.Credit, &f.License, &f.LicenseURL, &f.PageURL)
	if err != nil {
		return f, false
	}
	f.None = none != 0
	if !f.None {
		if _, err := os.Stat(a.consolePhotoPath(id)); err != nil {
			return f, false
		}
	}
	return f, true
}

func (a *App) writeConsolePhoto(ctx context.Context, id string, f consolePhotoFacts, body []byte) error {
	if body != nil {
		if err := writeFileAtomic(a.consolePhotoPath(id), body); err != nil {
			return err
		}
	}
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_console_photos(console, none, at, file, credit, license, license_url, page_url) VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(console) DO UPDATE SET none = excluded.none, at = excluded.at, file = excluded.file, credit = excluded.credit, license = excluded.license,
		license_url = excluded.license_url, page_url = excluded.page_url`, id, boolInt(f.None), f.At, f.File, f.Credit, f.License, f.LicenseURL, f.PageURL)
	return err
}

func handleConsolePhoto(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !consoleIDRe.MatchString(id) {
		writeErr(w, 400, "bad console")
		return
	}
	f, ok := a.consolePhoto(r.Context(), id)
	if !ok {
		writeErr(w, 404, "no picture of this console")
		return
	}
	writeJSON(w, 200, map[string]any{"imageUrl": gmBase + "/console-photos/" + id + "/image?v=" + time.Unix(f.At, 0).UTC().Format("20060102"), "credit": f.Credit,
		"license": f.License, "licenseUrl": f.LicenseURL, "pageUrl": f.PageURL, "source": "Wikimedia Commons"})
}

func handleConsolePhotoImage(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !consoleIDRe.MatchString(id) {
		writeErr(w, 400, "bad console")
		return
	}
	if f, ok := a.readConsolePhoto(r.Context(), id); !ok || f.None || !a.consolePhotosOn(r.Context()) {
		writeErr(w, 404, "no picture of this console")
		return
	}
	servePicture(w, a.consolePhotoPath(id), "private, max-age=604800")
}

// forgetConsolePhotos deletes every console photo kept, and what was kept about them.
func (a *App) forgetConsolePhotos(ctx context.Context) (int, error) {
	var n int
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wittyplay_console_photos WHERE none = 0`).Scan(&n); err != nil {
		return 0, err
	}
	if _, err := a.DB.ExecContext(ctx, `DELETE FROM wittyplay_console_photos`); err != nil {
		return 0, err
	}
	if err := os.RemoveAll(filepath.Join(a.Dir, "console-photos")); err != nil {
		return n, err
	}
	return n, nil
}

// consolePhotosState is the opt-in, with the notice to show beside its switch.
func (a *App) consolePhotosState(ctx context.Context) map[string]any {
	var at int64
	_, _ = a.Config(ctx, cfgConsolePhotosAt, &at)
	return map[string]any{"enabled": a.consolePhotosOn(ctx), "notice": ui.ConsolePhotoNotice, "source": "Wikimedia Commons", "changedAt": at,
		"wikidata": a.wikidataOn(ctx)}
}

func handleConsolePhotos(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.consolePhotosState(r.Context()))
}

// setConsolePhotos turns console photos on or off; off deletes the photos kept.
func (a *App) setConsolePhotos(ctx context.Context, on bool, by int64) error {
	v := "0"
	if on {
		v = consolePhotosVersion
	}
	if err := a.SetConfig(ctx, cfgConsolePhotos, v); err != nil {
		return err
	}
	if err := a.SetConfig(ctx, cfgConsolePhotosAt, time.Now().Unix()); err != nil {
		return err
	}
	removed := 0
	if !on {
		var err error
		if removed, err = a.forgetConsolePhotos(ctx); err != nil {
			return err
		}
	}
	a.Log.InfoContext(ctx, "wittyplay: console photos", "cat", "games", "on", on, "removed", removed, "by", by)
	return nil
}

// PUT /games/console-photos {enabled}: the administrator's opt-in, having been shown the notice.
func handleConsolePhotosPut(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil || b.Enabled == nil {
		writeErr(w, 400, "Say {enabled: true} or {enabled: false}.")
		return
	}
	if err := a.setConsolePhotos(r.Context(), *b.Enabled, who(r).User); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, a.consolePhotosState(r.Context()))
}
