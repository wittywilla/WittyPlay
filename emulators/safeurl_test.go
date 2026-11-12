// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestURLsLoseLoginsAndQueries(t *testing.T) {
	raw := "http://me:pw@example.com/x.zip?token=abc#f"
	if got := SafeURL(raw); got != "http://example.com/x.zip" {
		t.Errorf("SafeURL = %q", got)
	}
	for _, err := range []error{CheckURL(raw, []string{"example.com"}), CheckURL("https://me:pw@example.com/x?token=abc", []string{"example.com"}),
		CleanURLError(&url.Error{Op: "Get", URL: raw, Err: errors.New("refused")})} {
		if err == nil || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "pw") {
			t.Errorf("an error kept the login or query: %v", err)
		}
	}
}
