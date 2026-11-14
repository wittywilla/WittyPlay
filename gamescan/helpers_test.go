// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamescan

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/store"
)

// walk lists a folder as the server's scan walk sends it: every regular file, in the order
// filepath.WalkDir finds it, leaving out names that start with "." or "$" and following no
// linked folder.
func walk(root string) []wittymodule.ScanFile {
	var out []wittymodule.ScanFile
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p != root && (strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, wittymodule.ScanFile{Path: p, Root: root, Size: st.Size(), MTime: st.ModTime().Unix()})
		return nil
	})
	return out
}

// discover is what a scan of one root finds, the files sent in one batch.
func (ix *archiveIndex) discover(root, console string) ([]candidate, error) {
	return ix.discoverCtx(context.Background(), root, console)
}

func (ix *archiveIndex) discoverCtx(ctx context.Context, root, console string) ([]candidate, error) {
	freshConsoles()
	if err := ix.add(ctx, console, walk(root)); err != nil {
		return nil, err
	}
	return ix.games(filepath.Clean(root), console), nil
}

// discoverGames lists the games under a root, remembering nothing.
func discoverGames(root, console string) ([]candidate, error) {
	ix := newArchiveIndex(nil, 0)
	defer ix.close()
	return ix.discover(root, console)
}

// skippedGameFiles counts, by extension, game-like files a library can't use. An
// archive that can't be read at all counts by why instead (see gamearchive.Error).
func skippedGameFiles(root, console string) map[string]int {
	ix := newArchiveIndex(nil, 0)
	defer ix.close()
	_, _ = ix.discover(root, console)
	out := map[string]int{}
	for _, n := range ix.notes {
		if n.kind == reviewNeeds || n.kind == reviewBlocked {
			key := strings.ToLower(filepath.Ext(n.path))
			if code := problemCode(n.reason); code != "" {
				key = code
			}
			out[key]++
		}
	}
	return out
}

// problemCode reads back which archive problem a note is about.
func problemCode(reason string) string {
	for _, code := range []string{gamearchive.ErrEncrypted.Code, gamearchive.ErrDamaged.Code, gamearchive.ErrTooBig.Code, gamearchive.ErrTooMany.Code, gamearchive.ErrUnsafe.Code} {
		if reason == archiveProblem(code) {
			return code
		}
	}
	return ""
}

// testDB is WittyPlay's database, in a folder of the test's own.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d.DB
}

// scanLibrary runs a whole scan of a library through Begin, Add and Finish, the files sent in
// batches as the server sends them.
func scanLibrary(t *testing.T, db *sql.DB, lib wittymodule.ScanLibrary) wittymodule.ScanIdentified {
	t.Helper()
	ctx := context.Background()
	s := Begin(db, "test", lib)
	var files []wittymodule.ScanFile
	for _, root := range lib.Paths {
		files = append(files, walk(root)...)
	}
	for len(files) > 0 {
		n := min(len(files), 3) // small batches: a sheet can come after what it names
		if err := s.Add(ctx, files[:n], 0); err != nil {
			t.Fatal(err)
		}
		files = files[n:]
	}
	out, err := s.Finish(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// itemNames are a scan's items as "relative path=console", sorted.
func itemNames(items []wittymodule.ScanItem, root string) []string {
	var out []string
	for _, it := range items {
		rel, _ := filepath.Rel(root, it.Files[0])
		out = append(out, filepath.ToSlash(rel)+"="+it.Facets["console"])
	}
	sort.Strings(out)
	return out
}
