// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"sync/atomic"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamelogos"
)

// A console's names follow the version an administrator chose for its photo (a Famicom is called
// the Family Computer, or Famicom, wherever WittyPlay names it: the Consoles tab, a library's
// rows, game cards and Recent's console bar), unless they keep its usual names for it
// (cfgConsoleNamesKept). Until a version is chosen, a console keeps the names in WittyPlay's table,
// so nothing is renamed by itself. Read on every page a console is named on, so it's kept here,
// refreshed when either choice changes.

// cfgConsoleNamesKept is the consoles whose usual names stay whichever version is chosen, by id.
const cfgConsoleNamesKept = "consoleNamesKept"

type shownName struct{ Name, Short string }

var consoleNames atomic.Pointer[map[string]shownName]

// refreshConsoleNames reads both choices again.
func (a *App) refreshConsoleNames(ctx context.Context) {
	chosen := map[string]string{}
	_, _ = a.Config(ctx, cfgConsolePhotoVariants, &chosen)
	kept := map[string]bool{}
	_, _ = a.Config(ctx, cfgConsoleNamesKept, &kept)
	m := map[string]shownName{}
	for id, vid := range chosen {
		if kept[id] {
			continue
		}
		if v, ok := gamelogos.PhotoVariantOf(id, vid); ok && v.Name != "" {
			m[id] = shownName{v.Name, v.Short}
		}
	}
	consoleNames.Store(&m)
}

// namesKept says whether a console keeps its usual names.
func (a *App) namesKept(ctx context.Context, id string) bool {
	kept := map[string]bool{}
	_, _ = a.Config(ctx, cfgConsoleNamesKept, &kept)
	return kept[id]
}

// shownConsole is a console with the names it's shown by.
func shownConsole(c wittyplay.Console) wittyplay.Console {
	if p := consoleNames.Load(); p != nil {
		if n, ok := (*p)[c.ID]; ok {
			c.Name, c.Short = n.Name, n.Short
		}
	}
	return c
}
