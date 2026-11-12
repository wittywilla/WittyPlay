// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package local

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wittywilla/wittyplay/sandbox/docker"
)

// ArgsLimitsOnly is the command for an emulator with a display of its own and nothing else
// between it and the machine: FreeBSD, which has no bubblewrap, and whose jails need root. The
// entrypoint and the emulator see this machine's paths: every container path the spec names
// (the game, the saves, the BIOS folder, the emulator, its home, /tmp, the socket's folder) is
// turned into a folder of this session's own, or the real one it was mounted from. FreeBSD's
// limits(1) holds it to the spec's memory. It isn't a sandbox, and Settings says so.
//
// display is the X display number this session's Xvfb takes: on a machine with no namespaces it
// shares the display numbers with every other program, so each session has its own.
func ArgsLimitsOnly(s docker.Spec, entrypoint, agent, work string, display int) ([]string, error) {
	if err := docker.Check(s); err != nil {
		return nil, err
	}
	if !s.Local {
		return nil, errors.New("this spec is for Docker, not the local sandbox")
	}
	if !filepath.IsAbs(work) {
		return nil, errors.New("the session's folder must be a full path")
	}
	type pair struct{ from, to string }
	paths := []pair{{"/tmp", filepath.Join(work, "tmp")}, {docker.ContainerEmuHome, filepath.Join(work, "home")}, {toolDir, filepath.Join(work, "tools")}}
	if _, ok := s.Tmpfs[docker.ContainerAppRun]; ok {
		paths = append(paths, pair{docker.ContainerAppRun, filepath.Join(work, "app")})
	}
	for _, m := range s.Mounts {
		paths = append(paths, pair{m.Target, m.Source})
	}
	slices.SortFunc(paths, func(a, b pair) int { return len(b.from) - len(a.from) })
	var local func(v string) string
	local = func(v string) string {
		for _, p := range paths {
			if v == p.from {
				return p.to
			}
			if rest, ok := strings.CutPrefix(v, p.from+"/"); ok {
				return filepath.Join(p.to, rest)
			}
			if i := strings.Index(v, "="+p.from); i >= 0 && (len(v) == i+1+len(p.from) || v[i+1+len(p.from)] == '/') {
				return v[:i+1] + p.to + v[i+1+len(p.from):]
			}
		}
		if rest, ok := strings.CutPrefix(v, "unix:"); ok {
			return "unix:" + local(rest)
		}
		return v
	}
	tools := filepath.Join(work, "tools")
	a := []string{"limits", "-m", strconv.FormatInt(s.MemoryBytes, 10), "env", "-i", "PATH=" + tools + ":/usr/local/bin:/usr/bin:/bin"}
	if display <= 0 {
		return nil, errors.New("a session needs a display number of its own")
	}
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "DISPLAY":
			v = ":" + strconv.Itoa(display)
		// The encoder hands RTP to the agent on loopback; with no network namespace of its own
		// that's everyone's loopback, so each session's ports are its own too.
		case "WITTYPLAY_RTP_VIDEO":
			v = strconv.Itoa(40000 + display*4)
		case "WITTYPLAY_RTP_AUDIO":
			v = strconv.Itoa(40000 + display*4 + 2)
		}
		a = append(a, k+"="+local(v))
	}
	a = append(a, filepath.Join(tools, "wittyplay-entrypoint"))
	for _, c := range s.Cmd {
		a = append(a, local(c))
	}
	return a, nil
}

// prepareLimitsOnly makes the session's folders, and puts the entrypoint and the agent where
// the command above looks for them.
func prepareLimitsOnly(work, entrypoint, agent string) error {
	for _, d := range []string{"tmp", "home", "tools", "app"} {
		if err := os.MkdirAll(filepath.Join(work, d), 0o700); err != nil {
			return err
		}
	}
	for name, from := range map[string]string{"wittyplay-entrypoint": entrypoint, "wittyplay-input-agent": agent} {
		to := filepath.Join(work, "tools", name)
		_ = os.Remove(to)
		if err := os.Symlink(from, to); err != nil {
			return err
		}
	}
	return nil
}
