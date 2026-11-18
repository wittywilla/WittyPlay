// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !windows

package stream

import "os/exec"

// gamesPlaying has nothing to do here: the server leaves a module at the priority it has
// itself elsewhere, and a process can't raise its own priority back once it's lowered.
func gamesPlaying(int) {}

// encoderPriority has nothing to do here either, for the same reason.
func encoderPriority(*exec.Cmd) {}
