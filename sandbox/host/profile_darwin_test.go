// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The profile, run for real by sandbox-exec: the emulator can write in its save folder, and
// nowhere else, and has no network. A folder's name can't add a rule of its own.
func TestProfileOnThisMac(t *testing.T) {
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	app := filepath.Join(dir, `app") (allow file-write* (subpath "/`)
	saves := filepath.Join(dir, "saves")
	for _, d := range []string{app, saves} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere := filepath.Join(dir, "elsewhere") // a folder of this test's own, outside the save folder
	run := func(script string) error {
		cmd := exec.Command("/usr/bin/sandbox-exec", "-p", Profile(app, saves, userCacheDir()), "/bin/sh", "-c", script)
		return cmd.Run()
	}
	// /bin/sh isn't in the emulator's folder, so even starting it must be refused...
	if err := run("true"); err == nil {
		t.Fatal("a program outside the emulator's folder started")
	}
	// ...and with a program inside (this test's own, copied there), writing follows the rules.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(app, "helper")
	if err := os.WriteFile(helper, b, 0o755); err != nil {
		t.Fatal(err)
	}
	try := func(path string) error {
		cmd := exec.Command("/usr/bin/sandbox-exec", "-p", Profile(app, saves, userCacheDir()), helper, "-test.run=TestProfileHelper")
		cmd.Env = append(os.Environ(), "WP_PROFILE_WRITE="+path)
		out, err := cmd.CombinedOutput()
		if err == nil && !strings.Contains(string(out), "wrote") {
			return os.ErrPermission
		}
		return err
	}
	if err := try(filepath.Join(saves, "ok")); err != nil {
		t.Errorf("writing in the save folder: %v", err)
	}
	if err := try(elsewhere); err == nil {
		t.Error("it wrote outside its save folder")
	}
	if !strings.Contains(Profile(app, saves, userCacheDir()), `\"`) {
		t.Error("a quote in a folder's name wasn't escaped")
	}
}

// TestProfileHelper is the program run inside the sandbox above.
func TestProfileHelper(t *testing.T) {
	path := os.Getenv("WP_PROFILE_WRITE")
	if path == "" {
		t.Skip("run by TestProfileOnThisMac")
	}
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	println("wrote")
}
