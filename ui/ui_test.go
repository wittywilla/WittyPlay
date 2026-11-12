// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package ui

import (
	"testing"

	"github.com/wittywilla/wittymodule/sdui/sduitest"
)

// sdui.json is this definition, and the definition passes the kit's checks. Run with
// -update-sdui to write it again.
func TestSDUIFile(t *testing.T) {
	sduitest.Golden(t, UI(), "../sdui.json")
}
