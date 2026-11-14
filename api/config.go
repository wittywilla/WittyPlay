// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/wittywilla/wittyplay/store"
)

// WittyPlay's own server-wide settings (what were the server's gaming* settings): a key and a
// JSON value each.
func init() {
	store.Register(store.Migration{Version: 4, SQL: `
CREATE TABLE IF NOT EXISTS wittyplay_config (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL);`})
}

// Config reads a setting into v; false when it isn't set (v is left as it was).
func (a *App) Config(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := a.DB.QueryRowContext(ctx, `SELECT value FROM wittyplay_config WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetConfig keeps a setting; nil removes it.
func (a *App) SetConfig(ctx context.Context, key string, v any) error {
	if v == nil {
		_, err := a.DB.ExecContext(ctx, `DELETE FROM wittyplay_config WHERE key = ?`, key)
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = a.DB.ExecContext(ctx, `INSERT INTO wittyplay_config(key, value, updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(b), time.Now().Unix())
	return err
}
