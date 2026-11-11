// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package local

import "os/exec"

// Windows has no local sandbox (Missing says why); these only let the package build there.
const (
	sigTerm = iota
	sigKill
	sigStop
	sigCont
)

func ownGroup(*exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, sig int) {
	if cmd.Process != nil && sig == sigKill {
		_ = cmd.Process.Kill()
	}
}
