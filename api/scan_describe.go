// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// scan.describe (module API 5): after a scan of a games library, the server asks what's known about
// the games that are new or whose files changed, and about one game an administrator asked to be
// looked up again. WittyPlay answers at once with what it knows without asking anyone: an NFO
// beside the game, box art beside it (given to the server with PutImage), and the libretro
// database if its list for the console has been downloaded already. The rest is queued, and the
// worker (gamedetails_worker.go) sets what it finds with SetItem later.
//
// Nothing is ever written beside a game during a scan: its folder is the owner's, and may well be
// read-only. Only an administrator's own request writes there (wittyplay_edit.go, wittyplay_convert.go).

func init() { scanDescribe = describeGames }

// describeBudget is how long one call spends on the local database before leaving the rest to the
// worker: a scan waits for the answer.
const describeBudget = 20 * time.Second

func describeGames(ctx context.Context, a *App, args wittymodule.ScanDescribeArgs) (wittymodule.ScanDescribed, error) {
	out := wittymodule.ScanDescribed{Items: []wittymodule.ItemFields{}}
	g := a.gameWorker()
	set := g.sources(ctx)
	var x gamemeta.Identifier
	defer x.Close()
	until := time.Now().Add(describeBudget)
	report := a.reportScan(args.Library.ID)
	for i, it := range args.Items {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if report != nil && i > 0 {
			report(i, len(args.Items), "")
		}
		if it.Kind != "game" || len(it.Files) == 0 {
			continue
		}
		console := ""
		if it.Facets != nil {
			console = it.Facets["console"]
		}
		f, local := a.localGameFields(ctx, it, console, a.foreignNFO(ctx, args.Library.ID))
		// A .wittyplay beside it: its look, and, when made for this dump, details over the NFO's
		// (wittyplay_sidecar.go). Read only, never written here.
		a.applySidecar(ctx, it, console, &f, &local)
		if err := a.enqueueGame(ctx, gameEnqueued{ItemID: it.ID, LibraryID: args.Library.ID, Console: console, Title: it.Title, Files: it.Files, Local: local, Edited: it.Edited}); err != nil {
			return out, err
		}
		if args.Refresh {
			// Looked up again from the start, a locked match too (its chosen answer stays), with
			// its pictures downloaded again and its file read again (gamerefresh.go).
			if err := g.refreshDetails(ctx, []int64{it.ID}, true); err != nil {
				return out, err
			}
		}
		if time.Now().Before(until) {
			if r, err := a.gameRow(ctx, it.ID); err == nil {
				j := &gameJob{meta: r}
				if ok, _ := g.describeLocal(gamemeta.Offline(ctx), &x, j, set, false); ok {
					answers, _, _ := a.gameAnswers(ctx, it.ID)
					if m, err := g.mergeAnswers(ctx, it.ID, answersInOrder(answers, set.on)); err == nil {
						f = overlayFields(m, f)
					}
				}
			}
		}
		out.Items = append(out.Items, f)
	}
	g.Kick()
	return out, nil
}

// overlayFields is what the sources found, with what the owner's own files say over it.
func overlayFields(found, own wittymodule.ItemFields) wittymodule.ItemFields {
	out := found
	if own.Title != "" {
		out.Title = own.Title
	}
	if own.SortTitle != "" {
		out.SortTitle = own.SortTitle
	}
	if own.Tagline != "" {
		out.Tagline = own.Tagline
	}
	if own.Overview != "" {
		out.Overview = own.Overview
	}
	if own.Year != 0 {
		out.Year = own.Year
	}
	if len(own.Genres) > 0 {
		out.Genres = own.Genres
	}
	if own.Rating != nil {
		out.Rating = own.Rating
	}
	if own.ContentRating != "" {
		out.ContentRating = own.ContentRating
	}
	if own.Studio != "" {
		out.Studio = own.Studio
	}
	if own.Poster != "" {
		out.Poster = own.Poster
	}
	if own.Backdrop != "" {
		out.Backdrop = own.Backdrop
	}
	return out
}

