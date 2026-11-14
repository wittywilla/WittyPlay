// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// Finding out about games, in the background (moved from the server's library.GameMeta). A scan
// only lists files and asks WittyPlay to describe the new ones (scan_describe.go), which answers
// with what it knows at once and queues the rest here.
//
// It works through wittyplay_game_details a batch at a time. Each game's question goes to each
// source once and the answer is kept, so a restart loses nothing, changing the order of sources
// costs no requests, and adding a source only asks that one. Sources pace themselves, and one that
// says stop is left alone until it says it can be asked again. Pictures come last, in their own
// pass, because they're the slow part. What's found is set on the server's item with SetItem,
// which never changes a field a person typed.

// gameMetaBatch is how many games are worked on at once. Bulk sources get a batch's worth of
// questions in one go.
const gameMetaBatch = 50

// gameMeta runs the background work for one App.
type gameMeta struct {
	a *App
	// HTTP asks the sources, and Pictures fetches pictures. Both refuse local addresses unless a
	// test says otherwise.
	HTTP, Pictures *http.Client
	// Built overrides the sources, for tests.
	Built func() []gamemeta.Source
	// first is how long the worker waits after starting before its first round.
	first time.Duration

	kick chan struct{}
	id   gamemeta.Identifier

	mu      sync.Mutex
	set     *sourceSet
	keys    map[string]string
	running bool
	phase   string
	paused  map[string]gamemeta.RateLimited
	gates   map[string]*time.Time
	started bool
}

var (
	gameMetasMu sync.Mutex
	gameMetas   = map[*App]*gameMeta{}
)

// gameWorker is the App's worker, made the first time anything needs it.
func (a *App) gameWorker() *gameMeta {
	gameMetasMu.Lock()
	defer gameMetasMu.Unlock()
	g := gameMetas[a]
	if g == nil {
		g = &gameMeta{a: a, kick: make(chan struct{}, 1), first: 10 * time.Second, paused: map[string]gamemeta.RateLimited{}, gates: map[string]*time.Time{}}
		gameMetas[a] = g
	}
	return g
}

func init() {
	inBackground(func(a *App) { a.gameWorker().start() })
	for _, ev := range []string{"scan.finished", "library.removed", "item.removed"} {
		onEvent(ev, func(a *App, _ json.RawMessage) { a.pruneSoon() })
	}
}

// start runs the worker, once.
func (g *gameMeta) start() {
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return
	}
	g.started = true
	g.mu.Unlock()
	go g.run(context.Background())
}

func (g *gameMeta) log() *slog.Logger {
	if g.a.Log != nil {
		return g.a.Log
	}
	return slog.Default()
}

// Kick says there may be new work.
func (g *gameMeta) Kick() {
	if g == nil {
		return
	}
	g.start()
	select {
	case g.kick <- struct{}{}:
	default:
	}
}

