// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamelogos"
)

// Brand logos (moved from the server's api_game_pictures.go and its logo service's game parts):
// WittyPlay can put real console logos on its cartridges and cases and use them as the console's
// mark, and show a game studio's. Nothing is bundled; an administrator turns it on and confirms a
// warning first, and the logos then come from Wikimedia Commons (downloaded once, kept in
// WittyPlay's own folder, served by WittyPlay, so a browser never contacts the source), with their
// source and licence kept.

const (
	logoConsole    = gamelogos.LogoConsole
	logoGameStudio = gamelogos.LogoGameStudio
)

// cfgBrandLogos is WittyPlay's agreement to brand logos: the version of the warning agreed to, and
// when (cfgBrandLogosAt).
const (
	cfgBrandLogos      = "brandLogos"
	cfgBrandLogosAt    = "brandLogosAt"
	brandLogosVersion  = "1"
	brandLogoTaskName  = "brand-logos"
	brandLogoTaskTitle = "Looking for console logos"
)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/brand-logos", w(handleBrandLogos))
		mux.Handle("PUT /games/brand-logos", w(adminRoute(handleBrandLogosPut)))
		mux.Handle("DELETE /games/brand-logos", w(adminRoute(handleBrandLogosDelete)))
		mux.Handle("GET /game-logos/{id}/image", w(handleBrandLogoImage))
	})
}

var (
	logosMu sync.Mutex
	logos   = map[*App]*gamelogos.Logos{}
)

// logoSource is where logos and console photos come from; a fake in tests.
var logoSource = func() *gamelogos.WikimediaLogos {
	return &gamelogos.WikimediaLogos{Client: sourceClient(20 * time.Second)}
}

// logos is the App's logo service.
func (a *App) logos() *gamelogos.Logos {
	logosMu.Lock()
	defer logosMu.Unlock()
	l := logos[a]
	if l == nil {
		l = &gamelogos.Logos{DB: a.DB.DB, Dir: filepath.Join(a.Dir, "logos"), Source: logoSource(), Log: a.Log,
			Allowed: a.brandLogosOn,
			Report: func(done int, err error) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = a.hostTask(ctx, brandLogoTaskName, brandLogoTaskTitle, done, 0, true, err)
			}}
		logos[a] = l
	}
	return l
}

// logosKick looks for whatever logos are waiting, if the administrator agreed.
func (a *App) logosKick() { a.logos().Kick() }

// ensureLogo makes sure a name has a row, so the service can look for it. Nothing is looked up by it.
func (a *App) ensureLogo(ctx context.Context, kind, name string) (*gamelogos.Logo, error) {
	return a.logos().EnsureLogo(ctx, kind, name)
}

// brandLogosOn says the administrator's agreement to brand logos stands.
func (a *App) brandLogosOn(ctx context.Context) bool {
	var v string
	ok, err := a.Config(ctx, cfgBrandLogos, &v)
	return ok && err == nil && v == brandLogosVersion
}

// consoleLogoNameOf is the name a console's logo is found by (consoleLogoName, consoles.go), by its id.
func consoleLogoNameOf(id string) string {
	if c := wittyplay.ByID(id); c != nil {
		return consoleLogoName(*c)
	}
	return consoleLogoNames[id]
}

// brandLogoConsoles are every console WittyPlay shows, by id, with the name its logo is found by.
func brandLogoConsoles() map[string]string {
	out := map[string]string{}
	for _, c := range wittyplay.Consoles {
		if c.Hidden {
			continue
		}
		out[c.ID] = consoleLogoName(c)
	}
	return out
}

type brandLogo struct {
	ID         int64  `json:"id"`
	Version    int    `json:"version"`
	Opaque     bool   `json:"opaque"`
	Name       string `json:"name"`
	URL        string `json:"url"` // its original; ?variant=white or dark for those
	Source     string `json:"source,omitempty"`
	SourceURL  string `json:"sourceUrl,omitempty"`
	License    string `json:"license,omitempty"`
	LicenseURL string `json:"licenseUrl,omitempty"`
	Credit     string `json:"credit,omitempty"`
}

