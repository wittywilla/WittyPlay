// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay/gamemeta"
	"github.com/wittywilla/wittyplay/ui"
)

// Refresh game details looks every game of a library up again from the start: every source is
// asked again, their pictures are wanted again, and what people typed or chose stays.
func TestRefreshLibraryDetails(t *testing.T) {
	local := &fakeSource{id: "libretro", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "lr-" + q.Title, Title: q.Title, Method: gamemeta.MethodChecksum, Confidence: 100,
			Art: []gamemeta.ArtRef{{Ref: "logo-" + q.Title, Kind: "logo"}}}, nil
	}}
	online := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "9", Title: q.Title, Method: gamemeta.MethodName, Confidence: 95, Developer: "Maker", Native: "ゲーム", English: q.Title + " (English)"}, nil
	}}
	f := newGameFixture(t, local, online)
	f.game(t, 7, "Mega Game (Japan).sfc")
	f.game(t, 8, "Other Game (Japan).sfc")
	f.describe(t, []string{"studio"}, 7)
	f.describe(t, nil, 8)
	f.work(t)
	if online.asked.Load() != 2 {
		t.Fatalf("online asked %d times", online.asked.Load())
	}
	// Not for anyone but an administrator, and not for a library with no games.
	if w := call(t, f.h, "POST", "/libraries/1/details/refresh", 3, nil); w.Code != 403 {
		t.Errorf("a viewer refreshed: %d", w.Code)
	}
	if w := callAdmin(t, f.h, "POST", "/libraries/99/details/refresh", nil); w.Code != 404 {
		t.Errorf("no such library: %d", w.Code)
	}
	w := callAdmin(t, f.h, "POST", "/libraries/1/details/refresh", nil)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"games":2`) {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	var st refreshState
	_ = json.Unmarshal(callAdmin(t, f.h, "GET", "/libraries/1/details/refresh", nil).Body.Bytes(), &st)
	if st.Games != 2 || st.Waiting != 2 || !st.Running || st.Since == 0 {
		t.Errorf("while waiting: %+v", st)
	}
	rows, err := f.a.refreshRows(f.ctx, 1, 0)
	if err != nil || len(rows) != 1 || rows[0].Action.Op != ui.CommandLibraryRefresh || rows[0].Action.Item != "1" || rows[0].Trailing != "Looking up 0 of 2" {
		t.Errorf("rows while waiting: %+v %v", rows, err)
	}
	f.work(t)
	if online.asked.Load() != 4 {
		t.Errorf("online asked %d times after refreshing", online.asked.Load())
	}
	_ = json.Unmarshal(callAdmin(t, f.h, "GET", "/libraries/1/details/refresh", nil).Body.Bytes(), &st)
	if st.Waiting != 0 || st.Native != 2 || st.English != 2 || st.Logos != 2 {
		t.Errorf("after: %+v", st)
	}
	// The work showed as WittyPlay's task, by a name the server takes and under its title.
	f.host.titlesMu.Lock()
	title := f.host.titles[gameMetaTaskName]
	f.host.titlesMu.Unlock()
	if title != gameMetaTaskTitle {
		t.Errorf("the task's title: %q", title)
	}
	// The typed studio stays the person's.
	if set := f.host.fields(7); set.Studio != "" || set.Titles == nil || set.Titles.Native != "ゲーム" {
		t.Errorf("game 7: %+v", set)
	}
	if set := f.host.fields(8); set.Studio != "Maker" {
		t.Errorf("game 8: %+v", set)
	}
	// One game, and its row on a game's page.
	if rows, _ := f.a.refreshRows(f.ctx, 0, 7); len(rows) != 1 || rows[0].Action.Op != ui.CommandGameRefresh || rows[0].Action.Item != "7" {
		t.Errorf("a game's row: %+v", rows)
	}
	if rows, _ := f.a.refreshRows(f.ctx, 0, 12345); len(rows) != 0 {
		t.Errorf("not a game: %+v", rows)
	}
	if w := callAdmin(t, f.h, "POST", "/games/7/metadata/refresh", nil); w.Code != 202 {
		t.Fatalf("one game: %d %s", w.Code, w.Body)
	}
	if rows, _ := f.a.refreshRows(f.ctx, 0, 7); len(rows) != 1 || rows[0].Trailing != "Looking it up" {
		t.Errorf("a game's row while looking: %+v", rows)
	}
}

// The opt-in to console photos is a switch on Settings and the first-run step, off by default.
func TestConsolePhotoRows(t *testing.T) {
	f := newGameFixture(t)
	rows := f.a.consolePhotoRows(f.ctx)
	if len(rows) != 1 || rows[0].Control == nil || rows[0].Control.Value != false || rows[0].Action.Op != ui.CommandConsolePhotosOn {
		t.Fatalf("off: %+v", rows)
	}
	if err := f.a.setConsolePhotos(f.ctx, true, 1); err != nil {
		t.Fatal(err)
	}
	rows = f.a.consolePhotoRows(f.ctx)
	if rows[0].Control.Value != true || rows[0].Action.Op != ui.CommandConsolePhotosOff || rows[0].Action.Confirm == "" {
		t.Errorf("on: %+v", rows)
	}
	if len(ui.ConsolePhotoNotice) > 400 {
		t.Errorf("the notice is %d characters: a Notice holds 400", len(ui.ConsolePhotoNotice))
	}
}
