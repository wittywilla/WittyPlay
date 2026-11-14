// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMigrateAndImport(t *testing.T) {
	Register(Migration{Version: 9001, SQL: `CREATE TABLE IF NOT EXISTS wittyplay_controls (user_id INTEGER NOT NULL, console TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, data TEXT NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(user_id, console, kind, key))`})
	dir := t.TempDir()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if d.Version(ctx) < 9001 {
		t.Fatalf("version %d", d.Version(ctx))
	}
	server := []string{
		`{"user_id": 3, "console": "snes", "kind": "touch", "key": "phone", "data": "{}", "updated_at": 1794618000}`,
		`{"user_id": 4, "console": "nes", "kind": "pad", "key": "x", "data": "{\"a\":1}", "updated_at": 2}`,
	}
	reads := 0
	from := func(_ context.Context, table string, after int64, limit int) ([]json.RawMessage, int64, error) {
		reads++
		if table != "wittyplay_controls" || after > 0 {
			return nil, after, nil
		}
		var out []json.RawMessage
		for _, s := range server {
			out = append(out, json.RawMessage(s))
		}
		return out, 2, nil
	}
	n, err := d.Import(ctx, from, "wittyplay_controls")
	if err != nil || n != 2 {
		t.Fatalf("import: %d %v", n, err)
	}
	var user, at int64
	if err := d.QueryRow(`SELECT user_id, updated_at FROM wittyplay_controls WHERE console = 'snes'`).Scan(&user, &at); err != nil || user != 3 || at != 1794618000 {
		t.Fatalf("row: %d %d %v", user, at, err)
	}
	// Once only: a second run reads nothing.
	before := reads
	if n, err := d.Import(ctx, from, "wittyplay_controls"); n != 0 || err != nil || reads != before {
		t.Fatalf("again: %d %v, read %d more", n, err, reads-before)
	}
	d.Close()
	// Reopened: the schema isn't run again.
	d2, err := Open(dir)
	if err != nil || d2.Version(ctx) < 9001 {
		t.Fatalf("reopen: %v", err)
	}
	d2.Close()
	if _, err := Open(""); err == nil {
		t.Fatal("no folder was taken")
	}
}
