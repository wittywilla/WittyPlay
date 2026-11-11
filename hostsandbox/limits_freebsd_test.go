// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package hostsandbox

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// What a runner gives up on FreeBSD, tried from inside one. Root isn't held to the limit on programs.
func TestFreeBSDRunnerLimits(t *testing.T) {
	if os.Getenv("WITTYPLAY_LIMITS_CHILD") == "1" {
		if err := Apply(Limits{SaveDir: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
		if fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0); err == nil {
			unix.Close(fd)
			println("SOCKET")
		}
		if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); err == nil {
			unix.Close(fd)
			println("SOCKET")
		}
		if os.Geteuid() != 0 {
			if err := exec.Command("/bin/sh", "-c", "exit 0").Run(); err == nil {
				println("PROGRAM")
			}
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestFreeBSDRunnerLimits", "-test.v")
	cmd.Env = append(os.Environ(), "WITTYPLAY_LIMITS_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, bad := range []string{"SOCKET", "PROGRAM"} {
		if strings.Contains(string(out), bad+"\n") {
			t.Errorf("%s got through:\n%s", bad, out)
		}
	}
}