// localGameFields reads what the owner's files beside a game say: an NFO, and box art. local
// lists the fields they gave, which sources then leave alone. An NFO WittyMediaServer wrote beside
// a game earlier is only what it thought then: what it says is used, but a better answer replaces it.
// foreign says the library reads NFOs other programs wrote; without it only the server's own count.
func (a *App) localGameFields(ctx context.Context, it wittymodule.ScanDescribeItem, console string, foreign bool) (wittymodule.ItemFields, []string) {
	f := wittymodule.ItemFields{ID: it.ID}
	var local []string
	path := it.Files[0]
	dir, base := filepath.Dir(path), gameBaseName(filepath.Base(path))
	nfo := filepath.Join(dir, base+".nfo")
	if m, ok := readGameNFO(nfo); ok && (foreign || m.writtenHere) {
		ours := m.writtenHere
		note := func(field string) {
			if !ours {
				local = append(local, field)
			}
		}
		if m.Title != "" && m.Title != it.Title {
			f.Title = m.Title
			note("title")
		}
		if m.SortTitle != "" {
			f.SortTitle = m.SortTitle
		}
		if m.Tagline != "" {
			f.Tagline = m.Tagline
			note("tagline")
		}
		if m.Plot != "" {
			f.Overview = m.Plot
			note("overview")
		} else if m.Outline != "" {
			f.Overview = m.Outline
			note("overview")
		}
		if y := m.year(); y != 0 {
			f.Year = y
			note("year")
		}
		if len(m.Genres) > 0 {
			f.Genres = m.Genres
			note("genres")
		}
		if r := m.rating(); r != nil {
			f.Rating = r
			note("rating")
		}
		if m.MPAA != "" {
			f.ContentRating = m.MPAA
			note("contentRating")
		}
		if m.Studio != "" {
			f.Studio = m.Studio
			note("studio")
		}
	}
	if p := gameArtBeside(dir, base); p != "" {
		if u := a.putLocalPicture(ctx, it.ID, "poster", p); u != "" {
			f.Poster = u
			local = append(local, "poster")
		}
	}
	if p := gameBackdropBeside(dir, base); p != "" {
		if u := a.putLocalPicture(ctx, it.ID, "backdrop", p); u != "" {
			f.Backdrop = u
			local = append(local, "backdrop")
		}
	}
	return f, local
}

// maxLocalPicture is the biggest picture beside a game that's given to the server.
const maxLocalPicture = 20 << 20

