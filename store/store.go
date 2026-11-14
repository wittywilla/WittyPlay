// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyPlay - Console games streamed to a browser or an app, with every controller and a touch layout editor.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

// Package store is WittyPlay's own database (module API 5): a SQLite file in the module's own
// folder (WITTYMODULE_DATA), with migrations WittyPlay owns, and the one-off import of what the
// server kept for WittyPlay before (host.Client.Legacy). Every id is kept as it was, because the
// apps keep item ids, user ids and save slots.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// File is the database's name in the module's folder.
const File = "wittyplay.db"

// Migration is one step of the schema, applied once, in order of Version.
type Migration struct {
	Version int
	SQL     string
}

var (
	regMu      sync.Mutex
	migrations []Migration
)

// Register adds migrations; each part of WittyPlay that keeps data registers its own.
func Register(ms ...Migration) {
	regMu.Lock()
	defer regMu.Unlock()
	migrations = append(migrations, ms...)
}

// DB is WittyPlay's database.
type DB struct {
	*sql.DB
	path string
}

// Open opens (or makes) the database in dir and brings its schema up to date.
func Open(dir string) (*DB, error) {
	if dir == "" {
		return nil, errors.New("store: no folder of its own (the data permission)")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, File)
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	d := &DB{DB: db, path: path}
	if err := d.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return d, nil
}

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	regMu.Lock()
	ms := append([]Migration(nil), migrations...)
	regMu.Unlock()
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	have := d.Version(ctx)
	for _, m := range ms {
		if m.Version <= have {
			continue
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", m.Version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO store_meta (key, value) VALUES ('version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, fmt.Sprint(m.Version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		have = m.Version
	}
	return nil
}

// Version is the schema's version now.
func (d *DB) Version(ctx context.Context) int {
	var v int
	_ = d.QueryRowContext(ctx, `SELECT CAST(value AS INTEGER) FROM store_meta WHERE key = 'version'`).Scan(&v)
	return v
}

// Meta reads and writes a little of the store's own bookkeeping.
func (d *DB) Meta(ctx context.Context, key string) string {
	var v string
	_ = d.QueryRowContext(ctx, `SELECT value FROM store_meta WHERE key = ?`, key).Scan(&v)
	return v
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO store_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Legacy is where the server's own rows come from: host.Client.Legacy.
type Legacy func(ctx context.Context, table string, after int64, limit int) ([]json.RawMessage, int64, error)

// Import copies one of the server's tables into the table of the same name and columns here,
// once: rows keep every value as it was, ids included, and a row already here is kept (a
// second run, or one after a crash, changes nothing). It returns how many rows it read.
func (d *DB) Import(ctx context.Context, from Legacy, table string) (int, error) {
	key := "import." + table
	if d.Meta(ctx, key) == "done" {
		return 0, nil
	}
	cols, err := d.columns(ctx, table)
	if err != nil {
		return 0, err
	}
	after, n := int64(0), 0
	for {
		rows, next, err := from(ctx, table, after, 500)
		if err != nil {
			return n, fmt.Errorf("store: reading the server's %s: %w", table, err)
		}
		if len(rows) == 0 {
			break
		}
		tx, err := d.BeginTx(ctx, nil)
		if err != nil {
			return n, err
		}
		for _, raw := range rows {
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				tx.Rollback()
				return n, err
			}
			var names, marks []string
			var args []any
			for _, c := range cols {
				if v, ok := row[c]; ok {
					names = append(names, `"`+c+`"`)
					marks = append(marks, "?")
					if f, ok := v.(float64); ok && f == float64(int64(f)) {
						v = int64(f) // JSON numbers come as floats; ids and times are whole
					}
					args = append(args, v)
				}
			}
			if len(names) == 0 {
				continue
			}
			q := fmt.Sprintf(`INSERT OR IGNORE INTO %q (%s) VALUES (%s)`, table, strings.Join(names, ", "), strings.Join(marks, ", "))
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				tx.Rollback()
				return n, fmt.Errorf("store: importing %s: %w", table, err)
			}
			n++
		}
		if err := tx.Commit(); err != nil {
			return n, err
		}
		if next <= after {
			break
		}
		after = next
	}
	return n, d.SetMeta(ctx, key, "done")
}

func (d *DB) columns(ctx context.Context, table string) ([]string, error) {
	rs, err := d.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%q)`, table))
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rs.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("store: no table %s here", table)
	}
	return out, rs.Err()
}
