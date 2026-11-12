// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package host

import (
	"net"
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
	// Network: a port bound on loopback, which Lichii needs to start; no connection anywhere,
	// on this machine or off it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	netTry := func(mode string) bool {
		cmd := exec.Command("/usr/bin/sandbox-exec", "-p", Profile(app, saves, userCacheDir()), helper, "-test.run=TestProfileHelper")
		cmd.Env = append(os.Environ(), "WP_PROFILE_NET="+mode)
		out, _ := cmd.CombinedOutput()
		return strings.Contains(string(out), "net ok")
	}
	if !netTry("bind") {
		t.Error("binding a UDP port on loopback was refused")
	}
	if netTry("dial " + l.Addr().String()) {
		t.Error("it connected to a service on this machine")
	}
	if netTry("dial 1.1.1.1:443") {
		t.Error("it reached the internet")
	}
	if !strings.Contains(Profile(app, saves, userCacheDir()), `\"`) {
		t.Error("a quote in a folder's name wasn't escaped")
	}
}

// TestProfileHelper is the program run inside the sandbox above.
func TestProfileHelper(t *testing.T) {
	if mode := os.Getenv("WP_PROFILE_NET"); mode != "" {
		if mode == "bind" {
			c, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err == nil {
				c.Close()
				println("net ok")
			}
			return
		}
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(mode, "dial "), 2e9)
		if err == nil {
			c.Close()
			println("net ok")
		}
		return
	}
	path := os.Getenv("WP_PROFILE_WRITE")
	if path == "" {
		t.Skip("run by TestProfileOnThisMac")
	}
	if err := os.WriteFile(path, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	println("wrote")
}
