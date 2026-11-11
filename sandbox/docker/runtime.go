// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package docker is WittyPlay's optional Docker backend: a standalone emulator (emulators) in a
// container of its own, one per session. The process sandbox (stream, hostsandbox) stays the
// default and is what every libretro core runs in. This backend is off until an administrator
// turns it on, and when Docker can't be reached it says so and nothing else changes.
//
// Only the engine subpackage imports the Docker Engine SDK. Everything else here, and everything
// that uses this package, speaks the small Runtime interface below with this package's own types,
// so tests run against a fake (Fake) and nothing but the module's command links the SDK.
package docker

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// Labels every WittyPlay container carries, so that what a crash left behind can be found and
// removed at the next start, and nothing else on the machine is ever touched.
const (
	LabelManaged = "wittyplay.managed"
	LabelSession = "wittyplay.session"
	LabelUser    = "wittyplay.user"
	LabelEmu     = "wittyplay.emulator"
)

// Ports inside a container.
const (
	KasmPort  = 6901 // KasmVNC's web server, plain HTTP inside the network; only the proxy reaches it
	InputPort = 7000 // the input agent: pad states from WittyPlay, over TCP
)

// ErrUnavailable is Docker not being reachable. Callers carry on with the process sandbox.
var ErrUnavailable = errors.New("Docker isn't available on this server")

// Status says whether Docker can be used.
type Status struct {
	Available  bool   `json:"available"`
	Host       string `json:"host"` // DOCKER_HOST, or the default socket
	APIVersion string `json:"apiVersion,omitempty"`
	OS         string `json:"os,omitempty"`
	// Arch is the machine containers run on, in Go's names (amd64, arm64). On Docker Desktop
	// that's the virtual machine, which is where an emulator's download has to fit.
	Arch string `json:"arch,omitempty"`
	// Rootless: the daemon itself doesn't run as root, so reaching it isn't root on this machine.
	Rootless bool `json:"rootless,omitempty"`
	// CPUs and MemoryMB are what the daemon has to give, for refusing what can't run here.
	CPUs     int    `json:"cpus,omitempty"`
	MemoryMB int64  `json:"memoryMB,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Mount is one folder or file from the host.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
}

// Publish exposes a container port on the host. HostIP is always loopback: Build refuses
// anything else.
type Publish struct {
	Port   int        `json:"port"`
	Proto  string     `json:"proto"` // tcp | udp
	HostIP netip.Addr `json:"hostIp"`
}

// Spec is everything a container is created with. Build makes one; the engine turns it into
// the SDK's types word for word.
type Spec struct {
	Name   string
	Image  string
	User   string // uid:gid, never root
	Env    []string
	Cmd    []string
	Labels map[string]string

	ReadOnlyRoot bool
	CapDrop      []string
	CapAdd       []string
	SecurityOpt  []string
	Privileged   bool
	Init         bool

	PidsLimit   int64
	MemoryBytes int64
	NanoCPUs    int64
	ShmBytes    int64
	Tmpfs       map[string]string

	Mounts     []Mount
	Devices    []string // host device paths mapped at the same path, such as /dev/dri
	NvidiaGPUs bool     // ask the NVIDIA runtime for every GPU

	// Local: this is for the sandbox without Docker (sandbox/local), which has no images and
	// no network at all. Docker's engine refuses such a spec.
	Local bool

	Network    string // a network name; "isolated" for Local
	Publish    []Publish
	ExtraHosts []string

	StopTimeout time.Duration
}

// Info is what's known about a container.
type Info struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Image   string            `json:"image"`
	Running bool              `json:"running"`
	Paused  bool              `json:"paused,omitempty"`
	State   string            `json:"state"`
	Labels  map[string]string `json:"labels,omitempty"`
	// IP is its address on WittyPlay's network.
	IP netip.Addr `json:"ip,omitzero"`
	// HostPorts are published ports, by "6901/tcp", on 127.0.0.1.
	HostPorts map[string]int `json:"hostPorts,omitempty"`
	Created   time.Time      `json:"created"`
}

// NetworkSpec is WittyPlay's own network.
type NetworkSpec struct {
	Name string
	// Internal: no way out of it, and no published ports. The proxy reaches containers at their
	// address on it, which works where the host shares Docker's network (Linux).
	Internal bool
}

// NetworkInfo is the network as it was found or made.
type NetworkInfo struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Internal bool       `json:"internal"`
	Gateway  netip.Addr `json:"gateway,omitzero"` // the host's address on it
}

// Runtime is what WittyPlay asks of Docker. Every call takes a context, and none may block
// past it.
type Runtime interface {
	Available(ctx context.Context) Status
	Pull(ctx context.Context, ref string) error
	Create(ctx context.Context, spec Spec) (id string, err error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, timeout time.Duration) error
	Remove(ctx context.Context, id string) error
	// Pause freezes every process in a container, and Unpause lets them go on.
	Pause(ctx context.Context, id string) error
	Unpause(ctx context.Context, id string) error
	Logs(ctx context.Context, id string, tail int) (string, error)
	Inspect(ctx context.Context, id string) (Info, error)
	// List finds containers carrying all of these labels, running or not.
	List(ctx context.Context, labels map[string]string) ([]Info, error)
	EnsureNetwork(ctx context.Context, n NetworkSpec) (NetworkInfo, error)
}
