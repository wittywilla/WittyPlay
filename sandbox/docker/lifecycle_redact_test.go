// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"strings"
	"testing"
)

func TestRedactKeys(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 2)
	in := "loading prod.keys\nheader_key = " + hex + "\nframe 42 hash " + hex + "\n"
	out := RedactKeys(in)
	if strings.Contains(out, "header_key = "+hex) || !strings.Contains(out, "header_key = [hidden]") {
		t.Errorf("a key's value wasn't hidden: %q", out)
	}
	if !strings.Contains(out, "loading prod.keys") || !strings.Contains(out, "hash "+hex) {
		t.Errorf("more than the key was hidden: %q", out)
	}
}
