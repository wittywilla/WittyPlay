// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// A game's achievements at RetroAchievements, to show with the game. Nothing is earned
// or tracked here yet: every achievement is shown greyed out. The game is found by a
// checksum of its file, taken the first time someone opens it and kept, never during a
// scan. The file is only read: games live on drives that may be read-only.
//
// Badges come through WittyPlay, so a browser never contacts RetroAchievements and
// the key never leaves this machine. (Moved from the server's api_achievements.go.)

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /items/{id}/achievements", w(handleItemAchievements))
		mux.Handle("GET /items/{id}/achievements/badge/{badge}", w(handleItemAchievementBadge))
	})
}

// Achievement answer states.
const (
	achOK          = "ok"
	achNoKey       = "nokey"
	achUnsupported = "unsupported"
	achNotFound    = "notfound"
	achError       = "error"
)

// AchievementsAnswer is GET /items/{id}/achievements.
type AchievementsAnswer struct {
	State        string            `json:"state"`
	Game         *AchievementsGame `json:"game"`
	Achievements []AchievementView `json:"achievements"`
	Message      string            `json:"message"`
}

// AchievementsGame is the game as RetroAchievements knows it.
type AchievementsGame struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	Points int    `json:"points"`
}

// AchievementView is one achievement. Badge is this server's address for its
// greyed-out picture, "" if it has none.
type AchievementView struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Points      int    `json:"points"`
	Badge       string `json:"badge"`
}

// raHashSlots: checksums taken at once. Each reads a whole file, or unpacks one.
var raHashSlots = make(chan struct{}, 2)

// discExts are files of disc games, whose checksums aren't taken here.
var discExts = map[string]bool{".cue": true, ".m3u": true, ".gdi": true, ".chd": true, ".ccd": true, ".iso": true, ".toc": true,
	".cdi": true, ".pbp": true, ".cso": true, ".img": true, ".mds": true, ".mdf": true, ".nrg": true}

func achAnswer(state, msg string) AchievementsAnswer {
	return AchievementsAnswer{State: state, Achievements: []AchievementView{}, Message: msg}
}

// raGameFor finds the game an item is at RetroAchievements. The answer that isn't a
// game is a whole reply.
func (a *App) raGameFor(ctx context.Context, it metaGame) (*gamemeta.RetroAchievements, *gamemeta.RAGame, *AchievementsAnswer) {
	consoles := gamemeta.RAConsoles(it.Console)
	if len(consoles) == 0 || len(it.Files) == 0 || discExts[strings.ToLower(filepath.Ext(it.Files[0]))] {
		ans := achAnswer(achUnsupported, "Achievements can't be looked up for this console's games yet.")
		return nil, nil, &ans
	}
	ra, ready := a.gameWorker().achievements(ctx)
	if ra == nil || !ready {
		ans := achAnswer(achNoKey, "To see this game's achievements, an administrator can add a RetroAchievements Web API key in WittyPlay's settings, Game details.")
		return nil, nil, &ans
	}
	hash, err := a.raHash(ctx, ra, it)
	if errors.Is(err, gamemeta.ErrRANotCovered) {
		ans := achAnswer(achUnsupported, "Achievements can't be looked up for this game's file.")
		return nil, nil, &ans
	}
	if err != nil {
		a.Log.Info("achievements: no checksum", "cat", "games", "item", it.ID, "err", err)
		ans := achAnswer(achError, "This game's file couldn't be read to find its achievements.")
		return nil, nil, &ans
	}
	id, _, err := ra.FindGame(ctx, consoles, hash)
	if err == nil {
		var g *gamemeta.RAGame
		if g, err = ra.Game(ctx, id); err == nil {
			return ra, g, nil
		}
	}
	ans := raErrorAnswer(err)
	if ans.State == achError {
		a.Log.Info("achievements", "cat", "games", "item", it.ID, "err", err)
	}
	return nil, nil, &ans
}

func raErrorAnswer(err error) AchievementsAnswer {
	var rl *gamemeta.RateLimited
	switch {
	case errors.Is(err, gamemeta.ErrNotFound):
		return achAnswer(achNotFound, "RetroAchievements doesn't know this exact copy of the game, or has no achievements for it.")
	case errors.Is(err, gamemeta.ErrNotSet):
		return achAnswer(achNoKey, "To see this game's achievements, an administrator can add a RetroAchievements Web API key in WittyPlay's settings, Game details.")
	case errors.Is(err, gamemeta.ErrKey):
		return achAnswer(achError, "RetroAchievements didn't accept the Web API key. An administrator can check it in WittyPlay's settings, Game details.")
	case errors.As(err, &rl):
		return achAnswer(achError, "RetroAchievements asked for a pause. Try again in a few minutes.")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return achAnswer(achError, "RetroAchievements didn't answer in time. Try again in a moment.")
	}
	return achAnswer(achError, "RetroAchievements couldn't be asked just now. Try again later.")
}

