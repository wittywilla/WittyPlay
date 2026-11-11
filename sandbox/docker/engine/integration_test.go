// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build docker_integration

// Run with a Docker daemon reachable (DOCKER_HOST or the default socket):
//
//	go test -tags docker_integration -run Integration -v ./sandbox/docker/engine/
//
// It pulls busybox pinned by digest, creates a container from docker.Build's hardened spec through
// the engine, checks every hardening field as the daemon reports it, and removes everything.
package engine

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/wittywilla/wittyplay/sandbox/docker"
)

// busybox 1.36.1, the multi-platform index, pinned by digest (checked against Docker Hub's
// registry API).
const busybox = "docker.io/library/busybox:1.36.1@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

func TestIntegrationHardenedContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	e, err := Connect()
	if err != nil {
		t.Skip("no Docker:", err)
	}
	defer e.Close()
	if st := e.Available(ctx); !st.Available {
		t.Skip("Docker isn't reachable:", st.Error)
	}
	if err := e.Pull(ctx, busybox); err != nil {
		t.Fatal(err)
	}
	netName := "wittyplay-it-" + docker.NewSecret()[:8]
	n, err := e.EnsureNetwork(ctx, docker.NetworkSpec{Name: netName, Internal: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = e.c.NetworkRemove(context.Background(), n.ID, client.NetworkRemoveOptions{}) }()

	dir := t.TempDir()
	rom := filepath.Join(dir, "game.iso")
	saves := filepath.Join(dir, "saves")
	prog := filepath.Join(dir, "emulator")
	for _, d := range []string{saves, prog} {
		if err := os.Mkdir(d, 0o777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(rom, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sid := docker.NewSecret()[:32]
	spec, err := docker.Build(docker.Request{
		SessionID: sid, UserID: 1, Emulator: "busybox", Image: busybox,
		ROM: rom, SaveDir: saves, ProgramDir: prog, AppImage: true,
		Cmd: []string{"sleep", "300"}, Stream: "kasmvnc", KasmToken: docker.NewSecret(), InputToken: docker.NewSecret(),
		Network: n.Name, Limits: docker.Limits{MemoryMB: 128, CPUs: 0.5, Pids: 64},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := e.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		_ = e.Stop(cctx, id, time.Second)
		if err := e.Remove(cctx, id); err != nil {
			t.Errorf("removing: %v", err)
		}
		if left, _ := e.List(cctx, map[string]string{docker.LabelSession: sid}); len(left) != 0 {
			t.Errorf("%d containers left behind", len(left))
		}
	}()
	if err := e.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	info, err := e.Inspect(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Running || info.Labels[docker.LabelManaged] != "true" || info.Labels[docker.LabelSession] != sid {
		t.Fatalf("inspect: %+v", info)
	}

	// The daemon's own view of the hardening.
	raw, err := e.c.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hc, cfg := raw.Container.HostConfig, raw.Container.Config
	if hc == nil || cfg == nil {
		t.Fatal("no host config")
	}
	checks := []struct {
		what string
		ok   bool
	}{
		{"uid 1000", cfg.User == "1000:1000"},
		{"read-only root", hc.ReadonlyRootfs},
		{"cap drop ALL", slices.Equal(hc.CapDrop, []string{"ALL"})},
		{"no caps added", len(hc.CapAdd) == 0},
		{"no-new-privileges", slices.Contains(hc.SecurityOpt, "no-new-privileges:true")},
		{"not privileged", !hc.Privileged},
		{"pids", hc.PidsLimit != nil && *hc.PidsLimit == 64},
		{"memory", hc.Memory == 128<<20 && hc.MemorySwap == 128<<20},
		{"cpus", hc.NanoCPUs == 5e8},
		{"network", string(hc.NetworkMode) == n.Name},
		{"/tmp noexec", strings.Contains(hc.Tmpfs["/tmp"], "noexec")},
		{"/run/app exec but nosuid,nodev", strings.Contains(hc.Tmpfs[docker.ContainerAppRun], ",exec,") &&
			strings.Contains(hc.Tmpfs[docker.ContainerAppRun], "nosuid") && strings.Contains(hc.Tmpfs[docker.ContainerAppRun], "nodev")},
		{"no ports published", len(hc.PortBindings) == 0},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s: host config %+v", c.what, hc)
		}
	}
	for _, m := range hc.Mounts {
		wantRO := m.Target != docker.ContainerSaves
		if m.ReadOnly != wantRO {
			t.Errorf("mount %s read-only=%v", m.Target, m.ReadOnly)
		}
	}
	if !slices.ContainsFunc(hc.Mounts, func(m mount.Mount) bool { return m.Target == docker.ContainerProgram && m.ReadOnly }) {
		t.Errorf("no read-only %s: %+v", docker.ContainerProgram, hc.Mounts)
	}
	if _, err := e.Logs(ctx, id, 10); err != nil {
		t.Errorf("logs: %v", err)
	}
}
