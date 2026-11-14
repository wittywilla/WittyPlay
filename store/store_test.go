// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package store

import (
	"context"
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
