// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build unix

package hostsandbox

import "syscall"

func printFromC(s string) { _, _ = syscall.Write(1, []byte(s)) }
