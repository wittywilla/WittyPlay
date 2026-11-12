// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// BackendLocalWebRTC: a standalone emulator in the sandbox without Docker (sandbox/local),
// streamed the way BackendDockerWebRTC is. It's what a game gets when docker.runtime is "local";
// nobody picks it per emulator.
const BackendLocalWebRTC Backend = "local+webrtc"

// BackendHostWebRTC: a standalone emulator on this machine's own desktop, in the system's own
// sandbox (sandbox/host), captured and streamed the way BackendDockerWebRTC is. It's what a game
// gets when docker.runtime is "host": a Mac or a Windows server.
const BackendHostWebRTC Backend = "host+webrtc"

// Backends lists them, for a settings page.
var Backends = []Backend{BackendProcess, BackendDockerWebRTC, BackendDockerKasmVNC, BackendLocalWebRTC, BackendHostWebRTC}

// Runtimes an administrator can choose between.
const (
	RuntimeDocker = "docker" // a Docker (or Podman) daemon
	RuntimeLocal  = "local"  // a display of its own on this machine: bubblewrap on Linux, limits only on FreeBSD
	RuntimeHost   = "host"   // this machine's own desktop and sandbox: macOS and Windows
)

// DefaultRuntime is what a new server starts with: this machine's own sandbox where there is
// one that needs no daemon, Docker on Linux (whose own sandbox needs bubblewrap installed).
func DefaultRuntime(goos string) string {
	switch goos {
	case "darwin", "windows":
		return RuntimeHost
	case "freebsd":
		return RuntimeLocal
	}
	return RuntimeDocker
}

// RuntimeChoices are the runtimes a server on goos can use, for its settings page.
func RuntimeChoices(goos string) []Choice {
	out := []Choice{{RuntimeDocker, "Docker or Podman containers"}}
	switch goos {
	case "linux":
		out = append(out, Choice{RuntimeLocal, "This machine's own sandbox (bubblewrap and a display of its own)"})
	case "freebsd":
		out = append(out, Choice{RuntimeLocal, "This machine, with a display of its own (limits only: FreeBSD has no sandbox WittyPlay can use)"})
	case "darwin":
		out = append(out, Choice{RuntimeHost, "This Mac (sandbox-exec; needs Screen Recording and Accessibility permission)"})
	case "windows":
		out = append(out, Choice{RuntimeHost, "This PC (a job object at low integrity; needs a signed-in desktop)"})
	}
	return out
}

// Settings are what an administrator sets for the Docker backend. Docker is off until Enabled is
// set, and with it off nothing here runs and nothing reaches the daemon.
type Settings struct {
	Enabled bool `json:"docker.enabled"`
	// Runtime is what runs the emulators: "docker" (the default) or "local".
	Runtime string `json:"docker.runtime,omitempty"`
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
	return Settings{Enabled: false, Runtime: DefaultRuntime(runtime.GOOS), Network: "wittyplay", Overrides: map[string]Backend{}, Images: map[string]string{}}
}

