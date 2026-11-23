// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamemeta

import "testing"

func TestCleanDateWholeParts(t *testing.T) {
	for in, want := range map[string]string{
		"2001":                  "2001",
		"2001-02":               "2001-02",
		"2001-02-03":            "2001-02-03",
		" 2001-02-03T10:00:00Z": "2001-02-03",
		"2001-":                 "2001",
		"2001-1":                "2001",
		"2001-13":               "2001",
		"2001-00-10":            "2001",
		"2001-02-":              "2001-02",
		"2001-02-3":             "2001-02",
		"2001-02-40":            "2001-02",
		"2001/02/03":            "2001",
		"Feb 2001":              "",
		"1800-01-01":            "",
		"":                      "",
	} {
		if got := cleanDate(in); got != want {
			t.Errorf("cleanDate(%q) = %q, want %q", in, got, want)
		}
	}
}
