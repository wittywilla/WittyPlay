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
)

// Console photos for WittyPlay's Consoles tab (moved from the server's api_console_photos.go): a
// picture of each console's hardware from Wikimedia Commons (by its Wikidata entity), fetched by
// WittyPlay once and kept, never linked from the browser, with its author and licence to show
// beside it. It asks Wikimedia only while Wikidata may be asked (WittyPlay's sources) and the
// administrator agreed to downloads from Wikimedia (brand logos); a picture kept before is shown
// either way. With none, the app draws the console's own mark.
//
// GET /console-photos/{id}        → {imageUrl, credit, license, licenseUrl, pageUrl, source}, or 404
// GET /console-photos/{id}/image  → the picture

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /console-photos/{id}", w(handleConsolePhoto))
		mux.Handle("GET /console-photos/{id}/image", w(handleConsolePhotoImage))
	})
}

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

// consolePhotoAllowed says whether Wikimedia may be asked now.
func (a *App) consolePhotoAllowed(ctx context.Context) bool {
	return a.wikidataOn(ctx) && a.brandLogosOn(ctx)
}

// consolePhoto is the console's picture's facts, asking Commons when there are none yet (and it
// may), or ok false for none.
func (a *App) consolePhoto(ctx context.Context, id string) (consolePhotoFacts, bool) {
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
	if f, ok := a.readConsolePhoto(r.Context(), id); !ok || f.None {
		writeErr(w, 404, "no picture of this console")
		return
	}
	servePicture(w, a.consolePhotoPath(id), "private, max-age=604800")
}
