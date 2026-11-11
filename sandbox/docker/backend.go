// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Backend is where a game runs.
type Backend string

const (
	// BackendProcess: the game runner in its own sandboxed process. The default, and the only
	// one for libretro cores.
	BackendProcess Backend = "process"
	// BackendDockerWebRTC: a standalone emulator in a container, captured by FFmpeg inside it
	// and relayed into the same WebRTC session a libretro game has.
	BackendDockerWebRTC Backend = "docker+webrtc"
	// BackendDockerKasmVNC: a standalone emulator in a container, shown through its KasmVNC,
	// reached only through WittyPlay's authenticated proxy. The fallback.
	BackendDockerKasmVNC Backend = "docker+kasmvnc"
)

// Backends lists them, for a settings page.
var Backends = []Backend{BackendProcess, BackendDockerWebRTC, BackendDockerKasmVNC}

// Settings are what an administrator sets for the Docker backend. Docker is off until Enabled is
// set, and with it off nothing here runs and nothing reaches the daemon.
type Settings struct {
	Enabled bool `json:"docker.enabled"`
	// Overrides: a backend for an emulator, by its id, over what the list says.
	Overrides map[string]Backend `json:"docker.backend,omitempty"`
	// Network is the name of WittyPlay's own Docker network.
	Network string `json:"docker.network,omitempty"`
	// Internal makes that network internal: no way out to the internet. Set it where the
	// module can reach the containers' addresses directly (Linux without Docker Desktop).
	Internal bool `json:"docker.internal,omitempty"`
	// PublishLoopback publishes the stream ports on 127.0.0.1 instead, for Docker Desktop.
	PublishLoopback bool    `json:"docker.publishLoopback,omitempty"`
	MaxContainers   int     `json:"docker.maxContainers,omitempty"`
	MemoryMB        int64   `json:"docker.memoryMB,omitempty"`
	CPUs            float64 `json:"docker.cpus,omitempty"`
	Pids            int64   `json:"docker.pids,omitempty"`
	IdleMinutes     int     `json:"docker.idleMinutes,omitempty"`
	MaxHours        int     `json:"docker.maxHours,omitempty"`
	GPU             GPU     `json:"docker.gpu"`
	// Images: an image an administrator built (docker/images), by emulator id, over the list's.
	// Each must be pinned to a digest.
	Images map[string]string `json:"docker.images,omitempty"`
}

// DefaultSettings: off.
func DefaultSettings() Settings {
	return Settings{Enabled: false, Network: "wittyplay", Overrides: map[string]Backend{}, Images: map[string]string{}}
}