// putLocalPicture gives the server a picture from beside a game, if it is one, and says its address.
func (a *App) putLocalPicture(ctx context.Context, item int64, role, p string) string {
	st, err := os.Stat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxLocalPicture {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil || !strings.HasPrefix(http.DetectContentType(b), "image/") {
		return ""
	}
	u, err := a.Host.PutImage(ctx, item, role, bytes.NewReader(b))
	if err != nil {
		a.Log.Debug("wittyplay: a picture beside a game couldn't be given to the server", "cat", "games", "item", item, "err", err)
		return ""
	}
	return u
}

// artworkExts are the pictures beside a game that are box art.
var artworkExts = []string{".jpg", ".jpeg", ".jfif", ".png", ".webp", ".avif", ".gif", ".bmp", ".tbn"}

// gameArtBeside finds a picture for a game: its own name beside it, or in the folders other
// software keeps box art in. A shared cover.jpg is ignored, since one folder holds many games.
func gameArtBeside(dir, base string) string {
	for _, d := range []string{dir, filepath.Join(dir, "boxart"), filepath.Join(dir, "covers"), filepath.Join(dir, "images"), filepath.Join(dir, "media", "box2d")} {
		for _, n := range []string{base + "-cover", base + "-poster", base} {
			for _, ext := range artworkExts {
				p := filepath.Join(d, n+ext)
				if st, err := os.Stat(p); err == nil && !st.IsDir() {
					return p
				}
			}
		}
	}
	return ""
}

// gameBackdropBeside finds a wide picture for a game beside it, under the name saving metadata used.
func gameBackdropBeside(dir, base string) string {
	for _, ext := range artworkExts {
		p := filepath.Join(dir, base+"-fanart"+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// gameNFO is what an NFO beside a game says, in the form media servers write.
type gameNFO struct {
	Title     string   `xml:"title"`
	SortTitle string   `xml:"sorttitle"`
	Year      int      `xml:"year"`
	Plot      string   `xml:"plot"`
	Outline   string   `xml:"outline"`
	Tagline   string   `xml:"tagline"`
	Genres    []string `xml:"genre"`
	Rating    float64  `xml:"rating"`
	MPAA      string   `xml:"mpaa"`
	Studio    string   `xml:"studio"`
	Premiered string   `xml:"premiered"`
	Released  string   `xml:"releasedate"`
	Ratings   struct {
		Rating []struct {
			Value float64 `xml:"value"`
		} `xml:"rating"`
	} `xml:"ratings"`

	writtenHere bool
}

// nfoMarker is what WittyMediaServer put at the top of an NFO it wrote beside a game.
const nfoMarker = "<!-- written by WittyMediaServer -->"

// maxNFO is the biggest NFO read.
const maxNFO = 1 << 20

// readGameNFO reads an NFO beside a game: <movie> or <game>. Anything after its last closing tag
// (a web address, as some write) is left off.
func readGameNFO(p string) (*gameNFO, bool) {
	fh, err := os.Open(p)
	if err != nil {
		return nil, false
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, maxNFO+1))
	if err != nil || len(b) > maxNFO {
		return nil, false
	}
	s := string(b)
	for _, end := range []string{"</movie>", "</game>"} {
		if i := strings.LastIndex(s, end); i >= 0 {
			s = s[:i+len(end)]
			break
		}
	}
	var m gameNFO
	if xml.Unmarshal([]byte(s), &m) != nil {
		return nil, false
	}
	m.writtenHere = strings.Contains(string(b[:min(len(b), 256)]), nfoMarker)
	clean := func(v string) string { return strings.TrimSpace(v) }
	m.Title, m.SortTitle, m.Plot, m.Outline = clean(m.Title), clean(m.SortTitle), clean(m.Plot), clean(m.Outline)
	m.Tagline, m.MPAA, m.Studio = clipText(clean(m.Tagline), 200), clean(m.MPAA), clean(m.Studio)
	genres := m.Genres[:0]
	for _, g := range m.Genres {
		if g = clean(g); g != "" && !slices.Contains(genres, g) {
			genres = append(genres, g)
		}
	}
	m.Genres = genres
	return &m, true
}

func (m *gameNFO) year() int {
	if m.Year > 1950 && m.Year < 2200 {
		return m.Year
	}
	for _, d := range []string{m.Premiered, m.Released} {
		if len(d) >= 4 {
			if y, err := strconv.Atoi(d[:4]); err == nil && y > 1950 && y < 2200 {
				return y
			}
		}
	}
	return 0
}

func (m *gameNFO) rating() *float64 {
	v := m.Rating
	for _, r := range m.Ratings.Rating {
		if v <= 0 && r.Value > 0 {
			v = r.Value
		}
	}
	if v <= 0 || v > 10 {
		return nil
	}
	return &v
}

// foreignNFO says whether a library reads .nfo files other programs wrote beside its media (a
// library's setting, on unless an administrator turned it off), as the server's host API says.
// Asked at most every half minute; when the server can't be asked, they're read.
func (a *App) foreignNFO(ctx context.Context, library int64) bool {
	nfoMu.Lock()
	defer nfoMu.Unlock()
	if time.Since(nfoAt) > 30*time.Second || nfoLibs == nil {
		var out struct {
			Libraries []struct {
				ID         int64 `json:"id"`
				ForeignNFO *bool `json:"foreignNfo"`
			} `json:"libraries"`
		}
		if a.Host != nil && a.Host.Do(ctx, host.On{}, "GET", "/libraries", nil, &out) == nil {
			nfoLibs = map[int64]bool{}
			for _, l := range out.Libraries {
				nfoLibs[l.ID] = l.ForeignNFO == nil || *l.ForeignNFO
			}
			nfoAt = time.Now()
		}
	}
	use, known := nfoLibs[library]
	return use || !known
}

var (
	nfoMu   sync.Mutex
	nfoAt   time.Time
	nfoLibs map[int64]bool
)
