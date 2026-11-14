// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"io"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/gameid"
	"github.com/wittywilla/wittyplay/gamemeta"
	"github.com/wittywilla/wittyplay/gamescan"
)

// What identification (gamescan) and finding out about games (gamemeta, the game details
// worker) tell each other: the two halves of what the server's games scan used to do in one.

func init() {
	// An archive's entries that count are plain dumps and prereleases, never hacks or bad dumps.
	gamemeta.Played = func(name string) bool { return gameid.QualityOf(name) <= gameid.QualityPrerelease }
	gamemeta.SniffSerial = func(r io.ReaderAt, size int64) (string, string) {
		f := gameid.Sniff(r, size)
		return f.Console, f.Serial
	}
	gameDetailsExtra = func(ctx context.Context, a *App, id int64, d *gameDetails) {
		db := a.sqlDB()
		l, err := gamescan.ListedItem(ctx, db, id)
		if err != nil {
			return
		}
		if len(l.Discs) > 1 {
			d.Discs = l.Discs
		}
		if det, ok := gamescan.Detection(ctx, db, l.Library, l.Path); ok {
			d.DetectedBy, d.DetectSure, d.DetectWhy = det.How, det.Sure, det.Proof
			d.Problem, d.ProblemText = det.Problem, det.ProblemText
			if d.Serial == "" {
				d.Serial = det.Serial
			}
		}
		if e := gamescan.Entry(ctx, db, l.Library, l.Path); e != "" {
			d.Entry = e
		}
	}
	gameLookupStatus = func(ctx context.Context, a *App) any { return a.gameWorker().status(ctx) }
	onEntryPinned = func(ctx context.Context, a *App, it *service.Item) {
		noteIdentified(ctx, a, it.Library, it.ID, gamePath(it))
		a.gameWorker().Kick()
	}
	// Before a game is described, what identification read in its file goes with it.
	describe := scanDescribe
	scanDescribe = func(ctx context.Context, a *App, args wittymodule.ScanDescribeArgs) (wittymodule.ScanDescribed, error) {
		for _, it := range args.Items {
			if len(it.Files) > 0 {
				noteIdentified(ctx, a, args.Library.ID, it.ID, it.Files[0])
			}
		}
		return describe(ctx, a, args)
	}
}

// noteIdentified hands what the scan read in a game's file to the game details.
func noteIdentified(ctx context.Context, a *App, library, item int64, p string) {
	db := a.sqlDB()
	if db == nil || item <= 0 {
		return
	}
	det, _ := gamescan.Detection(ctx, db, library, p)
	serial := gamescan.Serial(ctx, db, library, p)
	if serial == "" {
		serial = det.Serial
	}
	_ = a.noteGameFile(ctx, item, serial, det.Title, det.Name, gamescan.Entry(ctx, db, library, p))
}
