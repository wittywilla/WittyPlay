// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package hostsandbox

import "golang.org/x/sys/windows"

// Confine has nothing to do here: the server sets the limits from outside.
func Confine(l Limits) error { return nil }

// Apply does little on Windows: the server starts the host in a job object, with a
// low-integrity token, at below-normal priority. A demanding game asks for above-normal
// priority, which a process may always do for itself.
//
// Normal wasn't enough. A game is someone playing, now, and any other program at normal
// priority or above shared the processor with it equally: on a busy machine (browsers, which
// raise themselves to above normal, a transcode, a library scan) a Wii game ran at 2 to 13
// frames a second of its 60 while its own threads used a third of one processor thread,
// waiting for a turn. Above normal still gives way to anything the system holds to be more
// urgent, and a game uses a few threads at most, so the rest of the machine keeps going.
func Apply(l Limits) error {
	if l.Normal {
		return windows.SetPriorityClass(windows.CurrentProcess(), windows.ABOVE_NORMAL_PRIORITY_CLASS)
	}
	return nil
}
