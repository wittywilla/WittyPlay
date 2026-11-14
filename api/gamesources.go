// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// Which sources are in use: built from WittyPlay's settings and the keys an administrator gave
// the server for it, and rebuilt when they change. The keys are host secrets (the server's
// PUT /api/modules/wittyplay/secrets/{name}): read from the server, never kept here, never
// shown, never logged. The order and switches are WittyPlay's own setting (gameSources).

// gameSourceSetting is one source's place in the owner's list.
type gameSourceSetting struct {
	ID string `json:"id"`
	On bool   `json:"on"`
}

// gameSourceKeys are the host secrets that hold what sources need.
var gameSourceKeys = []string{"igdbClientId", "igdbClientSecret", "rawgKey", "tgdbKey", "ssUser", "ssPassword", "ssDevId", "ssDevPassword", "retroAchievementsKey"}

// achievementKeys are the keys among those that don't change what's known about games, so a
// new one doesn't send every game to be looked up again.
var achievementKeys = []string{"retroAchievementsKey"}

// defaultSourceOrder is who's believed first until the owner says otherwise. The best data first;
// Wikidata last, as it often has plain summaries when nothing else does.
var defaultSourceOrder = []string{"igdb", "screenscraper", "libretro", "libretroart", "wikidata", "rawg", "tgdb", "retroachievements"}

// Settings keys (wittyplay_config).
const (
	cfgGameSources = "gameSources"
	// cfgWikidata: Wikidata may be asked (games' details, console photos). On until switched off.
	cfgWikidata = "wikidata"
)

