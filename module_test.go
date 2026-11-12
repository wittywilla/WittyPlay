// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

import (
	"testing"

	"github.com/wittywilla/wittymodule"
)

// The servers this version is for take it: a manifest that stopped at 0.6.x was refused by
// every 0.8.0-alpha server.
func TestManifestFitsThisServer(t *testing.T) {
	m, err := wittymodule.ParseManifest(ManifestJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range []string{"0.6.0-alpha", "0.8.0-alpha"} {
		if err := m.Supports(server, wittymodule.MinAPI, wittymodule.API); err != nil {
			t.Errorf("%s: %v", server, err)
		}
	}
}