// Choice is one value a setting may take, and what a page calls it.
type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field describes one setting, for a settings page to draw. Keys are Settings' JSON names. It's
// the same shape an emulator's own settings have (emulators.Setting), so one form draws both.
type Field struct {
	Key     string   `json:"key"`
	Kind    string   `json:"kind"` // toggle | number | text | choice | map
	Label   string   `json:"label"`
	Help    string   `json:"help,omitempty"`
	Default any      `json:"default"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Choices []Choice `json:"choices,omitempty"`
	// Scope is who may set it. Every setting of the backend is the administrator's.
	Scope string `json:"scope"`
	// MapOf and MapKeys describe a map: what each value is, and the keys it may have.
	MapOf   string   `json:"mapOf,omitempty"`
	MapKeys []Choice `json:"mapKeys,omitempty"`
}

// Schema is the settings schema of the Docker backend, in the order a page shows it. emus are
// the emulators the per-emulator maps may name: their ids and names.
func Schema(emus []Choice) []Field {
	bs := []Choice{
		{string(BackendDockerWebRTC), "In a container, streamed (WebRTC)"},
		{string(BackendDockerKasmVNC), "In a container, as a desktop (KasmVNC)"},
		{string(BackendProcess), "Not offered"},
	}
	f := []Field{
		{Key: "docker.enabled", Kind: "toggle", Label: "Run standalone emulators", Default: false,
			Help: "Off by default. Libretro cores always run in WittyPlay's own sandbox, whatever this says."},
		{Key: "docker.runtime", Kind: "choice", Label: "Where they run", Default: DefaultRuntime(runtime.GOOS),
			Choices: RuntimeChoices(runtime.GOOS),
			Help:    "Reaching a Docker daemon that runs as root is as good as being root on this machine. A rootless daemon, Podman, or the local sandbox isn't."},
		{Key: "docker.network", Kind: "text", Label: "Docker network", Default: "wittyplay"},
		{Key: "docker.internal", Kind: "toggle", Label: "No internet for emulators", Default: false,
			Help: "Makes WittyPlay's network internal. Needs a host that reaches container addresses directly (Linux without Docker Desktop)."},
		{Key: "docker.publishLoopback", Kind: "toggle", Label: "Publish on 127.0.0.1 (Docker Desktop)", Default: false},
		{Key: "docker.maxContainers", Kind: "number", Label: "Emulators at once", Default: DefaultMaxContainers, Min: 0, Max: 32, Step: 1,
			Help: "Each person plays one at a time. 0 means 2."},
		{Key: "docker.memoryMB", Kind: "number", Label: "Memory for each (MB)", Default: DefaultMemoryMB, Min: 0, Max: 64 << 10, Step: 256,
			Help: "0 means 4096. An emulator that needs more than this, or than the machine has, isn't started."},
		{Key: "docker.cpus", Kind: "number", Label: "Processors for each", Default: DefaultCPUs, Min: 0, Max: 64, Step: 0.5},
		{Key: "docker.pids", Kind: "number", Label: "Processes for each", Default: DefaultPids, Min: 0, Max: 65536, Step: 1},
		{Key: "docker.idleMinutes", Kind: "number", Label: "Stop after no input for (minutes)", Default: int(DefaultIdle.Minutes()), Min: 0, Max: 1440, Step: 1},
		{Key: "docker.maxHours", Kind: "number", Label: "Longest session (hours)", Default: int(DefaultLifetime.Hours()), Min: 0, Max: 48, Step: 1},
		{Key: "docker.gpu", Kind: "map", Label: "Graphics card passed through", Default: map[string]bool{}, MapOf: "toggle",
			MapKeys: []Choice{{"DRI", "Intel and AMD (/dev/dri)"}, {"Nvidia", "NVIDIA (its container toolkit)"}},
			Help:    "Off unless you turn it on. A graphics driver is a large thing to hand to a program that reads untrusted files."},
		{Key: "docker.backend", Kind: "map", Label: "How each emulator is shown", Default: map[string]Backend{}, MapOf: "choice", Choices: bs, MapKeys: emus},
		{Key: "docker.images", Kind: "map", Label: "Your own image for each (pinned by digest)", Default: map[string]string{}, MapOf: "text", MapKeys: emus,
			Help: "repo/name:tag@sha256:… as pushed to a registry, or sha256:… for an image built on this machine."},
	}
	for i := range f {
		f[i].Scope = "admin"
	}
	return f
}

// Clean checks settings that came from a request and fills in what was left out.
func (s Settings) Clean() (Settings, error) {
	if s.Network == "" {
		s.Network = "wittyplay"
	}
	if s.Runtime == "" {
		s.Runtime = DefaultRuntime(runtime.GOOS)
	}
	if s.Runtime != RuntimeDocker && s.Runtime != RuntimeLocal && s.Runtime != RuntimeHost {
		return s, fmt.Errorf("runtime %q is none of docker, local and host", s.Runtime)
	}
	if strings.ContainsAny(s.Network, "/:@ ") || s.Network == "host" || s.Network == "bridge" || s.Network == "none" || len(s.Network) > 64 {
		return s, fmt.Errorf("network %q can't be WittyPlay's own", s.Network)
	}
	for id, b := range s.Overrides {
		if !validEmuID.MatchString(id) {
			return s, fmt.Errorf("bad emulator id %q", id)
		}
		if !slices.Contains([]Backend{BackendProcess, BackendDockerWebRTC, BackendDockerKasmVNC}, b) {
			return s, fmt.Errorf("backend %q is none an emulator can be given", b)
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
	case s.IdleMinutes < 0 || s.IdleMinutes > 1440 || s.MaxHours < 0 || s.MaxHours > 48:
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
		return "", errors.New("standalone emulators are off; an administrator can turn them on in Settings, Standalone emulators")
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
	if s.Runtime == RuntimeLocal && b != BackendProcess {
		b = BackendLocalWebRTC // the local sandbox has no desktop stream
	}
	if s.Runtime == RuntimeHost && b != BackendProcess {
		b = BackendHostWebRTC // the window itself is captured: an emulator's desktop is its window
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
