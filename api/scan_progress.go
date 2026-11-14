// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/gamescan"
)

// How a scan of a games library tells the server it's moving, and what kind of scan it is.
//
// Progress: while WittyPlay works on a scan call (a batch of files, the last call that puts the
// files together into games, a batch to describe), it reports how far it has got as a host task
// named scan-<library>. The server takes that as the scan's progress, shows it, and doesn't count
// the scan as stuck while it keeps moving. A report goes only when the work has moved, at most
// once a second for a library, and never more than one at a time: a server that's slow to
// answer one doesn't hold the scan up, and one that doesn't know these reports drops them.
//
// Kind: the server's host libraries say, for a library with a scan running or waiting to carry
// on, {"scan": {"mode": "changes" | "full", "since"}}. A full rescan works out every file again
// from the start, once for each run (since says which), however much of it was known.

// scanReportEvery is the most often one library's scan reports.
const scanReportEvery = time.Second

type scanReporter struct {
	mu   sync.Mutex
	last time.Time
	busy bool
}

var scanReporters = struct {
	sync.Mutex
	m map[int64]*scanReporter
}{m: map[int64]*scanReporter{}}

// scanTaskName is the host task a library's scan reports as.
func scanTaskName(library int64) string { return "scan-" + strconv.FormatInt(library, 10) }

// reportScan returns what to tell how far a scan of this library has got: done of total, in the
// call's own terms. nil without a server to tell.
func (a *App) reportScan(library int64) func(done, total int) {
	if a == nil || a.Host == nil {
		return nil
	}
	scanReporters.Lock()
	r := scanReporters.m[library]
	if r == nil {
		r = &scanReporter{}
		scanReporters.m[library] = r
	}
	scanReporters.Unlock()
	c := a.Host
	name := scanTaskName(library)
	return func(done, total int) {
		r.mu.Lock()
		if r.busy || time.Since(r.last) < scanReportEvery {
			r.mu.Unlock()
			return
		}
		r.busy, r.last = true, time.Now()
		r.mu.Unlock()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.Task(ctx, name, done, total, nil) // only progress: nothing to do if it's lost
			r.mu.Lock()
			r.busy = false
			r.mu.Unlock()
		}()
	}
}

// libraryScan is the scan the server says a library has running: its mode and when the run
// began. ok is false when it couldn't say.
func (a *App) libraryScan(ctx context.Context, library int64) (mode string, since int64, ok bool) {
	if a == nil || a.Host == nil {
		return "", 0, false
	}
	var out struct {
		Libraries []struct {
			ID   int64 `json:"id"`
			Scan *struct {
				Mode  string `json:"mode"`
				Since int64  `json:"since"`
			} `json:"scan"`
		} `json:"libraries"`
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if a.Host.Do(ctx, host.On{}, "GET", "/libraries", nil, &out) != nil {
		return "", 0, false
	}
	for _, l := range out.Libraries {
		if l.ID == library && l.Scan != nil {
			return l.Scan.Mode, l.Scan.Since, true
		}
	}
	return "", 0, true
}

// rereadIfFull makes a full rescan work out every file again, once for each run: a run that's
// carried on after a restart keeps what it had already worked out.
func (a *App) rereadIfFull(ctx context.Context, library int64) {
	if a.DB == nil {
		return
	}
	mode, since, ok := a.libraryScan(ctx, library)
	if !ok || mode != "full" || since <= 0 {
		return
	}
	key := "gamescan.full." + strconv.FormatInt(library, 10)
	run := strconv.FormatInt(since, 10)
	if was, _ := a.metaOf(ctx, key); was == run {
		return
	}
	if err := gamescan.RereadAll(ctx, a.DB.DB, library); err != nil {
		a.Log.Warn("setting up a full rescan", "cat", "games", "library", library, "err", err)
		return
	}
	_ = a.DB.SetMeta(ctx, key, run)
}