// run does the work until ctx ends. It wakes when kicked, and now and then by itself for
// whatever was waiting for a time.
func (g *gameMeta) run(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			g.log().Error("wittyplay: finding out about games stopped", "cat", "games", "err", fmt.Sprint(p))
		}
	}()
	// Not straight away: the service has just started, and has other things to do first.
	first := time.NewTimer(g.first)
	defer first.Stop()
	select {
	case <-first.C:
	case <-g.kick:
	case <-ctx.Done():
		return
	}
	tick := time.NewTicker(5 * time.Minute)
	defer tick.Stop()
	for {
		g.work(ctx)
		select {
		case <-g.kick:
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}

func (g *gameMeta) setPhase(running bool, phase string) {
	g.mu.Lock()
	g.running, g.phase = running, phase
	g.mu.Unlock()
}

// work goes until there's nothing left to do right now.
//
// What WittyPlay can work out by itself comes first, for every game: a library of thousands has
// its names and box art within a minute or two. After that, pictures and online sources go side
// by side, each at its own pace, as one source that's slow or asks to wait shouldn't hold
// everything up.
func (g *gameMeta) work(ctx context.Context) {
	defer g.setPhase(false, "")
	defer g.id.Close()
	g.readKeys(ctx)
	t := &gameMetaTask{g: g}
	var failed error
	defer func() { t.finish(ctx, failed) }()
	defer g.a.logosKick()
	t.stage(ctx, phaseLocal)
	for ctx.Err() == nil {
		g.setPhase(true, "details")
		n, err := g.localBatch(ctx, t)
		if err != nil && ctx.Err() == nil {
			g.log().Warn("game details", "cat", "games", "err", err)
			break
		}
		if n == 0 {
			break
		}
	}
	t.stage(ctx, phaseOnline)
	finished := make(chan struct{})
	pictures := make(chan struct{})
	go func() {
		defer close(pictures)
		defer func() {
			if p := recover(); p != nil {
				g.log().Error("game pictures", "cat", "games", "err", fmt.Sprint(p))
			}
		}()
		for ctx.Err() == nil {
			n, err := g.picturesBatch(ctx)
			if err != nil {
				if ctx.Err() == nil {
					g.log().Warn("game pictures", "cat", "games", "err", err)
				}
				return
			}
			if n > 0 {
				t.picture(n)
				continue
			}
			select {
			case <-finished:
				n, err := g.picturesBatch(ctx)
				if n == 0 || err != nil {
					return
				}
				t.picture(n)
			case <-ctx.Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	}()
	defer func() {
		close(finished)
		<-pictures
	}()
	for ctx.Err() == nil {
		g.setPhase(true, "details")
		g.readKeys(ctx) // a key given meanwhile is used from the next batch
		details, err := g.detailsBatch(ctx, t)
		if err != nil && ctx.Err() == nil {
			g.log().Warn("game details", "cat", "games", "err", err)
			failed = err
			return
		}
		if details == 0 {
			t.stage(ctx, phasePictures)
			return
		}
	}
}

// gameJob is one game being worked on.
type gameJob struct {
	meta   gameRow
	lookup gamemeta.Query
}

// pendingWhere is what a game waiting to be asked about is: not the test console's.
const pendingWhere = `status = 'pending' AND path <> '' AND console <> '` + wittyplay.TestConsole + `'`

// localBatch asks the local database about games it hasn't been asked about, and applies the
// answers straight away. Nothing here goes over the internet, beyond fetching a console's list
// once.
func (g *gameMeta) localBatch(ctx context.Context, t *gameMetaTask) (int, error) {
	set := g.sources(ctx)
	if set.all["libretro"] == nil || !slices.Contains(set.on, "libretro") {
		return 0, nil
	}
	const unasked = pendingWhere + ` AND NOT EXISTS (SELECT 1 FROM wittyplay_game_answers s WHERE s.item_id = d.item_id AND s.source = 'libretro')`
	jobs, err := g.nextJobs(ctx, unasked, 4*gameMetaBatch)
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	done := 0
	for i := range jobs {
		if ctx.Err() != nil {
			return done, ctx.Err()
		}
		ok, err := g.describeLocal(ctx, &g.id, &jobs[i], set, true)
		if err != nil {
			return done, err
		}
		if !ok {
			// The list can't be had just now, or it doesn't know this game.
			if _, asked, err := g.a.gameAnswers(ctx, jobs[i].meta.ItemID); err != nil || !asked["libretro"] {
				continue
			}
		}
		done++
		t.did(1)
	}
	return done, nil
}

// describeLocal asks the local database about one game, then the list of picture names, and
// applies what they said. apply false only saves the answers, for the caller to apply.
func (g *gameMeta) describeLocal(ctx context.Context, x *gamemeta.Identifier, j *gameJob, set *sourceSet, apply bool) (bool, error) {
	identifyGame(ctx, g.a, x, j)
	if _, ok := g.askLocal(ctx, j, set); !ok {
		return false, nil
	}
	answers, _, err := g.a.gameAnswers(ctx, j.meta.ItemID)
	if err != nil {
		return false, nil
	}
	// Pictures from a list of names, which asks nothing of anyone until they're fetched.
	if art := set.all["libretroart"]; art != nil && slices.Contains(set.on, "libretroart") {
		in, err := art.Lookup(ctx, j.lookup)
		switch {
		case err == nil:
			_ = g.a.saveGameAnswer(ctx, j.meta.ItemID, "libretroart", in)
		case errors.Is(err, gamemeta.ErrNotFound):
			_ = g.a.saveGameAnswer(ctx, j.meta.ItemID, "libretroart", nil)
		}
		answers, _, _ = g.a.gameAnswers(ctx, j.meta.ItemID)
	}
	if apply {
		return true, g.applyAnswers(ctx, j.meta.ItemID, answersInOrder(answers, set.on))
	}
	return true, nil
}

// nextJobs picks games to work on: one console at a time, because that's how the bulk sources
// want asking.
func (g *gameMeta) nextJobs(ctx context.Context, where string, n int, args ...any) ([]gameJob, error) {
	var console string
	err := g.a.DB.QueryRowContext(ctx, `SELECT console FROM wittyplay_game_details d WHERE `+where+` ORDER BY console LIMIT 1`, args...).Scan(&console)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ids, err := g.a.gameIDs(ctx, `SELECT item_id FROM wittyplay_game_details d WHERE `+where+` AND console = ? ORDER BY item_id LIMIT ?`, append(args, console, n)...)
	if err != nil {
		return nil, err
	}
	var out []gameJob
	for _, id := range ids {
		r, err := g.a.gameRow(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, gameJob{meta: r})
	}
	return out, nil
}

// ---------------------------------------------------------------- asking sources

// fetchOrder is the order questions are asked in, which isn't the order answers are trusted in:
// free, local answers first, so the rest can be asked by the exact name the file turned out to
// have.
func fetchOrder(ids []string) []string {
	rank := map[string]int{"libretro": 0, "libretroart": 0, "wikidata": 1, "screenscraper": 2}
	out := append([]string{}, ids...)
	sort.SliceStable(out, func(a, b int) bool {
		ra, ok := rank[out[a]]
		if !ok {
			ra = 3
		}
		rb, ok := rank[out[b]]
		if !ok {
			rb = 3
		}
		return ra < rb
	})
	return out
}

func (g *gameMeta) detailsBatch(ctx context.Context, t *gameMetaTask) (int, error) {
	jobs, err := g.nextJobs(ctx, pendingWhere+` AND next_try <= ?`, gameMetaBatch, time.Now().Unix())
	if err != nil || len(jobs) == 0 {
		return 0, err
	}
	set := g.sources(ctx)
	order := fetchOrder(set.on)
	console := jobs[0].meta.Console
	qs := make([]gamemeta.Query, len(jobs))
	for i := range jobs {
		g.identify(ctx, &jobs[i])
		qs[i] = jobs[i].lookup
	}
	// Local answers come first, so that bulk sources are asked by the name a file turned out to
	// have and not the one it was given.
	for i := range jobs {
		if q, ok := g.askLocal(ctx, &jobs[i], set); ok {
			qs[i] = q
		}
	}
	for _, id := range order {
		if p, ok := set.all[id].(gamemeta.Preparer); ok && id != "libretro" && g.pausedUntil(id).IsZero() {
			if err := p.Prepare(ctx, console, qs); err != nil {
				g.notePause(id, err)
				if ctx.Err() != nil {
					return 0, ctx.Err()
				}
				g.log().Info("game source", "cat", "games", "source", id, "err", err)
			}
		}
	}
	for i := range jobs {
		if ctx.Err() != nil {
			return i, ctx.Err()
		}
		if err := g.lookup(ctx, &jobs[i], set, order); err != nil && ctx.Err() == nil {
			g.log().Warn("game details", "cat", "games", "item", jobs[i].meta.ItemID, "err", err)
		}
		t.did(1) // one at a time, so the time left that's shown means something
	}
	return len(jobs), nil
}

// askLocal asks the local database about a game, if it hasn't been asked, and says what the game
// should be asked about as from now on.
func (g *gameMeta) askLocal(ctx context.Context, j *gameJob, set *sourceSet) (gamemeta.Query, bool) {
	src := set.all["libretro"]
	if src == nil || !slices.Contains(set.on, "libretro") {
		return j.lookup, false
	}
	answers, done, err := g.a.gameAnswers(ctx, j.meta.ItemID)
	if err != nil {
		return j.lookup, false
	}
	in := answers["libretro"]
	if !done["libretro"] {
		var lerr error
		in, lerr = src.Lookup(ctx, j.lookup)
		switch {
		case lerr == nil:
			_ = g.a.saveGameAnswer(ctx, j.meta.ItemID, "libretro", in)
		case errors.Is(lerr, gamemeta.ErrNotFound):
			_ = g.a.saveGameAnswer(ctx, j.meta.ItemID, "libretro", nil)
			return j.lookup, false
		default:
			return j.lookup, false
		}
	}
	if in != nil && in.Confidence >= 90 && in.Title != "" {
		j.lookup.Title = gamemeta.SearchTitle(gameCleanTitle(in.Title))
	}
	return j.lookup, true
}

// identify works out what's needed to ask about a game, once per version of its file.
func (g *gameMeta) identify(ctx context.Context, j *gameJob) {
	identifyGame(ctx, g.a, &g.id, j)
}

// identifyGame is identify with an Identifier of the caller's: describing during a scan has its
// own, as the worker's isn't for two goroutines at once.
func identifyGame(ctx context.Context, a *App, x *gamemeta.Identifier, j *gameJob) {
	r := &j.meta
	title, tags := gameTitleOf(r)
	var size, mtime int64 = -2, -2
	if st, err := os.Stat(r.Path); err == nil {
		size, mtime = st.Size(), st.ModTime().Unix()
	}
	if size >= 0 && (r.FileSize != size || r.FileMTime != mtime) {
		id := x.Identify(r.Path, r.Console, r.ChosenEntry)
		r.CRCs, r.AltCRCs, r.CRCSize = id.CRCs, id.AltCRCs, id.Size
		if id.Serial != "" {
			r.Serial = id.Serial
		}
		r.FileSize, r.FileMTime = size, mtime
		_, _ = a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET file_size = ?, file_mtime = ?, crcs = ?, alt_crcs = ?, crc_size = ?, serial = ?, tags = ? WHERE item_id = ?`,
			r.FileSize, r.FileMTime, gmFormatCRCs(r.CRCs), gmFormatCRCs(r.AltCRCs), r.CRCSize, r.Serial, tags, r.ItemID)
	}
	if s := gamemeta.SerialInName(filepath.Base(r.Path)); s != "" && r.Serial == "" {
		r.Serial = s
	}
	q := title
	if r.QueryTitle != "" {
		q = r.QueryTitle // someone chose what this game is
	}
	j.lookup = gamemeta.Query{Console: r.Console, Title: gamemeta.SearchTitle(q), Tags: tags, CRCs: r.CRCs, Size: r.CRCSize, Serial: r.Serial, HeaderTitle: r.HeaderTitle, AltCRCs: r.AltCRCs}
}

// lookup asks the sources that haven't answered about this game yet, then merges.
func (g *gameMeta) lookup(ctx context.Context, j *gameJob, set *sourceSet, order []string) error {
	id := j.meta.ItemID
	answers, done, err := g.a.gameAnswers(ctx, id)
	if err != nil {
		return err
	}
	retry := time.Time{}
	failed, faulted := false, false // something's left to ask; and it went wrong, rather than a source asking to wait
	q := j.lookup
	// Someone chose this game or a source recognised the exact file: use its name.
	for _, sid := range order {
		if a := answers[sid]; a != nil && a.Confidence >= 90 && a.Title != "" {
			q.Title = gamemeta.SearchTitle(gameCleanTitle(a.Title))
			break
		}
	}
	for _, sid := range order {
		if done[sid] {
			continue
		}
		src := set.all[sid]
		if gf, ok := src.(gamemeta.GapFiller); ok && gf.GapsOnly() {
			m := gamemeta.Merge(answersInOrder(answers, set.on))
			if !m.Gaps() {
				continue
			}
		}
		if until := g.pausedUntil(sid); !until.IsZero() {
			retry = laterOf(retry, until)
			failed = true
			continue
		}
		in, err := src.Lookup(ctx, q)
		switch {
		case err == nil:
			answers[sid] = in
			if err := g.a.saveGameAnswer(ctx, id, sid, in); err != nil {
				return err
			}
			if in.Confidence >= 90 && in.Title != "" {
				q.Title = gamemeta.SearchTitle(gameCleanTitle(in.Title))
			}
		case errors.Is(err, gamemeta.ErrNotFound):
			if err := g.a.saveGameAnswer(ctx, id, sid, nil); err != nil {
				return err
			}
		case errors.Is(err, gamemeta.ErrNotSet), errors.Is(err, gamemeta.ErrKey):
			// Nothing to retry until the key changes, which asks everything again anyway.
			g.notePause(sid, &gamemeta.RateLimited{Until: time.Now().Add(time.Hour), Why: err.Error()})
			retry = laterOf(retry, time.Now().Add(time.Hour))
			failed = true
		default:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if g.notePause(sid, err) {
				retry = laterOf(retry, g.pausedUntil(sid))
			} else {
				retry = laterOf(retry, time.Now().Add(backoff(j.meta.Tries)))
				faulted = true
				g.log().Info("game source", "cat", "games", "source", sid, "err", err)
			}
			failed = true
		}
	}
	if err := g.applyAnswers(ctx, id, answersInOrder(answers, set.on)); err != nil {
		return err
	}
	if failed && (!faulted || j.meta.Tries < 8) {
		tries := j.meta.Tries
		if faulted {
			tries++
		}
		_, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'pending', next_try = ?, tries = ? WHERE item_id = ?`, retry.Unix(), tries, id)
		return err
	}
	_, err = g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'done', next_try = 0, tries = 0, fetched_at = ? WHERE item_id = ?`, time.Now().Unix(), id)
	return err
}

func laterOf(a, b time.Time) time.Time {
	if a.IsZero() || b.After(a) {
		return b
	}
	return a
}

func backoff(tries int) time.Duration {
	return min(5*time.Minute<<min(tries, 8), 24*time.Hour)
}

// gameBaseName is a game's file name without its extension. For an archive that's everything
// that makes it one, and the game's own extension if the name carries it too: "Game (USA).sfc.7z"
// and "Game (USA).tar.gz" are both "Game (USA)".
func gameBaseName(name string) string {
	if !gamearchive.IsArchive(name) {
		return strings.TrimSuffix(name, filepath.Ext(name))
	}
	base := gamearchive.BaseName(name)
	if x := strings.ToLower(filepath.Ext(base)); x != "" && wittyplay.KnownExtension(x) {
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base
}

// gameTitleOf is what a game is called going by its file alone: the title the scan gave it, and
// the tags its file's name has.
func gameTitleOf(r *gameRow) (title, tags string) {
	stem := gameBaseName(filepath.Base(r.Path))
	if r.EntryName != "" {
		stem = r.EntryName
	}
	title, tags = gamemeta.CleanName(stem)
	if r.Title != "" {
		title = r.Title
	}
	return title, tags
}

func gameCleanTitle(name string) string {
	t, _ := gamemeta.CleanName(name)
	return t
}

func (g *gameMeta) pausedUntil(id string) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.paused[id]
	if !ok {
		return time.Time{}
	}
	if time.Now().After(p.Until) {
		delete(g.paused, id)
		return time.Time{}
	}
	return p.Until
}

// notePause remembers that a source asked to be left alone, if that's what err means.
func (g *gameMeta) notePause(id string, err error) bool {
	var rl *gamemeta.RateLimited
	if !errors.As(err, &rl) {
		return false
	}
	g.mu.Lock()
	g.paused[id] = *rl
	g.mu.Unlock()
	return true
}

func answersInOrder(answers map[string]*gamemeta.Info, order []string) []gamemeta.Answer {
	var out []gamemeta.Answer
	for _, id := range order {
		if in := answers[id]; in != nil {
			out = append(out, gamemeta.Answer{Source: id, Info: in})
		}
	}
	return out
}

// ---------------------------------------------------------------- applying answers

// applyAnswers merges the answers and sets the result on the server's item. Scans never take away
// what someone chose, and neither does this: a field the owner's own files gave or a person typed
// stays (and the server refuses to change a typed one anyway).
func (g *gameMeta) applyAnswers(ctx context.Context, id int64, answers []gamemeta.Answer) error {
	f, err := g.mergeAnswers(ctx, id, answers)
	if err != nil {
		return err
	}
	return g.a.Host.SetItem(ctx, f)
}

// mergeAnswers merges the answers into the fields to set on the item, and keeps what's known
// beside them. Pictures are fetched later (art_wanted).
func (g *gameMeta) mergeAnswers(ctx context.Context, id int64, answers []gamemeta.Answer) (wittymodule.ItemFields, error) {
	f := wittymodule.ItemFields{ID: id}
	r, err := g.a.gameRow(ctx, id)
	if err != nil {
		return f, err
	}
	m := gamemeta.Merge(answers)
	fills := func(field string) bool { return !r.Edited[field] }
	mark := func(field string) { r.Applied[field] = true }
	clear := func(field string) { f.Clear = append(f.Clear, field) }

	// What a source filled in goes when no source says it any more: one was switched off, or a
	// match changed. What someone typed is never in Applied, so it's safe.
	fileTitle, fileTags := gameTitleOf(&r)
	drop := func(field string, undo func()) {
		if r.Applied[field] {
			undo()
			delete(r.Applied, field)
		}
	}
	if m.Title == "" {
		drop("title", func() { f.Title = fileTitle })
	}
	if m.Description == "" {
		drop("overview", func() { clear("overview") })
	}
	if gamemeta.Year(m.Released) == 0 {
		drop("year", func() { clear("year") })
	}
	if len(m.Genres) == 0 {
		drop("genres", func() { clear("genres") })
	}
	if m.Developer == "" {
		drop("studio", func() { clear("studio") })
	}
	if m.Rating() == nil {
		drop("rating", func() { clear("rating") })
	}
	if m.ContentRating() == "" {
		drop("contentRating", func() { clear("contentRating") })
	}
	if len(m.Posters) == 0 {
		drop("poster", func() { clear("poster"); r.PosterRef, r.PosterURL = "", "" })
	}
	if len(m.Backdrops) == 0 {
		drop("backdrop", func() { clear("backdrop"); r.BackdropRef, r.BackdropURL = "", "" })
	}

	accepted := m.Confidence >= gamemeta.AcceptConfidence
	if m.Title != "" && !r.Edited["title"] && accepted {
		if title, _ := gamemeta.CleanName(m.Title); title != "" {
			f.Title = title
			mark("title")
		}
	}
	_, nameTags := gamemeta.CleanName(m.Title)
	if nameTags == "" {
		nameTags = m.Tags
	}
	nameTags = gamemeta.DropDiscTag(nameTags)
	if nameTags != "" && fills("tagline") {
		f.Tagline = clipText(nameTags, 200)
		mark("tagline")
	} else if nameTags == "" {
		drop("tagline", func() {
			if f.Tagline = fileTags; fileTags == "" {
				clear("tagline")
			}
		})
	}
	hasOverview := r.Edited["overview"]
	if m.Description != "" && fills("overview") {
		f.Overview = m.Description
		mark("overview")
		hasOverview = true
	}
	if y := gamemeta.Year(m.Released); y != 0 && fills("year") {
		f.Year = y
		mark("year")
	}
	if len(m.Genres) > 0 && fills("genres") {
		f.Genres = m.Genres
		mark("genres")
	}
	if m.Developer != "" && fills("studio") {
		f.Studio = m.Developer
		mark("studio")
		// Only a row: nothing is looked up until an administrator has agreed to logos.
		_, _ = g.a.ensureLogo(ctx, logoGameStudio, m.Developer)
	}
	if v := m.Rating(); v != nil && fills("rating") {
		f.Rating = v
		mark("rating")
	}
	if c := m.ContentRating(); c != "" && fills("contentRating") {
		f.ContentRating = c
		mark("contentRating")
	}
	if m.MatchSource != "" {
		f.Provider, f.ProviderID = "game:"+m.MatchSource, m.MatchID
		mark("provider")
	} else {
		drop("provider", func() { clear("provider") })
	}
	// The game's other names go with the title a source gave. A title someone typed is shown as
	// they typed it, whatever a profile prefers.
	titles := &wittymodule.ItemTitles{}
	if !r.Edited["title"] && accepted {
		titles = gameTitlesFor(m.Native, m.English, f.Title)
	}
	if titles != nil && (titles.Native != "" || titles.English != "") {
		f.Titles = titles
		mark("titles")
	} else {
		drop("titles", func() { clear("titles") })
	}
	// Pictures are fetched later, unless what's wanted is what's already here.
	wantArt := 0
	if len(m.Posters) > 0 && m.Posters[0] != r.PosterRef && fills("poster") {
		wantArt = 1
	}
	if len(m.Backdrops) > 0 && m.Backdrops[0] != r.BackdropRef && fills("backdrop") {
		wantArt = 1
	}
	credits := make([]gamemeta.Credit, 0, len(m.Used))
	set := g.sources(ctx)
	for _, u := range m.Used {
		src := set.all[u]
		if src == nil {
			continue
		}
		about := src.About()
		c := gamemeta.Credit{Source: u, Name: about.Name, Site: about.Site, Text: about.Credit, License: about.License}
		for _, a := range answers {
			if a.Source == u {
				c.URL = a.Info.URL
			}
		}
		credits = append(credits, c)
	}
	_, err = g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET applied = ?, match_source = ?, match_id = ?, match_method = ?, confidence = ?,
		released = ?, developer = ?, publisher = ?, franchise = ?, players = ?, scores = ?, ages = ?, art = ?, credits = ?,
		poster_refs = ?, backdrop_refs = ?, poster_ref = ?, backdrop_ref = ?, poster_url = ?, backdrop_url = ?, has_overview = ?,
		art_wanted = CASE WHEN ? = 1 THEN 1 ELSE art_wanted END, art_tries = CASE WHEN ? = 1 THEN 0 ELSE art_tries END WHERE item_id = ?`,
		gmListOf(r.Applied), m.MatchSource, m.MatchID, m.MatchMethod, m.Confidence, m.Released, m.Developer, m.Publisher, m.Franchise, m.Players,
		toJSON(m.Scores), toJSON(m.Ages), toJSON(m.Art), toJSON(credits), toJSON(m.Posters), toJSON(m.Backdrops), r.PosterRef, r.BackdropRef,
		r.PosterURL, r.BackdropURL, boolInt(hasOverview), wantArt, wantArt, id)
	return f, err
}

// clipText cuts s to at most n bytes, at a character's edge.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// gameTitlesFor is what a game's other names come to, from what the sources said: tags left off
// as from the title, and nothing that only repeats the title. nil when there's nothing to keep.
func gameTitlesFor(native, english, title string) *wittymodule.ItemTitles {
	clean := func(s string) string {
		s, _ = gamemeta.CleanName(s)
		s = strings.TrimSpace(s)
		if strings.EqualFold(s, title) {
			return ""
		}
		return s
	}
	t := &wittymodule.ItemTitles{Native: clean(native), English: clean(english)}
	if t.Native == "" && t.English == "" {
		return nil
	}
	return t
}

// ---------------------------------------------------------------- pictures

// gameImageName is what a picture is called in WittyPlay's picture cache.
func gameImageName(ref, ext string) string {
	h := sha1.Sum([]byte(ref))
	return "game-" + hex.EncodeToString(h[:12]) + ext
}

var pictureExts = map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif"}

// errNoPicture: this picture doesn't exist, so try another. Anything else is worth trying again
// later.
var errNoPicture = errors.New("no such picture")

// pictureGap is how far apart pictures from one site are asked for.
const pictureGap = 250 * time.Millisecond

func (a *App) gameImagesDir() string { return filepath.Join(a.Dir, "cache", "game-pictures") }

func (g *gameMeta) waitForHost(ctx context.Context, host string) error {
	g.mu.Lock()
	next := g.gates[host]
	if next == nil {
		next = new(time.Time)
		g.gates[host] = next
	}
	wait := time.Until(*next)
	*next = time.Now().Add(max(wait, 0) + pictureGap)
	g.mu.Unlock()
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pictureAddress turns a stored reference into something to fetch, and says which site it's on,
// without the address (which may hold a login) ever being kept.
func (g *gameMeta) pictureAddress(ctx context.Context, ref string) (string, string, error) {
	if strings.HasPrefix(ref, "ss:") {
		src, ok := g.sources(ctx).all["screenscraper"].(gamemeta.Referrer)
		if !ok {
			return "", "", errNoPicture
		}
		u, err := src.PictureURL(ref)
		if err != nil {
			return "", "", errNoPicture
		}
		return u, "screenscraper", nil
	}
	if gamemeta.WebAddress(ref) == "" {
		return "", "", errNoPicture
	}
	host := ref
	if _, rest, ok := strings.Cut(ref, "://"); ok {
		host, _, _ = strings.Cut(rest, "/")
	}
	return ref, host, nil
}

// fetchPicture keeps a picture in WittyPlay's own picture cache, and says where it is. Only real
// pictures are kept, however they were labelled.
func (g *gameMeta) fetchPicture(ctx context.Context, ref string) (string, error) {
	dir := g.a.gameImagesDir()
	for _, ext := range []string{".jpg", ".png", ".webp", ".gif"} {
		p := filepath.Join(dir, gameImageName(ref, ext))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	address, host, err := g.pictureAddress(ctx, ref)
	if err != nil {
		return "", err
	}
	if err := g.waitForHost(ctx, host); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, "GET", address, nil)
	if err != nil {
		return "", errNoPicture
	}
	req.Header.Set("User-Agent", gamemeta.UserAgent)
	client := g.Pictures
	if client == nil {
		client = sourceClient(time.Minute)
	}
	res, err := client.Do(req)
	if err != nil {
		// The error would name the address, which can hold a login.
		return "", errors.New("the picture's site did not answer")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == 404 || res.StatusCode == 410:
		return "", errNoPicture
	case res.StatusCode != 200:
		return "", fmt.Errorf("the picture's site answered %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 20<<20+1))
	if err != nil {
		return "", errors.New("the picture couldn't be read")
	}
	if len(b) > 20<<20 {
		return "", errNoPicture
	}
	ext := pictureExts[http.DetectContentType(b)]
	if ext == "" {
		return "", errNoPicture
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	p := filepath.Join(dir, gameImageName(ref, ext))
	if err := writeFileAtomic(p, b); err != nil {
		return "", err
	}
	return p, nil
}

// firstPicture tries a list of references until one works.
func (g *gameMeta) firstPicture(ctx context.Context, refs []string) (file, ref string, err error) {
	for _, r := range refs {
		f, ferr := g.fetchPicture(ctx, r)
		if ferr == nil {
			return f, r, nil
		}
		if !errors.Is(ferr, errNoPicture) {
			return "", "", ferr
		}
	}
	return "", "", errNoPicture
}

func (g *gameMeta) picturesBatch(ctx context.Context) (int, error) {
	ids, err := g.a.gameIDs(ctx, `SELECT item_id FROM wittyplay_game_details WHERE art_wanted = 1 AND art_next <= ? ORDER BY item_id LIMIT ?`, time.Now().Unix(), gameMetaBatch)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		g.itemPictures(ctx, id)
	}
	return len(ids), nil
}

// putPicture gives the server a picture from WittyPlay's cache for an item's role, and says the
// address it's shown at.
func (a *App) putPicture(ctx context.Context, item int64, role, file string) (string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return a.Host.PutImage(ctx, item, role, bytes.NewReader(b))
}

func (g *gameMeta) itemPictures(ctx context.Context, id int64) {
	r, err := g.a.gameRow(ctx, id)
	if err != nil {
		return
	}
	retry := false
	f := wittymodule.ItemFields{ID: id}
	get := func(field, role string, refs []string, was, wasURL string, set func(url string)) string {
		if len(refs) == 0 || r.Edited[field] || (refs[0] == was && wasURL != "") {
			return was
		}
		file, ref, err := g.firstPicture(ctx, refs)
		if err == nil {
			var url string
			if url, err = g.a.putPicture(ctx, id, role, file); err == nil {
				set(url)
				r.Applied[field] = true
				return ref
			}
		}
		if !errors.Is(err, errNoPicture) {
			retry = true
			g.log().Debug("game picture", "cat", "games", "item", id, "err", err)
		}
		return was
	}
	r.PosterRef = get("poster", "poster", r.PosterRefs, r.PosterRef, r.PosterURL, func(u string) { f.Poster, r.PosterURL = u, u })
	r.BackdropRef = get("backdrop", "backdrop", r.BackRefs, r.BackdropRef, r.BackdropURL, func(u string) { f.Backdrop, r.BackdropURL = u, u })
	if f.Poster != "" || f.Backdrop != "" {
		// Only the pictures: the download took a while, and the server keeps a picture a person
		// chose meanwhile.
		if err := g.a.Host.SetItem(ctx, f); err != nil {
			retry = true
		}
	}
	if retry {
		_, _ = g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET applied = ?, poster_ref = ?, backdrop_ref = ?, poster_url = ?, backdrop_url = ?, art_tries = art_tries + 1,
			art_wanted = CASE WHEN art_tries >= 6 THEN 0 ELSE 1 END, art_next = ? WHERE item_id = ?`,
			gmListOf(r.Applied), r.PosterRef, r.BackdropRef, r.PosterURL, r.BackdropURL, time.Now().Add(30*time.Minute).Unix(), id)
		return
	}
	_, _ = g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET applied = ?, poster_ref = ?, backdrop_ref = ?, poster_url = ?, backdrop_url = ?, art_wanted = 0, art_tries = 0 WHERE item_id = ?`,
		gmListOf(r.Applied), r.PosterRef, r.BackdropRef, r.PosterURL, r.BackdropURL, id)
}

// ---------------------------------------------------------------- what people ask for

// choose makes one source's game the answer for this game, with the rest asked again about it by
// that name. lock keeps automatic work away from it from then on.
func (g *gameMeta) choose(ctx context.Context, it metaGame, source, id string, lock bool) error {
	set := g.sources(ctx)
	src := set.all[source]
	if src == nil {
		return gamemeta.ErrNotFound
	}
	in, err := src.Fetch(ctx, it.Console, id)
	if err != nil {
		return err
	}
	in.Method, in.Confidence = gamemeta.MethodManual, 100
	if err := g.ensureRow(ctx, it); err != nil {
		return err
	}
	if _, err := g.a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE item_id = ?`, it.ID); err != nil {
		return err
	}
	if err := g.a.saveGameAnswer(ctx, it.ID, source, in); err != nil {
		return err
	}
	if _, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'pending', next_try = 0, tries = 0, locked = ?, query_title = ?, poster_ref = '', backdrop_ref = '' WHERE item_id = ?`,
		boolInt(lock), gameCleanTitle(in.Title), it.ID); err != nil {
		return err
	}
	if err := g.applyAnswers(ctx, it.ID, []gamemeta.Answer{{Source: source, Info: in}}); err != nil {
		return err
	}
	g.Kick()
	return nil
}

// setLocked changes whether a match is locked.
func (g *gameMeta) setLocked(ctx context.Context, it metaGame, on bool) error {
	if err := g.ensureRow(ctx, it); err != nil {
		return err
	}
	_, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET locked = ? WHERE item_id = ?`, boolInt(on), it.ID)
	return err
}

// metaGame is a game as the routes know it: the server's item, with its files.
type metaGame struct {
	ID, Library int64
	Console     string
	Title       string
	Files       []string
}

// ensureRow makes sure a game has a row, with what the server says of it now.
func (g *gameMeta) ensureRow(ctx context.Context, it metaGame) error {
	path := ""
	if len(it.Files) > 0 {
		path = it.Files[0]
	}
	files, _ := json.Marshal(it.Files)
	if _, err := g.a.DB.ExecContext(ctx, `INSERT OR IGNORE INTO wittyplay_game_details(item_id, library_id, console, title, path, files) VALUES(?,?,?,?,?,?)`,
		it.ID, it.Library, it.Console, it.Title, path, string(files)); err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	_, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET library_id = ?, console = ?, path = ?, files = ? WHERE item_id = ?`, it.Library, it.Console, path, string(files), it.ID)
	return err
}

// clearMatch forgets a match someone says is wrong: nothing is filled in from sources until asked
// again, or a match is chosen. The game stays locked so it isn't refound.
func (g *gameMeta) clearMatch(ctx context.Context, it metaGame) error {
	if err := g.ensureRow(ctx, it); err != nil {
		return err
	}
	r, err := g.a.gameRow(ctx, it.ID)
	if err != nil {
		return err
	}
	if _, err := g.a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE item_id = ?`, it.ID); err != nil {
		return err
	}
	f := wittymodule.ItemFields{ID: it.ID}
	title, tags := gameTitleOf(&r)
	for _, field := range []string{"title", "tagline", "overview", "year", "genres", "studio", "rating", "contentRating", "poster", "backdrop", "titles"} {
		if !r.Applied[field] {
			continue
		}
		switch {
		case field == "title":
			f.Title = title
		case field == "tagline" && tags != "":
			f.Tagline = tags
		default:
			f.Clear = append(f.Clear, field)
		}
	}
	if r.Applied["provider"] {
		f.Clear = append(f.Clear, "provider")
	}
	if err := g.a.Host.SetItem(ctx, f); err != nil {
		return err
	}
	_, err = g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'done', locked = 1, applied = '', query_title = '', match_source = '', match_id = '', match_method = '', confidence = 0,
		released = '', developer = '', publisher = '', franchise = '', players = 0, scores = '', ages = '', art = '', credits = '', poster_refs = '', backdrop_refs = '',
		poster_ref = '', backdrop_ref = '', poster_url = '', backdrop_url = '', has_overview = 0, art_wanted = 0 WHERE item_id = ?`, it.ID)
	return err
}

// refresh asks every source about a game again. A locked or chosen match keeps its game: the
// source's own id is asked for, rather than the name being searched again.
func (g *gameMeta) refresh(ctx context.Context, it metaGame) error {
	if err := g.ensureRow(ctx, it); err != nil {
		return err
	}
	var source, id string
	var locked int
	_ = g.a.DB.QueryRowContext(ctx, `SELECT IFNULL(s.source, ''), IFNULL(s.source_id, ''), m.locked FROM wittyplay_game_details m
		LEFT JOIN wittyplay_game_answers s ON s.item_id = m.item_id AND s.method = 'manual' WHERE m.item_id = ?`, it.ID).Scan(&source, &id, &locked)
	if source != "" && id != "" {
		if src := g.sources(ctx).all[source]; src != nil {
			in, err := src.Fetch(ctx, it.Console, id)
			if err != nil {
				return err
			}
			in.Method, in.Confidence = gamemeta.MethodManual, 100
			if err := g.a.saveGameAnswer(ctx, it.ID, source, in); err != nil {
				return err
			}
		}
	}
	if _, err := g.a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE item_id = ? AND method <> 'manual'`, it.ID); err != nil {
		return err
	}
	// Refreshing a file means reading it again too.
	if _, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'pending', next_try = 0, tries = 0, file_size = -1, locked = ? WHERE item_id = ?`, locked, it.ID); err != nil {
		return err
	}
	g.Kick()
	return nil
}