// raKept is a file's checksum, kept with what the file was when it was taken.
type raKept struct {
	Path  string `json:"path"`
	Entry string `json:"entry,omitempty"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	Hash  string `json:"hash"`
}

// raArchives unpacks an archived game long enough to take its checksum, in WittyPlay's cache.
func (a *App) raArchives() *gamearchive.Cache {
	raArchiveMu.Lock()
	defer raArchiveMu.Unlock()
	c := raArchive[a]
	if c == nil {
		c = &gamearchive.Cache{Dir: filepath.Join(a.Dir, "cache", "ra-unpacked"), Limit: func() int64 { return raUnpackLimit }}
		raArchive[a] = c
	}
	return c
}

var (
	raArchiveMu sync.Mutex
	raArchive   = map[*App]*gamearchive.Cache{}
)

// raUnpackLimit is the room the checksums' unpacked games may take: one large disc.
const raUnpackLimit = 8 << 30

// raHash is the checksum RetroAchievements knows this game's file by. It's taken once
// and kept in WittyPlay's cache, until the file changes. The file is the one the server says
// the item is: never an address from a browser.
func (a *App) raHash(ctx context.Context, ra *gamemeta.RetroAchievements, it metaGame) (string, error) {
	path := it.Files[0]
	if gamemeta.RAByName(it.Console) {
		return gamemeta.RAHashName(path), nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", gamemeta.ErrRANotCovered
	}
	entry := ""
	archive := gamearchive.IsArchive(filepath.Base(path))
	if archive {
		entry = a.gameEntry(ctx, it.ID)
	}
	p := filepath.Join(ra.Dir, "hashes", strconv.FormatInt(it.ID, 10)+".json")
	var kept raKept
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &kept) == nil &&
		kept.Path == path && kept.Entry == entry && kept.Size == st.Size() && kept.MTime == st.ModTime().UnixNano() && kept.Hash != "" {
		return kept.Hash, nil
	}
	select {
	case raHashSlots <- struct{}{}:
		defer func() { <-raHashSlots }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	rom := path
	if archive {
		console := wittyplay.ByID(it.Console)
		if console == nil {
			return "", fmt.Errorf("console %q isn't available to unpack the game", it.Console)
		}
		var cleanup func()
		rom, cleanup, err = a.raArchives().AcquireEntry(ctx, path, console, entry)
		if err != nil {
			return "", err
		}
		if cleanup != nil {
			defer cleanup()
		}
		if discExts[strings.ToLower(filepath.Ext(rom))] {
			return "", gamemeta.ErrRANotCovered
		}
	}
	f, err := os.Open(rom)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fst, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fst.Mode().IsRegular() || fst.Size() > gamemeta.RAMaxSize {
		return "", gamemeta.ErrRANotCovered
	}
	hash, err := gamemeta.RAHash(it.Console, f, fst.Size())
	if err != nil {
		return "", err
	}
	kept = raKept{Path: path, Entry: entry, Size: st.Size(), MTime: st.ModTime().UnixNano(), Hash: hash}
	if b, err := json.Marshal(kept); err == nil {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			tmp, err := os.CreateTemp(filepath.Dir(p), ".hash-*")
			if err == nil {
				_, werr := tmp.Write(b)
				if tmp.Close() == nil && werr == nil {
					_ = os.Rename(tmp.Name(), p)
				}
				_ = os.Remove(tmp.Name())
			}
		}
	}
	return hash, nil
}

// GET /items/{id}/achievements
func handleItemAchievements(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second) // a console's list is a few megabytes
	defer cancel()
	_, g, answer := a.raGameFor(ctx, it)
	if answer != nil {
		writeJSON(w, 200, answer)
		return
	}
	out := achAnswer(achOK, "")
	out.Game = &AchievementsGame{ID: g.ID, Title: g.Title, URL: g.URL(), Points: g.Points}
	for _, ach := range g.Achievements {
		v := AchievementView{ID: ach.ID, Title: ach.Title, Description: ach.Description, Points: ach.Points}
		if ach.Badge != "" {
			v.Badge = fmt.Sprintf(gmBase+"/items/%d/achievements/badge/%s", it.ID, ach.Badge)
		}
		out.Achievements = append(out.Achievements, v)
	}
	writeJSON(w, 200, out)
}

// GET /items/{id}/achievements/badge/{badge}: one of the game's achievement
// pictures, greyed out. Only the game's own pictures are fetched.
func handleItemAchievementBadge(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.gameOf(w, r)
	if !ok {
		return
	}
	badge := r.PathValue("badge")
	if !gamemeta.ValidBadge(badge) {
		writeErr(w, 400, "bad badge name")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	ra, g, answer := a.raGameFor(ctx, it)
	if answer != nil || !g.HasBadge(badge) {
		writeErr(w, 404, "no such badge")
		return
	}
	b, err := ra.Badge(ctx, badge)
	if err != nil {
		writeErr(w, 404, "no such badge")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}
