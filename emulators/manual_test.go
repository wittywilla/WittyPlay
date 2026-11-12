// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func standIn() *Emulator {
	return &Emulator{ID: "standin", Name: "Stand-in", Manual: &Manual{
		Download:    "https://example.org/standin",
		Programs:    map[string][]string{"linux": {"StandIn", "standin-*.AppImage"}, "darwin": {"StandIn"}, "windows": {"StandIn.exe"}},
		BundleIDs:   []string{"org.example.standin"},
		VersionFrom: `^standin-([0-9][0-9.]*)-`,
	}}
}

func program(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A program is that emulator by its name on the system it will run on, an AppImage's name gives
// its version, and anything else is refused in words.
func TestIdentifyByName(t *testing.T) {
	e := standIn()
	dir := t.TempDir()
	img := program(t, filepath.Join(dir, "standin-1.4.2-x64.AppImage"))
	id, err := IdentifyFor(e, img, "linux")
	if err != nil || id.Version != "1.4.2" || id.Entry != "standin-1.4.2-x64.AppImage" || id.Dir != dir {
		t.Fatalf("%+v %v", id, err)
	}
	// A folder holding it: the program inside.
	unpacked := filepath.Join(dir, "unpacked")
	program(t, filepath.Join(unpacked, "StandIn"))
	if id, err := IdentifyFor(e, unpacked, "linux"); err != nil || id.Entry != "StandIn" {
		t.Errorf("a folder: %+v %v", id, err)
	}
	for name, p := range map[string]string{
		"another program": program(t, filepath.Join(dir, "SomethingElse")),
		"relative":        "StandIn",
		"missing":         filepath.Join(dir, "nope"),
	} {
		if _, err := IdentifyFor(e, p, "linux"); err == nil {
			t.Errorf("%s was taken", name)
		}
	}
	data := filepath.Join(dir, "StandInData", "StandIn")
	if err := os.MkdirAll(filepath.Dir(data), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(data, nil, 0o644)
	if _, err := IdentifyFor(e, data, "linux"); err == nil || !strings.Contains(err.Error(), "program") {
		t.Errorf("a file that can't run: %v", err)
	}
	// The Windows name doesn't count on Linux, where it would run in a container.
	if _, err := IdentifyFor(e, program(t, filepath.Join(dir, "StandIn.exe")), "linux"); err == nil {
		t.Error("a Windows program was taken for Linux")
	}
}

// A Mac app is recognised by its bundle identifier, not its name, and gives its version.
func TestIdentifyMacApp(t *testing.T) {
	e := standIn()
	dir := t.TempDir()
	app := filepath.Join(dir, "Whatever.app")
	program(t, filepath.Join(app, "Contents", "MacOS", "StandIn"))
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>StandIn</string>
<key>CFBundleIdentifier</key><string>org.example.standin</string>
<key>CFBundleShortVersionString</key><string>2.0.1</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{app, filepath.Join(app, "Contents", "MacOS", "StandIn")} {
		id, err := IdentifyFor(e, p, "darwin")
		if err != nil || id.Version != "2.0.1" || id.Entry != "Whatever.app/Contents/MacOS/StandIn" || id.Dir != dir {
			t.Errorf("%s: %+v %v", p, id, err)
		}
	}
	other := filepath.Join(dir, "Other.app")
	program(t, filepath.Join(other, "Contents", "MacOS", "StandIn"))
	_ = os.WriteFile(filepath.Join(other, "Contents", "Info.plist"), []byte(strings.Replace(plist, "org.example.standin", "org.example.other", 1)), 0o644)
	if _, err := IdentifyFor(e, other, "darwin"); err == nil || !strings.Contains(err.Error(), "org.example.other") {
		t.Errorf("another app: %v", err)
	}
}

// What's found in the drop folder is added, an install by hand is never downloaded, and
// forgetting it leaves the program where it is.
func TestManualInstalls(t *testing.T) {
	e := standIn()
	in := &Installer{DataDir: t.TempDir()}
	if _, err := in.Install(t.Context(), e); err != ErrManual {
		t.Errorf("install: %v", err)
	}
	drop := t.TempDir()
	img := program(t, filepath.Join(drop, "standin-3.0-x64.AppImage"))
	id, ok := Detect(e, drop, "linux")
	if !ok || id.Program != img {
		t.Fatalf("detect: %+v %v", id, ok)
	}
	rec, err := in.RegisterManual(e, id)
	if err != nil || rec.Source != SourceManual || rec.Version != "3.0" || in.Get("standin") == nil {
		t.Fatalf("%+v %v", rec, err)
	}
	if err := in.Uninstall("standin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(img); err != nil {
		t.Errorf("forgetting it removed the program: %v", err)
	}
	if in.Get("standin") != nil {
		t.Error("it's still recorded")
	}
}
