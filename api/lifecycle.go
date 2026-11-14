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
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
)

// Lifecycle answers the server's lifecycle calls (module API 5). Installing or upgrading to a
// version that keeps data of its own takes over what the server kept before, once: its tables'
// rows, ids and all, and the pictures behind controller screens. Nothing of the server's is
// changed; a version of WittyPlay before this one still finds it all there.
func Lifecycle(ctx context.Context, call string, args wittymodule.LifecycleArgs) error {
	switch call {
	case wittymodule.CallInstall, wittymodule.CallUpgrade:
		a, err := Get()
		if err != nil {
			return err
		}
		return a.takeOver(ctx)
	case wittymodule.CallUninstall:
		if !args.KeepData {
			if a, err := Get(); err == nil {
				a.DB.Close()
				return os.RemoveAll(a.Dir)
			}
		}
	}
	return nil
}

var backgroundName = regexp.MustCompile(`^u([0-9]+)\.jpg$`)

func (a *App) takeOver(ctx context.Context) error {
	for _, t := range Imported {
		n, err := a.DB.Import(ctx, a.Host.Legacy, t)
		if err != nil {
			return err
		}
		if n > 0 {
			a.Log.Info("wittyplay: took over the server's rows", "cat", "games", "table", t, "rows", n)
		}
	}
	if a.DB.Meta(ctx, "import.backgrounds") == "done" {
		return nil
	}
	names, err := a.Host.LegacyFiles(ctx, "gaming/backgrounds")
	if err != nil && !host.IsNotFound(err) {
		return fmt.Errorf("listing the server's controller backgrounds: %w", err)
	}
	for _, name := range names {
		if !backgroundName.MatchString(name) {
			continue
		}
		dst := filepath.Join(a.Dir, "backgrounds", name)
		if _, err := os.Stat(dst); err == nil {
			continue // one made here since wins
		}
		b, err := a.Host.LegacyFile(ctx, "gaming/backgrounds/"+name)
		if err != nil {
			return fmt.Errorf("reading the server's %s: %w", name, err)
		}
		if err := writeFileAtomic(dst, b); err != nil {
			return err
		}
	}
	return a.DB.SetMeta(ctx, "import.backgrounds", "done")
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
