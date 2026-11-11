// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package base holds the entrypoint that starts a display, sound, the input agent, the encoder
// and the emulator. The container images copy it in; the sandbox without Docker (sandbox/local)
// writes it out from here, so both run the same script.
package base

import _ "embed"

// Entrypoint is entrypoint.sh.
//
//go:embed entrypoint.sh
var Entrypoint []byte