// brandLogoWarning is what an administrator confirms before any logo is downloaded. The settings
// page shows these words; WittyPlay keeps its own copy so every client shows the same.
var brandLogoWarning = []string{
	"Console and game studio logos are trademarks of their owners.",
	"They are downloaded from Wikimedia Commons for personal, non-commercial display on this server only: console logos by themselves, a game studio's only when you choose one.",
	"This server and WittyPlay are not endorsed by, sponsored by or affiliated with any of the owners.",
	"You are responsible for how they are used here.",
	"Each logo's source and licence is recorded and shown in the credits. You can remove every downloaded logo at any time.",
}

// GET /games/brand-logos: whether WittyPlay shows console logos, and, when it does, each console's
// logo it has (by console id) with where it came from. Off, the list is empty: WittyPlay draws its
// own generic shapes and the console's name.
func handleBrandLogos(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	on := a.brandLogosOn(ctx)
	out := map[string]any{"enabled": on, "logos": map[string]brandLogo{}, "warning": brandLogoWarning}
	pending := 0
	if on {
		list := map[string]brandLogo{}
		l := a.logos()
		for id, name := range brandLogoConsoles() {
			g, err := l.LogoFor(ctx, logoConsole, name)
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			if g == nil {
				continue
			}
			if g.Status == gamelogos.LogoPending {
				pending++
			}
			if !g.Has {
				continue
			}
			list[id] = brandLogo{ID: g.ID, Version: g.Version, Opaque: g.Opaque, Name: g.Name, URL: fmt.Sprintf(gmBase+"/game-logos/%d/image?v=%d", g.ID, g.Version),
				Source: g.Source, SourceURL: g.SourceURL, License: g.License, LicenseURL: g.LicenseURL, Credit: g.Credit}
		}
		out["logos"] = list
	}
	if who(r).Admin {
		var at int64
		_, _ = a.Config(ctx, cfgBrandLogosAt, &at)
		out["pending"] = pending
		if at > 0 {
			out["agreedAt"] = strconv.FormatInt(at, 10)
		} else {
			out["agreedAt"] = ""
		}
	}
	writeJSON(w, 200, out)
}

// PUT /games/brand-logos {enabled, accepted}: turning them on needs accepted, the administrator's
// confirmation of the warning. Turning them off stops downloads and stops showing them; what was
// downloaded stays until removed.
func handleBrandLogosPut(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled  bool `json:"enabled"`
		Accepted bool `json:"accepted"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if b.Enabled && !b.Accepted {
		writeErr(w, 400, "Confirm the warning about trademarks first.")
		return
	}
	ctx := r.Context()
	v := "0"
	if b.Enabled {
		v = brandLogosVersion
	}
	if err := a.SetConfig(ctx, cfgBrandLogos, v); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := a.SetConfig(ctx, cfgBrandLogosAt, time.Now().Unix()); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if b.Enabled {
		var names []string
		for _, n := range brandLogoConsoles() {
			names = append(names, n)
		}
		sort.Strings(names)
		if err := a.logos().Agreed(ctx, names); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	a.Log.InfoContext(ctx, "wittyplay: console logos", "cat", "games", "on", b.Enabled, "by", who(r).User)
	handleBrandLogos(a, w, r)
}

// DELETE /games/brand-logos: removes every downloaded console and game studio logo, files and all,
// and turns them off. A logo an administrator uploaded is theirs, and stays.
func handleBrandLogosDelete(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := a.SetConfig(ctx, cfgBrandLogos, "0"); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	n := 0
	for _, kind := range gamelogos.BrandLogoKinds {
		k, err := a.logos().ForgetDownloadedKind(ctx, kind)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		n += k
	}
	a.Log.InfoContext(ctx, "wittyplay: console logos removed", "cat", "games", "removed", n, "by", who(r).User)
	writeJSON(w, 200, map[string]any{"removed": n, "enabled": false})
}

// GET /game-logos/{id}/image?variant=white|dark|original: a logo WittyPlay downloaded, while
// brand logos are on.
func handleBrandLogoImage(a *App, w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok || !a.brandLogosOn(r.Context()) {
		writeErr(w, 404, "no such logo")
		return
	}
	l := a.logos()
	g, err := l.LogoByID(r.Context(), id)
	if err != nil || g == nil || !g.Has {
		writeErr(w, 404, "no such logo")
		return
	}
	servePicture(w, l.ImagePath(g, r.URL.Query().Get("variant")), "private, max-age=604800")
}
