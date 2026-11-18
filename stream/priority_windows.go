// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
)

// The server starts modules below normal priority, which suits a library scan and not a game:
// this process carries every frame from the runner to the encoder and on to the network, and
// below normal it waited behind everything else the machine was doing. So while a game is
// playing it goes above normal, as the demanding games' runners do (hostsandbox.Apply), and
// back to what it was given once the last one ends. A process may always do this for itself.

var priority struct {
	mu     sync.Mutex
	raised bool
	was    uint32
}

// gamesPlaying is told how many games are playing now.
func gamesPlaying(n int) {
	priority.mu.Lock()
	defer priority.mu.Unlock()
	self := windows.CurrentProcess()
	switch {
	case n > 0 && !priority.raised:
		was, err := windows.GetPriorityClass(self)
		if err != nil || was == windows.ABOVE_NORMAL_PRIORITY_CLASS || was == windows.HIGH_PRIORITY_CLASS || was == windows.REALTIME_PRIORITY_CLASS {
			return
		}
		if windows.SetPriorityClass(self, windows.ABOVE_NORMAL_PRIORITY_CLASS) == nil {
			priority.raised, priority.was = true, was
		}
	case n == 0 && priority.raised:
		if windows.SetPriorityClass(self, priority.was) == nil {
			priority.raised = false
		}
	}
}

// encoderPriority starts a game's encoder above normal, for the same reason: a frame waiting
// for it is a frame late. A child may start above its parent.
func encoderPriority(cmd *exec.Cmd) {
	if cmd.SysProcAttr != nil {
		cmd.SysProcAttr.CreationFlags |= windows.ABOVE_NORMAL_PRIORITY_CLASS
	}
}
