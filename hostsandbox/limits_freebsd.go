// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package hostsandbox

import "golang.org/x/sys/unix"

// FreeBSD's jails take root and Capsicum takes away opening files by name, so a runner can't have
// either. These are what an ordinary account can give up for good: starting programs, and any new
// socket, as there's no room for its buffers. A graphics driver may need a socket to reach a display.
func moreLimits(l Limits) {
	const (
		rlimitNProc  = 7
		rlimitSBSize = 9
		rlimitNPTS   = 11
	)
	none := unix.Rlimit{}
	_ = unix.Setrlimit(rlimitNProc, &none)
	_ = unix.Setrlimit(rlimitNPTS, &none)
	if !l.GPU {
		_ = unix.Setrlimit(rlimitSBSize, &none)
	}
}
