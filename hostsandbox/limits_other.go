// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build unix && !linux && !freebsd && !darwin

package hostsandbox

// moreLimits has nothing to add on a system this project doesn't ship to.
func moreLimits(Limits) {}
