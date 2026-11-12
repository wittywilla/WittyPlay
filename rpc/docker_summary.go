// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/host"
)

// Summary is how standalone emulators stand, for the server's diagnostics and log pages. It
// carries no secrets, no file contents, no paths and no addresses.
type Summary struct {
	Backend     string             `json:"backend"` // docker | local | host | off
	Available   bool               `json:"available"`
	Error       string             `json:"error,omitempty"`
	Missing     []string           `json:"missing"`     // what the native host lacks, in words
	Permissions []host.Permission  `json:"permissions"` // a Mac's, for the native host only
	Installed   []SummaryInstalled `json:"installed"`
	Sessions    int                `json:"sessions"`
}

// SummaryInstalled is one emulator that can run.
type SummaryInstalled struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Source  string `json:"source"` // catalogue | manual | own
}

// Summary answers at once: nothing is downloaded or pulled, and a Docker daemon is asked only
// whether it's there, briefly.
func (b *DockerBackend) Summary(ctx context.Context) Summary {
	set := b.Settings()
	out := Summary{Backend: "off", Missing: []string{}, Permissions: []host.Permission{}, Installed: []SummaryInstalled{}, Sessions: b.Count()}
	if set.Enabled {
		out.Backend = set.Runtime
		if out.Backend == "" {
			out.Backend = docker.RuntimeDocker
		}
		switch out.Backend {
		case docker.RuntimeHost:
			st := b.hostStatus()
			out.Available, out.Error = st.Available, st.Why
			if b.HostMissing != nil {
				out.Missing = append(out.Missing, b.HostMissing()...)
			}
			if hr, ok := b.Host.(*host.Runtime); ok {
				out.Permissions = append(out.Permissions, hr.Permissions()...)
			}
		case docker.RuntimeLocal:
			st := b.localStatus()
			out.Available, out.Error = st.Available, st.Why
		default:
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, st, err := b.runtime(cctx)
			cancel()
			out.Available, out.Error = err == nil, st.Error
			if errors.Is(err, errDockerOff) {
				out.Error = ""
			}
		}
	}
	if b.inst != nil {
		if list, err := b.inst.List(); err == nil {
			for _, in := range list {
				src := "catalogue"
				if in.Source == emulators.SourceManual {
					src = "manual"
				}
				out.Installed = append(out.Installed, SummaryInstalled{ID: in.ID, Version: in.Version, Source: src})
			}
		}
	}
	for _, o := range emulators.OwnList() {
		in := o.Installed()
		out.Installed = append(out.Installed, SummaryInstalled{ID: in.ID, Version: in.Version, Source: "own"})
	}
	return out
}
