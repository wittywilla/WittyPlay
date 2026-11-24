// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Why no card could be used, when the render node is the reason, says what to add to the container.
func TestRenderNodeReason(t *testing.T) {
	was := devDRI
	t.Cleanup(func() { devDRI = was })
	t.Setenv("WMS_DOCKER", "1")

	devDRI = t.TempDir()
	if r := renderNodeReason(); !strings.Contains(r, "no graphics device was passed into the container") {
		t.Errorf("none passed in: %q", r)
	}

	node := filepath.Join(devDRI, "renderD128")
	if err := os.WriteFile(node, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if r := renderNodeReason(); r != "" {
		t.Errorf("one this user can open: %q", r)
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens anything: the refused case can't be made")
	}
	if err := os.Chmod(node, 0); err != nil {
		t.Fatal(err)
	}
	if r := renderNodeReason(); !strings.Contains(r, "can't open "+node) || !strings.Contains(r, "group_add") {
		t.Errorf("one this user can't open: %q", r)
	}
}
