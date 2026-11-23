// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// A .wittyplay file is shared between people, so it is read before anyone has looked at it,
// as bare JSON or as a ZIP with pictures. Reading one must never panic, and what is taken must
// hold up: it checks out again, and every member it kept has a plain name and a bounded size.
func FuzzParse(f *testing.F) {
	f.Add([]byte(example))
	f.Add([]byte(`{"wittyplay":1,"default":{"picture":"case","case":"generic-box"}}`))
	f.Add([]byte("PK\x03\x04garbage"))
	if d, err := Parse([]byte(example)); err == nil {
		if b, err := d.EncodeArchive(); err == nil {
			f.Add(b)
		}
	}
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	for _, name := range []string{"../escape.json", "images/a.png", ManifestName} {
		if w, err := zw.Create(name); err == nil {
			_, _ = w.Write([]byte(example))
		}
	}
	_ = zw.Close()
	f.Add(zb.Bytes())
	f.Fuzz(func(t *testing.T, b []byte) {
		d, err := Parse(b)
		if err != nil {
			return
		}
		if err := d.Validate(); err != nil {
			t.Fatalf("taken, then invalid: %v", err)
		}
		var total int
		for name, data := range d.Files {
			if !ValidMemberName(name) || strings.Contains(name, "..") {
				t.Fatalf("kept a member named %q", name)
			}
			total += len(data)
		}
		if total > MaxUnpacked {
			t.Fatalf("kept %d bytes of members", total)
		}
		if _, err := d.EncodeAny(); err != nil && len(d.Files) == 0 {
			t.Fatalf("a file that was taken can't be written back: %v", err)
		}
	})
}

func FuzzValidMemberName(f *testing.F) {
	for _, s := range []string{"images/a.png", "../a", "a/../../b", "/etc/passwd", "C:\\x", "images/./a", "a//b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidMemberName(s) {
			return
		}
		if strings.HasPrefix(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, ":") {
			t.Fatalf("%q was taken as a member name", s)
		}
		for _, part := range strings.Split(s, "/") {
			if part == "" || part == "." {
				t.Fatalf("%q was taken as a member name", s)
			}
		}
	})
}

func FuzzParseGamepad(f *testing.F) {
	for _, s := range []string{"a", "lb+rb", "dpad-up", "", "x+y+z+"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = ParseGamepad(s) })
}
