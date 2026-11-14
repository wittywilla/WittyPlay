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

package api

import "github.com/wittywilla/wittyplay/store"

// The tables these routes keep. A person's rows go when the server says the account has gone
// (user.deleted), as its own tables' foreign keys did there.
func init() {
	store.Register(store.Migration{Version: 1, SQL: `
CREATE TABLE IF NOT EXISTS wittyplay_controls (
  user_id INTEGER NOT NULL, console TEXT NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL,
  data TEXT NOT NULL, updated_at INTEGER NOT NULL,
  PRIMARY KEY(user_id, console, kind, key));
CREATE TABLE IF NOT EXISTS wittyplay_profiles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL, name TEXT NOT NULL, kind TEXT NOT NULL, controller TEXT NOT NULL, console TEXT NOT NULL,
  game_id INTEGER NOT NULL, raw INTEGER NOT NULL, active INTEGER NOT NULL,
  data TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS idx_wittyplay_profiles_user ON wittyplay_profiles(user_id);
CREATE TABLE IF NOT EXISTS wittyplay_backgrounds (
  user_id INTEGER PRIMARY KEY,
  version INTEGER NOT NULL, fit TEXT NOT NULL, x INTEGER NOT NULL, y INTEGER NOT NULL, dim INTEGER NOT NULL);`})
}

// Cores and consoles (the cores group): custom cores and their versions, custom consoles, which core
// a game uses, and how each core has played each game. The audit log of cores stays the server's
// (its module audit log shares it): WittyPlay writes to it through the host API.
func init() {
	store.Register(store.Migration{Version: 2, SQL: `
CREATE TABLE IF NOT EXISTS custom_cores (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', sha256 TEXT NOT NULL, size INTEGER NOT NULL DEFAULT 0,
  license TEXT NOT NULL DEFAULT '', license_source TEXT NOT NULL DEFAULT '', non_commercial INTEGER NOT NULL DEFAULT 0,
  consoles TEXT NOT NULL DEFAULT '[]', note TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', source_url TEXT NOT NULL DEFAULT '',
  added_by TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, report TEXT NOT NULL DEFAULT '{}', system_files TEXT NOT NULL DEFAULT '[]');
CREATE TABLE IF NOT EXISTS custom_core_versions (
  core_id TEXT NOT NULL, sha256 TEXT NOT NULL, version TEXT NOT NULL DEFAULT '', added_at INTEGER NOT NULL,
  PRIMARY KEY(core_id, sha256));
CREATE TABLE IF NOT EXISTS custom_consoles (
  id TEXT PRIMARY KEY, data TEXT NOT NULL, aliases TEXT NOT NULL DEFAULT '[]', cores TEXT NOT NULL DEFAULT '[]',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS game_core_choice (item_id INTEGER PRIMARY KEY, core_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS game_core_plays (
  core_id TEXT NOT NULL, item_id INTEGER NOT NULL,
  starts INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0, seconds REAL NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '', last_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(core_id, item_id));`})
}

// perUser are the tables with a person's own rows, which go when the account goes.
var perUser = []string{"wittyplay_controls", "wittyplay_profiles", "wittyplay_backgrounds"}
