// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gamescan"
)

// scan.choices and scan.identify: which consoles a games library can be held to, and what the
// files of one are (package gamescan). A scan's files come in batches, each judged as it comes;
// its last call (Done) puts files together into games and answers every one. Until then what a
// scan has found is kept here, in memory, by its id: one scan of a library at a time, and none
// the server has stopped hearing from for an hour.

func init() {
	scanChoices = identifyChoices
	scanIdentify = identifyFiles
}

// scanForgetAfter is how long a scan the server says nothing more about is kept.
const scanForgetAfter = time.Hour

var scans = struct {
	sync.Mutex
	m map[string]*gamescan.Scan
}{m: map[string]*gamescan.Scan{}}

// errNotGames: a library of a kind WittyPlay doesn't scan.
var errNotGames = errors.New("WittyPlay scans games libraries only")

func identifyChoices(_ context.Context, _ *App, args wittymodule.ScanChoicesArgs) (wittymodule.ScanChoices, error) {
	if args.LibraryKind != "games" {
		return wittymodule.ScanChoices{}, errNotGames
	}
	out := wittymodule.ScanChoices{Facet: "console", Values: []wittymodule.ScanChoice{}}
	for _, c := range wittyplay.All() {
		if c.Hidden && !args.Testing {
			continue
		}
		out.Values = append(out.Values, wittymodule.ScanChoice{ID: c.ID, Name: c.Name, Short: c.Short})
	}
	return out, nil
}

func identifyFiles(ctx context.Context, a *App, args wittymodule.ScanIdentifyArgs) (wittymodule.ScanIdentified, error) {
	if args.Library.Kind != "games" {
		return wittymodule.ScanIdentified{}, errNotGames
	}
	if args.Scan == "" {
		return wittymodule.ScanIdentified{}, errors.New("a scan has an id")
	}
	s, fresh := a.scanOf(ctx, args)
	if fresh && args.Done && args.Total > 0 {
		// The files went to a WittyPlay that has since restarted: answering now would say the
		// library holds nothing.
		a.dropScan(args.Scan)
		return wittymodule.ScanIdentified{}, errors.New("WittyPlay restarted during this scan; scan the library again")
	}
	if len(args.Files) > 0 {
		if err := s.Add(ctx, args.Files, args.Total); err != nil {
			if ctx.Err() != nil {
				a.dropScan(args.Scan)
			}
			return wittymodule.ScanIdentified{}, err
		}
	}
	if !args.Done {
		return wittymodule.ScanIdentified{}, nil
	}
	defer a.dropScan(args.Scan)
	if got, total, _ := s.Progress(); args.Total > 0 && got < total {
		return wittymodule.ScanIdentified{}, errors.New("not every file of this scan reached WittyPlay; scan the library again")
	}
	return s.Finish(ctx, args.Offline)
}

// scanOf is the scan a call belongs to, begun if it's the first WittyPlay has heard of it. A new
// scan of a library ends any other of the same library.
func (a *App) scanOf(ctx context.Context, args wittymodule.ScanIdentifyArgs) (*gamescan.Scan, bool) {
	scans.Lock()
	defer scans.Unlock()
	for id, s := range scans.m {
		if id != args.Scan && (s.Library.ID == args.Library.ID || time.Since(s.LastHeard()) > scanForgetAfter) {
			delete(scans.m, id)
			go s.Close()
		}
	}
	if s := scans.m[args.Scan]; s != nil {
		return s, false
	}
	a.useRegionOrder(ctx, args.Library.ID)
	a.rereadIfFull(ctx, args.Library.ID)
	s := gamescan.Begin(a.sqlDB(), args.Scan, args.Library)
	s.Report = a.reportScan(args.Library.ID)
	scans.m[args.Scan] = s
	return s, true
}

func (a *App) dropScan(id string) {
	scans.Lock()
	s := scans.m[id]
	delete(scans.m, id)
	scans.Unlock()
	if s != nil {
		s.Close()
	}
}

// scanning is the scan of a library going on now, or nil.
func scanning(library int64) *gamescan.Scan {
	scans.Lock()
	defer scans.Unlock()
	for _, s := range scans.m {
		if s.Library.ID == library && time.Since(s.LastHeard()) <= scanForgetAfter {
			return s
		}
	}
	return nil
}

// useRegionOrder puts the owner's order of regions to use for a scan. When it's changed since the
// library's last scan, which file of an archive plays and which version leads a game are worked
// out again.
func (a *App) useRegionOrder(ctx context.Context, library int64) {
	order := a.configText(ctx, "gamingRegionOrder", "")
	gamearchive.SetRegionOrder(order)
	if a.DB == nil {
		return
	}
	key := "gamescan.regions." + strconv.FormatInt(library, 10)
	if was, ok := a.metaOf(ctx, key); ok && was != order {
		if err := gamescan.RegionsChanged(ctx, a.DB.DB, library); err != nil {
			a.Log.Warn("putting a new order of regions to use", "cat", "games", "library", library, "err", err)
			return
		}
	}
	_ = a.DB.SetMeta(ctx, key, order)
}

// metaOf reads one of the store's own notes, and whether there is one.
func (a *App) metaOf(ctx context.Context, key string) (string, bool) {
	var v string
	err := a.DB.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&v)
	return v, err == nil
}

// sqlDB is WittyPlay's database, or nil when it has none (a test's App).
func (a *App) sqlDB() *sql.DB {
	if a == nil || a.DB == nil {
		return nil
	}
	return a.DB.DB
}

// noteDescribed remembers the server's id for each game it asks about: the game routes find a
// game's other versions by it.
func noteDescribed(ctx context.Context, a *App, args wittymodule.ScanDescribeArgs) {
	db := a.sqlDB()
	if db == nil {
		return
	}
	for _, it := range args.Items {
		if len(it.Files) > 0 {
			_ = gamescan.NoteItem(ctx, db, args.Library.ID, it.Files[0], it.ID)
		}
	}
}
