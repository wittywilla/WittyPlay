// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamemeta

import (
	"bytes"
	"testing"
)

func FuzzGameParsers(f *testing.F) {
	f.Add([]byte("RARCHDB\x00"), "Some Game (USA) (Rev 1).sfc")
	f.Add([]byte("\x81\xa4name\xa1x"), "[BIOS] x.bin")
	f.Add([]byte("SEGA SEGASATURN "), "2001-01-01")
	f.Add(bytes.Repeat([]byte{0xff}, 4096), "9.5 / 10")
	f.Fuzz(func(t *testing.T, b []byte, s string) {
		_ = parseRDB(b, func(*rdbRecord) {})
		CleanName(s)
		if d := cleanDate(s); d != "" && !validDate(d) {
			t.Errorf("cleanDate(%q) = %q, not YYYY, YYYY-MM or YYYY-MM-DD", s, d)
		}
		parseReview(s)
		r := bytes.NewReader(b)
		playStationSerial(r)
		segaSerial(r, s)
		isoFile(r, s, 1<<20)
	})
}

// validDate: YYYY, YYYY-MM or YYYY-MM-DD, digits where digits go.
func validDate(d string) bool {
	if len(d) != 4 && len(d) != 7 && len(d) != 10 {
		return false
	}
	for i, c := range d {
		if (i == 4 || i == 7) != (c == '-') || (c != '-' && (c < '0' || c > '9')) {
			return false
		}
	}
	return true
}
