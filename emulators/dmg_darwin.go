// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// unpackDMG attaches a disk image read-only, without showing it in the Finder and without
// running anything on it, copies what's inside out under the unpacker's rules, and detaches it.
// The image has already passed its hash check.
func unpackDMG(file, dir, entry string, limit int64) error {
	mnt, err := os.MkdirTemp("", "wittyplay-dmg-")
	if err != nil {
		return err
	}
	defer os.Remove(mnt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-noverify", "-mountpoint", mnt, file).CombinedOutput()
	if err != nil {
		return fmt.Errorf("the disk image wouldn't open: %v: %s", err, out)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), time.Minute)
		defer dcancel()
		if exec.CommandContext(dctx, "/usr/bin/hdiutil", "detach", mnt, "-quiet").Run() != nil {
			_ = exec.CommandContext(dctx, "/usr/bin/hdiutil", "detach", mnt, "-force", "-quiet").Run()
		}
	}()
	return copyTree(mnt, dir, entry, limit)
}