// requeueAll asks again about whatever hasn't answered yet, after the list of sources or their
// keys changed. Sources that have already answered aren't asked again, and order changes are
// merged here and now. Locked games stay as they are.
func (g *gameMeta) requeueAll(ctx context.Context) error {
	_, err := g.a.DB.ExecContext(ctx, `UPDATE wittyplay_game_details SET status = 'pending', next_try = 0, tries = 0 WHERE locked = 0`)
	g.mu.Lock()
	g.paused = map[string]gamemeta.RateLimited{}
	g.mu.Unlock()
	g.Kick()
	return err
}

// refetchAll forgets every answer and starts again. Choices people made stay.
func (g *gameMeta) refetchAll(ctx context.Context) error {
	if _, err := g.a.DB.ExecContext(ctx, `DELETE FROM wittyplay_game_answers WHERE method <> 'manual' AND item_id IN (SELECT item_id FROM wittyplay_game_details WHERE locked = 0)`); err != nil {
		return err
	}
	return g.requeueAll(ctx)
}

// search asks every source in use about a name, for someone choosing a match.
func (g *gameMeta) search(ctx context.Context, console, title string) ([]gamemeta.Candidate, []string) {
	set := g.sources(ctx)
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := []gamemeta.Candidate{}
	var failed []string
	for _, id := range set.on {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					g.log().Error("game search", "cat", "games", "source", id, "err", fmt.Sprint(p))
				}
			}()
			c, err := set.all[id].Search(ctx, console, title)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if !errors.Is(err, gamemeta.ErrNotFound) {
					failed = append(failed, set.all[id].About().Name)
				}
				return
			}
			out = append(out, c...)
		}(id)
	}
	wg.Wait()
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > 40 {
		out = out[:40]
	}
	if art, ok := set.all["libretroart"].(*gamemeta.LibretroArt); ok && slices.Contains(set.on, "libretroart") {
		art.Decorate(ctx, console, out)
	}
	return out, failed
}

