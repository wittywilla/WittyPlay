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

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
)

// Lifecycle answers the server's lifecycle calls (module API 5): WittyPlay's data starts with it,
// and goes when it is taken off unless the administrator keeps it.
func Lifecycle(ctx context.Context, call string, args wittymodule.LifecycleArgs) error {
	if call == wittymodule.CallUninstall && !args.KeepData {
		if a, err := Get(); err == nil {
			a.DB.Close()
			return os.RemoveAll(a.Dir)
		}
	}
	return nil
}

var watchOnce sync.Once

// Watch follows the server's events while the service runs: an account that goes takes its
// layouts, profiles and picture with it, as the server's own tables did.
func Watch() {
	watchOnce.Do(func() {
		go func() {
			var after int64
			for {
				a, err := Get()
				if err != nil {
					time.Sleep(30 * time.Second)
					continue
				}
				evs, next, err := a.Host.Events(context.Background(), after, 50*time.Second)
				if err != nil {
					time.Sleep(10 * time.Second)
					continue
				}
				after = next
				for _, e := range evs {
					if e.Type != "user.deleted" {
						continue
					}
					var d struct {
						User int64 `json:"user"`
					}
					if json.Unmarshal(e.Data, &d) == nil && d.User > 0 {
						a.forget(d.User)
					}
				}
			}
		}()
	})
}

func (a *App) forget(userID int64) {
	ctx := context.Background()
	for _, t := range perUser {
		_, _ = a.DB.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %q WHERE user_id = ?`, t), userID)
	}
	_ = os.Remove(a.backgroundPath(userID))
	a.Log.Info("wittyplay: an account's controller data went with it", "cat", "games", "user", strconv.FormatInt(userID, 10))
}
