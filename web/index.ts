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

// WittyPlay's web half: the game player, controllers and touch editor. The host
// app provides the API through PlayHost; the server half is the Go module of
// the same name.
export { default, ControllersPage } from './Player'
export type { PlayHost, PrepareStatus, SaveSlot, SessionInfo } from './Player'
export type { ProfileHost } from './Studio'
export type { Background, ControlsData, SaveControl } from './consoles'
export type { ControllerData, Profile } from './mapping'
export type { SettingsHost } from './settings'
export { GameSettingsScreen } from './Screen'