var (
	pruneMu      sync.Mutex
	pruneWaiting = map[*App]bool{}
)

// pruneSoon forgets games the server no longer has, a little after the last of a run of events
// that may have taken some away: a scan sends many.
func (a *App) pruneSoon() {
	pruneMu.Lock()
	defer pruneMu.Unlock()
	if pruneWaiting[a] {
		return
	}
	pruneWaiting[a] = true
	time.AfterFunc(30*time.Second, func() {
		pruneMu.Lock()
		delete(pruneWaiting, a)
		pruneMu.Unlock()
		a.pruneGames(context.Background())
	})
}

// pruneGames forgets games the server no longer has, after a scan or a library or item went.
func (a *App) pruneGames(ctx context.Context) {
	ids, err := a.gameIDs(ctx, `SELECT item_id FROM wittyplay_game_details UNION SELECT item_id FROM wittyplay_game_pictures`)
	if err != nil {
		return
	}
	var gone []int64
	for len(ids) > 0 {
		n := min(len(ids), 500)
		chunk := ids[:n]
		ids = ids[n:]
		items, err := a.Host.Items(ctx, hostOn(), hostQueryIDs(chunk))
		if err != nil {
			return // the server can't say: nothing goes
		}
		have := map[int64]bool{}
		for _, it := range items {
			have[it.ID] = true
		}
		for _, id := range chunk {
			if have[id] {
				continue
			}
			// Not listed: only gone if the server says there's no such item.
			if _, err := a.Host.Item(ctx, hostOn(), id); isHostNotFound(err) {
				gone = append(gone, id)
			}
		}
	}
	if len(gone) > 0 {
		_ = a.forgetGames(ctx, gone)
		a.Log.Info("wittyplay: forgot games the server no longer has", "cat", "games", "games", len(gone))
	}
}