// gameSourceOrder reads the owner's list, with any source it doesn't mention added at the end,
// switched on (Wikidata only if it's allowed at all).
func gameSourceOrder(saved []gameSourceSetting, wikidata bool) []gameSourceSetting {
	seen := map[string]bool{}
	out := make([]gameSourceSetting, 0, len(defaultSourceOrder))
	for _, s := range saved {
		if slices.Contains(defaultSourceOrder, s.ID) && !seen[s.ID] {
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	for _, id := range defaultSourceOrder {
		if !seen[id] {
			out = append(out, gameSourceSetting{ID: id, On: id != "wikidata" || wikidata})
		}
	}
	return out
}

// wikidataOn says whether Wikidata may be asked.
func (a *App) wikidataOn(ctx context.Context) bool {
	on := true
	_, _ = a.Config(ctx, cfgWikidata, &on)
	return on
}

func (a *App) savedSourceOrder(ctx context.Context) []gameSourceSetting {
	var saved []gameSourceSetting
	_, _ = a.Config(ctx, cfgGameSources, &saved)
	return gameSourceOrder(saved, a.wikidataOn(ctx))
}

// sourceSet is the sources in use, kept while the settings they're built from stay the same, so
// their pacing and sign-ins carry over.
type sourceSet struct {
	signature string
	all       map[string]gamemeta.Source
	order     []string // every source, in the owner's order
	on        []string // those switched on and able to run
}

// readKeys asks the server for the sources' keys again: before each batch, and when an
// administrator looks at the sources. A key that changed sends games to be asked again.
func (g *gameMeta) readKeys(ctx context.Context) map[string]string {
	keys := map[string]string{}
	failed := false
	for _, k := range gameSourceKeys {
		v, err := g.a.Host.Secret(ctx, k)
		if err != nil {
			failed = true
			continue
		}
		keys[k] = strings.TrimSpace(v)
	}
	g.mu.Lock()
	old, had := g.keys, g.keys != nil
	if failed && had {
		// The server couldn't say: what was read last still stands.
		for _, k := range gameSourceKeys {
			if _, ok := keys[k]; !ok {
				keys[k] = old[k]
			}
		}
	}
	g.keys = keys
	g.mu.Unlock()
	if had {
		changed := false
		for _, k := range gameSourceKeys {
			if old[k] != keys[k] && !slices.Contains(achievementKeys, k) {
				changed = true
			}
		}
		if changed {
			_ = g.requeueAll(ctx)
		}
	}
	return keys
}

// currentKeys are the keys as last read, reading them the first time.
func (g *gameMeta) currentKeys(ctx context.Context) map[string]string {
	g.mu.Lock()
	k := g.keys
	g.mu.Unlock()
	if k == nil {
		return g.readKeys(ctx)
	}
	return k
}

// sources builds the source list, reusing the last one if nothing has changed.
func (g *gameMeta) sources(ctx context.Context) *sourceSet {
	keys := g.currentKeys(ctx)
	var sig strings.Builder
	for _, k := range gameSourceKeys {
		sig.WriteString(k + "=" + keys[k] + "\n")
	}
	order := g.a.savedSourceOrder(ctx)
	for _, o := range order {
		sig.WriteString(o.ID)
		if o.On {
			sig.WriteString("+")
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.set != nil && g.set.signature == sig.String() {
		return g.set
	}
	set := &sourceSet{signature: sig.String(), all: map[string]gamemeta.Source{}}
	for _, s := range g.build(keys) {
		set.all[s.About().ID] = s
	}
	for _, o := range order {
		src := set.all[o.ID]
		if src == nil {
			continue
		}
		set.order = append(set.order, o.ID)
		if sc, ok := src.(gamemeta.Sidecar); ok && sc.Sidecar() {
			continue // listed, never asked about games
		}
		if o.On && sourceUsable(src, keys) {
			set.on = append(set.on, o.ID)
		}
	}
	for _, src := range set.all {
		if m, ok := src.(gamemeta.Metered); ok {
			m.SetMeter(g.meterFor(src.About().ID, m))
		}
	}
	g.set = set
	return set
}

// sourceUsable: everything the source needs has been given.
func sourceUsable(src gamemeta.Source, keys map[string]string) bool {
	about := src.About()
	if about.Unavailable != "" {
		return false
	}
	for _, f := range about.Fields {
		if keys[f.Setting] == "" && f.Hint == "" {
			return false
		}
	}
	return true
}

func (g *gameMeta) build(s map[string]string) []gamemeta.Source {
	if g.Built != nil {
		return g.Built()
	}
	client := g.HTTP
	if client == nil {
		client = sourceClient(90 * time.Second) // a whole console's list can take a while
	}
	cache := filepath.Join(g.a.Dir, "cache", "gamemeta")
	db := &gamemeta.Libretro{HTTP: client, Dir: filepath.Join(cache, "libretro")}
	return []gamemeta.Source{
		db,
		&gamemeta.LibretroArt{DB: db},
		&gamemeta.WikidataSource{HTTP: client, Dir: filepath.Join(cache, "wikidata")},
		&gamemeta.IGDB{ClientID: s["igdbClientId"], Secret: s["igdbClientSecret"], HTTP: client},
		&gamemeta.ScreenScraper{User: s["ssUser"], Password: s["ssPassword"], DevUser: s["ssDevId"], DevPass: s["ssDevPassword"], HTTP: client},
		&gamemeta.RAWG{Key: s["rawgKey"], HTTP: client},
		&gamemeta.TGDB{Key: s["tgdbKey"], HTTP: client},
		&gamemeta.RetroAchievements{Key: s["retroAchievementsKey"], HTTP: client, Dir: filepath.Join(cache, "retroachievements")},
	}
}

// sourceClient asks the internet only: an address that turns out to be on this machine or its
// network is refused when it connects.
func sourceClient(timeout time.Duration) *http.Client {
	t := fetchClient.Transport.(*http.Transport).Clone()
	return &http.Client{Timeout: timeout, Transport: t}
}

// achievements is the RetroAchievements source. ready: it has a key and is switched on.
func (g *gameMeta) achievements(ctx context.Context) (ra *gamemeta.RetroAchievements, ready bool) {
	set := g.sources(ctx)
	ra, _ = set.all["retroachievements"].(*gamemeta.RetroAchievements)
	if ra == nil {
		return nil, false
	}
	keys := g.currentKeys(ctx)
	for _, o := range g.a.savedSourceOrder(ctx) {
		if o.ID == "retroachievements" {
			return ra, o.On && sourceUsable(ra, keys)
		}
	}
	return ra, false
}

// Requests sources with an allowance have spent are counted in WittyPlay's settings, so a
// restart doesn't forget them.

type meterState struct {
	Period string `json:"period"`
	Used   int    `json:"used"`
}

var meterMu sync.Mutex

func periodKey(period string, now time.Time) string {
	if period == "day" {
		return now.Format("2006-01-02")
	}
	return now.Format("2006-01")
}

func periodEnd(period string, now time.Time) time.Time {
	if period == "day" {
		return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	}
	return time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
}

func (g *gameMeta) meterFor(id string, m gamemeta.Metered) func(int) error {
	limit, period := m.Allowance()
	key := "gameSourceUse." + id
	return func(n int) error {
		if limit <= 0 {
			return nil
		}
		meterMu.Lock()
		defer meterMu.Unlock()
		ctx := context.Background()
		now := time.Now()
		st := meterState{Period: periodKey(period, now)}
		var saved meterState
		if ok, err := g.a.Config(ctx, key, &saved); ok && err == nil && saved.Period == st.Period {
			st.Used = saved.Used
		}
		if st.Used+n > limit {
			return &gamemeta.RateLimited{Until: periodEnd(period, now), Why: "this " + period + "'s allowance is used up"}
		}
		st.Used += n
		_ = g.a.SetConfig(ctx, key, st)
		return nil
	}
}

// gameSourceView is one source as the setup wizard, Settings and the apps show it.
type gameSourceView struct {
	gamemeta.About
	On bool `json:"on"`
	// Ready: it has everything it needs, so being on means it's in use.
	Ready bool `json:"ready"`
	// Saved lists which of its fields have a value. The values never leave the server.
	Saved []string `json:"saved"`
	// Used and Allowance: requests spent this period, and how many it may spend.
	Used      int    `json:"used,omitempty"`
	Allowance int    `json:"allowance,omitempty"`
	Period    string `json:"period,omitempty"`
}

// sourceViews lists every source, in the owner's order.
func (g *gameMeta) sourceViews(ctx context.Context) []gameSourceView {
	keys := g.readKeys(ctx)
	set := g.sources(ctx)
	on := map[string]bool{}
	for _, o := range g.a.savedSourceOrder(ctx) {
		on[o.ID] = o.On
	}
	out := make([]gameSourceView, 0, len(set.order))
	for _, id := range set.order {
		src := set.all[id]
		v := gameSourceView{About: src.About(), On: on[id], Ready: sourceUsable(src, keys), Saved: []string{}}
		for _, f := range v.Fields {
			if keys[f.Setting] != "" {
				v.Saved = append(v.Saved, f.Setting)
			}
		}
		if m, ok := src.(gamemeta.Metered); ok {
			v.Allowance, v.Period = m.Allowance()
			var st meterState
			if ok, _ := g.a.Config(ctx, "gameSourceUse."+id, &st); ok && st.Period == periodKey(v.Period, time.Now()) {
				v.Used = st.Used
			}
		}
		out = append(out, v)
	}
	return out
}

// setSourceOrder saves the owner's list. Answers are kept, so a new order only needs them merged
// again; whatever a newly switched-on source can add, it's asked.
func (g *gameMeta) setSourceOrder(ctx context.Context, list []gameSourceSetting) error {
	clean := gameSourceOrder(list, g.a.wikidataOn(ctx))
	if err := g.a.SetConfig(ctx, cfgGameSources, clean); err != nil {
		return err
	}
	return g.requeueAll(ctx)
}

// testSource tries a source with these details, or the saved ones where a detail is left out,
// without saving anything. The answer is safe to show.
func (g *gameMeta) testSource(ctx context.Context, id string, fields map[string]string) (string, error) {
	keys := map[string]string{}
	for k, v := range g.readKeys(ctx) {
		keys[k] = v
	}
	for k, v := range fields {
		if slices.Contains(gameSourceKeys, k) && strings.TrimSpace(v) != "" && !strings.Contains(v, "•") {
			keys[k] = strings.TrimSpace(v)
		}
	}
	var src gamemeta.Source
	for _, s := range g.build(keys) {
		if s.About().ID == id {
			src = s
		}
	}
	if src == nil {
		return "", errNoSuchSource
	}
	for _, f := range src.About().Fields {
		if keys[f.Setting] == "" && f.Hint == "" {
			return "", errors.New("Fill in " + f.Label + " first.")
		}
	}
	msg, err := src.Test(ctx)
	switch {
	case err == nil:
		return msg, nil
	case errors.Is(err, gamemeta.ErrKey):
		return "", errors.New("The service didn't accept these details. Check that you copied all of them.")
	case errors.Is(err, gamemeta.ErrNotSet):
		return "", errors.New("This source needs more details before it can be tried.")
	case errors.Is(err, context.DeadlineExceeded):
		return "", errors.New("No answer from the service. Is this server online?")
	}
	return "", err
}

var errNoSuchSource = errors.New("no such source")

// ---------------------------------------------------------------- routes

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /gamemeta/sources", w(adminRoute(handleGameSources)))
		mux.Handle("PUT /gamemeta/sources", w(adminRoute(handlePutGameSources)))
		mux.Handle("POST /gamemeta/sources/{id}/test", w(adminRoute(handleGameSourceTest)))
	})
	// Who's asking about games, as the sources require.
	var m struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(wittyplay.ManifestJSON, &m) == nil && m.Version != "" {
		gamemeta.UserAgent = "WittyPlay/" + m.Version + " (https://github.com/wittywilla/WittyPlay)"
	}
}

// adminRoute refuses anyone who isn't an administrator: the server checked already (the route's
// "auth"), and this checks again.
func adminRoute(f func(*App, http.ResponseWriter, *http.Request)) func(*App, http.ResponseWriter, *http.Request) {
	return func(a *App, w http.ResponseWriter, r *http.Request) {
		if !who(r).Admin {
			writeErr(w, 403, "only an administrator can do that")
			return
		}
		f(a, w, r)
	}
}

// GET /gamemeta/sources
func handleGameSources(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"sources": a.gameWorker().sourceViews(r.Context())})
}

// PUT /gamemeta/sources {sources: [{id, on}]}: their order is the order they're believed in. Keys
// aren't taken here: they're the server's secrets for WittyPlay.
func handlePutGameSources(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Sources []gameSourceSetting `json:"sources"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	g := a.gameWorker()
	if err := g.setSourceOrder(r.Context(), b.Sources); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"sources": g.sourceViews(r.Context())})
}

// POST /gamemeta/sources/{id}/test {fields: {setting: value}}: does this work? A field left out,
// or sent back masked, uses what's saved. Nothing is saved here.
func handleGameSourceTest(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Fields map[string]string `json:"fields"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	msg, err := a.gameWorker().testSource(ctx, r.PathValue("id"), b.Fields)
	if errors.Is(err, errNoSuchSource) {
		writeErr(w, 404, "no such source")
		return
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}
