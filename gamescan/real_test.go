// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay/store"
)

// Not a test of the code so much as of a real folder: run by hand with WITTYPLAY_GAMES_FOLDER
// set, to see what a scan of it finds and how long it takes. It only reads that folder. Its
// database is WITTYPLAY_GAMES_DB (a folder) or a temporary one, and it prints counts, never a
// name. WITTYPLAY_GAMES_OUT, when set, is a file it writes every item's path and console to.
func TestRealGamesFolder(t *testing.T) {
	root := os.Getenv("WITTYPLAY_GAMES_FOLDER")
	if root == "" {
		t.Skip("set WITTYPLAY_GAMES_FOLDER to run this")
	}
	dir := os.Getenv("WITTYPLAY_GAMES_DB")
	if dir == "" {
		dir = t.TempDir()
	}
	d, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	lib := wittymodule.ScanLibrary{ID: 1, Kind: "games", Facet: "auto", Paths: []string{root}}
	for round := 1; round <= 2; round++ {
		start := time.Now()
		files := walk(root)
		s := Begin(d.DB, fmt.Sprint("real-", round), lib)
		for len(files) > 0 {
			n := min(len(files), 256)
			if err := s.Add(context.Background(), files[:n], 0); err != nil {
				t.Fatal(err)
			}
			files = files[n:]
		}
		out, err := s.Finish(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		versions := 0
		var lines []string
		for _, it := range out.Items {
			counts[it.Facets["console"]]++
			if it.VersionOf != "" {
				versions++
			}
			lines = append(lines, it.Files[0]+"\t"+it.Facets["console"]+"\t"+it.VersionOf)
		}
		t.Logf("scan %d: %d items (%d of them versions under another) in %v; %s", round, len(out.Items), versions, time.Since(start).Round(time.Millisecond), out.Result)
		var keys []string
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t.Logf("  %-8s %d", k, counts[k])
		}
		review, _ := ReviewCounts(context.Background(), d.DB, lib.ID)
		t.Logf("  review: %v", review)
		if p := os.Getenv("WITTYPLAY_GAMES_OUT"); p != "" && round == 1 {
			sort.Strings(lines)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
