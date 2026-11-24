// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !linux

package api

// renderNodeReason: only Linux has render nodes to explain.
func renderNodeReason() string { return "" }

func inContainer() bool { return false }
