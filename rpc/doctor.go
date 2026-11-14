// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"strings"
)

// Finding is one thing WittyPlay's own checks found, for the server's Repair page. The
// server asks with the service call doctor.Check; module.json lists the checks under
// capabilities.doctor.
type Finding struct {
	Check    string   `json:"check"`
	Key      string   `json:"key"`
	Severity string   `json:"severity"` // info | warn | problem
	Title    string   `json:"title"`
	What     string   `json:"what"`
	Steps    []string `json:"steps,omitempty"`
}

// Doctor checks that standalone emulators can run the way the owner chose: a Mac's
// permissions for the native host, and the backend itself. Nothing is downloaded.
func (b *DockerBackend) Doctor(ctx context.Context) []Finding {
	out := []Finding{}
	sum := b.Summary(ctx)
	if sum.Backend == "" || sum.Backend == "off" {
		return out
	}
	for _, p := range sum.Permissions {
		if p.Granted {
			continue
		}
		what, pane, short := "record the screen, so it can't show an emulator's picture", "Screen & System Audio Recording", "record the screen"
		if p.Name == "accessibility" {
			what, pane, short = "send key presses and controller input to an emulator", "Accessibility", "send key presses"
		}
		out = append(out, Finding{Check: "host", Key: "permission-" + p.Name, Severity: "problem",
			Title: "macOS hasn't let the server " + short,
			What:  "Emulators run on this Mac need permission to " + what + ". Until it's given, games in these emulators won't start.",
			Steps: []string{"Open System Settings, Privacy & Security, " + pane + ".",
				"Turn it on for the program that runs the server (WittyMediaServer, or the Terminal you started it from).",
				"Start the server again: macOS only applies this to a program when it starts."}})
	}
	if len(out) == 0 && !sum.Available {
		why := sum.Error
		if why == "" {
			why = strings.Join(sum.Missing, "; ")
		}
		if why == "" {
			why = "it didn't say why"
		}
		out = append(out, Finding{Check: "host", Key: "backend", Severity: "warn",
			Title: "Standalone emulators can't run here now",
			What:  "The " + sum.Backend + " way of running them isn't available: " + why + ".",
			Steps: []string{"Open Settings, Games & emulators, Standalone emulators, and read what it says under how emulators run."}})
	}
	return out
}
