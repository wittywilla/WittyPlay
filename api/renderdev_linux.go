// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// devDRI is where Linux keeps graphics cards' render nodes; tests point it elsewhere.
var devDRI = "/dev/dri"

// inContainer: this server runs in Docker or Podman.
func inContainer() bool {
	if os.Getenv("WMS_DOCKER") == "1" {
		return true
	}
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// renderNodeReason says, in words, why no graphics card could be used when the reason is the
// render node itself: none passed into a container, or one this server's user can't open (a
// container's user isn't in the group that owns it on the host, and can't join it once started,
// so the group has to be added when it starts). "" when that isn't why.
func renderNodeReason() string {
	nodes, _ := filepath.Glob(filepath.Join(devDRI, "renderD*"))
	if len(nodes) == 0 {
		if inContainer() {
			return "no graphics device was passed into the container. Add /dev/dri under devices: (docs/DOCKER.md, Games)"
		}
		return ""
	}
	var denied string
	var gid uint32
	for _, n := range nodes {
		f, err := os.OpenFile(n, os.O_RDWR, 0)
		if err == nil {
			_ = f.Close()
			return "" // one can be opened: the reason is elsewhere
		}
		if errors.Is(err, fs.ErrPermission) && denied == "" {
			denied = n
			if st, err := os.Stat(n); err == nil {
				if s, ok := st.Sys().(*syscall.Stat_t); ok {
					gid = s.Gid
				}
			}
		}
	}
	if denied == "" {
		return ""
	}
	if gid == 0 {
		return fmt.Sprintf("this server's user can't open %s, which only root may use. Allow its group on the host (docs/DOCKER.md, Only root may open the render node)", denied)
	}
	return fmt.Sprintf("this server's user can't open %s, which belongs to group %d. Add that group to the container: group_add: - \"%d\"", denied, gid, gid)
}
