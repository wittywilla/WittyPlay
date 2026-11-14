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

// The tables these routes keep, as the server had them (its migrations 16 and 19), so its rows
// come across as they are. A person's rows go when the server says the account has gone
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

// Imported are the server's tables these routes took over (data.import "server-games-v1").
var Imported = []string{"wittyplay_controls", "wittyplay_profiles", "wittyplay_backgrounds"}
