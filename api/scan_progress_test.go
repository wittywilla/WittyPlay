// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/service"
)

// A scan says how far it has got as the host task scan-<library>, at most once a second.
func TestScanReportsProgress(t *testing.T) {
	root := gameTree(t)
	lib := wittymodule.ScanLibrary{ID: 7, Kind: "games", Facet: "auto", Paths: []string{root}}
	gh := &gamesHost{lib: lib, items: map[int64]service.Item{}}
	testApp(t, gh.client(t))
	identify(t, ScanHooks(), "progress", lib)
	deadline := time.Now().Add(5 * time.Second)
	for {
		gh.reportsMu.Lock()
		n := len(gh.reports)
		gh.reportsMu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the scan never said how far it had got")
		}
		time.Sleep(20 * time.Millisecond)
	}
	gh.reportsMu.Lock()
	defer gh.reportsMu.Unlock()
	if len(gh.reports) > 2 {
		t.Fatalf("a scan of a moment reported more than once a second: %v", gh.reports)
	}
}

// A full rescan works out every file again, once for each run; a scan for changes believes what
// it knew.
func TestFullRescanJudgesAgain(t *testing.T) {
	root := gameTree(t)
	lib := wittymodule.ScanLibrary{ID: 8, Kind: "games", Facet: "auto", Paths: []string{root}}
	gh := &gamesHost{lib: lib, items: map[int64]service.Item{}}
	a, _ := testApp(t, gh.client(t))
	hooks := ScanHooks()
	ctx := context.Background()
	identify(t, hooks, "first", lib)
	stale := func() {
		t.Helper()
		if _, err := a.DB.ExecContext(ctx, `UPDATE game_files SET verdict = json_set(verdict, '$.proof', 'stale') WHERE library = ?`, lib.ID); err != nil {
			t.Fatal(err)
		}
	}
	staleRows := func() int {
		t.Helper()
		var n int
		if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_files WHERE library = ? AND json_extract(verdict, '$.proof') = 'stale'`, lib.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	stale()
	all := staleRows()
	if all == 0 {
		t.Fatal("the first scan kept no verdicts")
	}
	identify(t, hooks, "changes", lib)
	if staleRows() != all {
		t.Fatal("a scan for changes worked unchanged files out again")
	}
	gh.scan.Store(&map[string]any{"mode": "full", "since": 1000})
	identify(t, hooks, "full", lib)
	if n := staleRows(); n != 0 {
		t.Fatalf("a full rescan believed %d old verdicts", n)
	}
	// The same run carried on (after a restart): what it already worked out stands.
	stale()
	identify(t, hooks, "full-again", lib)
	if staleRows() != all {
		t.Fatal("a full rescan that carried on started again from scratch")
	}
	// The next full rescan is a run of its own.
	gh.scan.Store(&map[string]any{"mode": "full", "since": 2000})
	identify(t, hooks, "full-next", lib)
	if staleRows() != 0 {
		t.Fatal("a second full rescan believed the first one's verdicts")
	}
}