// Field describes one setting, for a settings page to draw. Keys are Settings' JSON names.
type Field struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"` // toggle | number | text | choice | map
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Default any      `json:"default"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Choices []string `json:"choices,omitempty"`
	// Admin: only an administrator sees or changes it. Every Docker setting is.
	Admin bool `json:"admin"`
}

// Schema is the settings schema of the Docker backend, in the order a page shows it.
func Schema() []Field {
	bs := make([]string, len(Backends))
	for i, b := range Backends {
		bs[i] = string(b)
	}
	return []Field{
		{Key: "docker.enabled", Kind: "toggle", Label: "Run standalone emulators in Docker", Default: false, Admin: true,
			Help: "Off by default. Libretro cores always run in the process sandbox, whatever this says."},
		{Key: "docker.network", Kind: "text", Label: "Docker network", Default: "wittyplay", Admin: true},
		{Key: "docker.internal", Kind: "toggle", Label: "No internet for emulators", Default: false, Admin: true,
			Help: "Makes WittyPlay's network internal. Needs a host that reaches container addresses directly (Linux without Docker Desktop)."},
		{Key: "docker.publishLoopback", Kind: "toggle", Label: "Publish on 127.0.0.1 (Docker Desktop)", Default: false, Admin: true},
		{Key: "docker.maxContainers", Kind: "number", Label: "Emulator containers at once", Default: DefaultMaxContainers, Min: 0, Max: 32, Admin: true},
		{Key: "docker.memoryMB", Kind: "number", Label: "Memory per container (MB)", Default: DefaultMemoryMB, Min: 0, Max: 64 << 10, Admin: true},
		{Key: "docker.cpus", Kind: "number", Label: "CPUs per container", Default: DefaultCPUs, Min: 0, Max: 64, Admin: true},
		{Key: "docker.pids", Kind: "number", Label: "Processes per container", Default: DefaultPids, Min: 0, Max: 65536, Admin: true},
		{Key: "docker.idleMinutes", Kind: "number", Label: "Stop after idle (minutes)", Default: int(DefaultIdle.Minutes()), Min: 0, Admin: true},
		{Key: "docker.maxHours", Kind: "number", Label: "Longest session (hours)", Default: int(DefaultLifetime.Hours()), Min: 0, Max: 48, Admin: true},
		{Key: "docker.gpu", Kind: "map", Label: "GPU passthrough (DRI, Nvidia)", Default: GPU{}, Admin: true},
		{Key: "docker.backend", Kind: "map", Label: "Backend per emulator", Default: map[string]Backend{}, Choices: bs, Admin: true},
		{Key: "docker.images", Kind: "map", Label: "Own image per emulator (pinned by digest)", Default: map[string]string{}, Admin: true},
	}
}

// Clean checks settings that came from a request and fills in what was left out.
func (s Settings) Clean() (Settings, error) {
	if s.Network == "" {
		s.Network = "wittyplay"
	}
	if strings.ContainsAny(s.Network, "/:@ ") || s.Network == "host" || s.Network == "bridge" || s.Network == "none" || len(s.Network) > 64 {
		return s, fmt.Errorf("network %q can't be WittyPlay's own", s.Network)
	}
	for id, b := range s.Overrides {
		if !validEmuID.MatchString(id) {
			return s, fmt.Errorf("bad emulator id %q", id)
		}
		if !slices.Contains(Backends, b) {
			return s, fmt.Errorf("backend %q is none of %v", b, Backends)
		}
	}
	if s.Overrides == nil {
		s.Overrides = map[string]Backend{}
	}
	for id, ref := range s.Images {
		if !validEmuID.MatchString(id) {
			return s, fmt.Errorf("bad emulator id %q", id)
		}
		if !Pinned(ref) {
			return s, fmt.Errorf("the image for %s: %w", id, ErrNotPinned)
		}
	}
	if s.Images == nil {
		s.Images = map[string]string{}
	}
	switch {
	case s.MaxContainers < 0 || s.MaxContainers > 32:
		return s, errors.New("between 0 and 32 containers")
	case s.MemoryMB < 0 || s.MemoryMB > 64<<10:
		return s, errors.New("memory: up to 64 GiB")
	case s.CPUs < 0 || s.CPUs > 64:
		return s, errors.New("CPUs: up to 64")
	case s.Pids < 0 || s.Pids > 65536:
		return s, errors.New("processes: up to 65536")
	case s.IdleMinutes < 0 || s.MaxHours < 0 || s.MaxHours > 48:
		return s, errors.New("bad time limits")
	}
	return s, nil
}

// Choose picks the backend for a game. A libretro core always runs as a process. A standalone
// emulator runs in Docker only when Docker is on and reachable; its list entry says which
// stream, an administrator's override wins, and "process" means it isn't offered here.
//
// stream is the list's "pipe" or "kasmvnc".
func Choose(s Settings, dockerUp bool, emulatorID, stream string) (Backend, error) {
	if emulatorID == "" {
		return BackendProcess, nil
	}
	if !s.Enabled {
		return "", errors.New("standalone emulators need the Docker backend, which is off; an administrator can turn it on in Settings, Consoles & cores")
	}
	if !dockerUp {
		return "", ErrUnavailable
	}
	b := BackendDockerWebRTC
	if stream == "kasmvnc" {
		b = BackendDockerKasmVNC
	}
	if o, ok := s.Overrides[emulatorID]; ok {
		b = o
	}
	if b == BackendProcess {
		return "", errors.New("an administrator has turned this emulator off")
	}
	return b, nil
}

// Limits makes container limits from the settings.
func (s Settings) Limits() Limits {
	return Limits{MemoryMB: s.MemoryMB, CPUs: s.CPUs, Pids: s.Pids}
}

// Lifecycle makes the lifecycle these settings describe.
func (s Settings) Lifecycle(rt Runtime) *Lifecycle {
	l := &Lifecycle{RT: rt, Max: s.MaxContainers, Pull: true}
	if s.IdleMinutes > 0 {
		l.Idle = time.Duration(s.IdleMinutes) * time.Minute
	}
	if s.MaxHours > 0 {
		l.MaxLifetime = time.Duration(s.MaxHours) * time.Hour
	}
	return l
}

// LoadSettings reads settings saved by SaveSettings. A file that isn't there is the defaults.
func LoadSettings(path string) (Settings, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return DefaultSettings(), err
	}
	s := DefaultSettings()
	if err := json.Unmarshal(b, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("the Docker settings are unreadable: %w", err)
	}
	return s.Clean()
}

// SaveSettings writes settings in one step.
func SaveSettings(path string, s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
