// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Defaults for a session's container. An administrator can lower or raise the limits; nothing
// can turn the hardening off.
const (
	DefaultUID       = 1000
	DefaultPids      = 512
	DefaultMemoryMB  = 4096
	DefaultCPUs      = 2.0
	DefaultShmMB     = 512 // Chromium-style GPU stacks and PulseAudio want a real /dev/shm
	DefaultTmpMB     = 256
	DefaultAppMB     = 1536 // an unpacked AppImage: the largest listed is a few hundred MB packed
	DefaultStop      = 20 * time.Second
	ContainerROM     = "/game"     // the game file, read-only
	ContainerSaves   = "/saves"    // this person's saves and settings for this emulator
	ContainerBIOS    = "/bios"     // firmware the person provided, read-only
	ContainerEmuHome = "/home/emu" // HOME, which is a tmpfs: config is linked into /saves
	// ContainerProgram is an emulator installed by the emulators package, read-only, for an
	// image that runs the program it's given rather than one of its own.
	ContainerProgram = "/opt/emulator"
	// ContainerAppRun is where an AppImage unpacks itself (--appimage-extract-and-run): the one
	// place with exec, as FUSE isn't available to an unprivileged container.
	ContainerAppRun = "/run/app"
)

// Request is what a session asks for. Paths are on the host and must be absolute; Build checks
// every one.
type Request struct {
	SessionID string
	UserID    int64
	Emulator  string // the id, for the label
	Image     string // pinned reference: repo@sha256:...
	ROM       string // a file
	SaveDir   string // this person's folder for this emulator
	BIOSDir   string // optional
	// ProgramDir is an installed emulator's folder, for an image without one built in. Optional.
	ProgramDir string
	// AppImage says the program in ProgramDir is an AppImage. Only then does the container get
	// ContainerAppRun, the one tmpfs where files may be executed (still nosuid and nodev), for
	// the AppImage to unpack itself into: FUSE isn't available to an unprivileged container.
	// It needs ProgramDir.
	AppImage bool
	Cmd      []string
	Env      []string

	Stream string // "pipe" or "kasmvnc"
	// Network is WittyPlay's bridge network. Empty means "none": no network at all, which
	// leaves only the pipe stream's RTP impossible, so Build refuses that combination.
	Network string
	// PublishLoopback publishes the stream port on 127.0.0.1, for a host that can't reach a
	// container's address on the network (Docker Desktop). Never on another address.
	PublishLoopback bool
	// RTPHost is where the in-container FFmpeg sends RTP: the host's address on Network.
	RTPHost   string
	RTPVideo  int
	RTPAudio  int
	KasmToken string // per-session secret the container's KasmVNC requires
	// InputToken is the per-session secret the input agent requires: inputagent.
	InputToken string

	Limits Limits
	GPU    GPU
}

// Limits on one container.
type Limits struct {
	UID      int
	Pids     int64
	MemoryMB int64
	CPUs     float64
	ShmMB    int64
	TmpMB    int64
	AppMB    int64 // the size of ContainerAppRun, for an AppImage
}

// GPU is optional passthrough, off unless an administrator turns it on.
type GPU struct {
	DRI    bool // /dev/dri: Intel and AMD, and the Mesa stack in the image
	Nvidia bool // the NVIDIA container runtime's device request
}

var (
	validSession = regexp.MustCompile(`^[a-f0-9]{8,64}$`)
	validEmuID   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	pinnedImage  = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?@sha256:[0-9a-f]{64}$`)
	envKey       = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// envDenied are variables a caller may not set: they would change what the loader does or
// where the program looks for its libraries.
var envDenied = []string{"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT", "PATH", "HOME", "USER"}

// ErrNotPinned is an image without a digest. Tags move; a digest doesn't.
var ErrNotPinned = errors.New("the container image isn't pinned to a digest")

// Build makes the hardened Spec for one session. Everything a container gets is decided here,
// in one place, and a test checks every field.
func Build(r Request) (Spec, error) {
	if !validSession.MatchString(r.SessionID) {
		return Spec{}, errors.New("bad session id")
	}
	if !validEmuID.MatchString(r.Emulator) {
		return Spec{}, errors.New("bad emulator id")
	}
	if !pinnedImage.MatchString(r.Image) {
		return Spec{}, ErrNotPinned
	}
	for name, p := range map[string]string{"game": r.ROM, "save folder": r.SaveDir} {
		if err := hostPath(p); err != nil {
			return Spec{}, fmt.Errorf("the %s: %w", name, err)
		}
	}
	if r.BIOSDir != "" {
		if err := hostPath(r.BIOSDir); err != nil {
			return Spec{}, fmt.Errorf("the BIOS folder: %w", err)
		}
	}
	if r.Stream != "pipe" && r.Stream != "kasmvnc" {
		return Spec{}, fmt.Errorf("stream %q is neither pipe nor kasmvnc", r.Stream)
	}
	if r.Network == "" || r.Network == "none" {
		return Spec{}, errors.New("a stream needs WittyPlay's own network; none was given")
	}
	if r.Network == "host" || r.Network == "bridge" || strings.HasPrefix(r.Network, "container:") {
		return Spec{}, fmt.Errorf("network %q is shared with more than this session", r.Network)
	}
	l := r.Limits.withDefaults()
	if l.UID <= 0 {
		return Spec{}, errors.New("a container never runs as root")
	}

	env := []string{
		"HOME=" + ContainerEmuHome,
		"XDG_CONFIG_HOME=" + ContainerSaves + "/config",
		"XDG_DATA_HOME=" + ContainerSaves + "/data",
		"XDG_RUNTIME_DIR=/tmp/runtime",
		"DISPLAY=:1",
		"PULSE_SERVER=unix:/tmp/runtime/pulse/native",
		"WITTYPLAY_STREAM=" + r.Stream,
		"WITTYPLAY_ROM=" + ContainerROM + "/" + filepath.Base(r.ROM),
		"WITTYPLAY_INPUT_PORT=" + strconv.Itoa(InputPort),
	}
	if r.BIOSDir != "" {
		env = append(env, "WITTYPLAY_BIOS="+ContainerBIOS)
	}
	if len(r.InputToken) < 32 {
		return Spec{}, errors.New("the input agent needs a per-session secret")
	}
	env = append(env, "WITTYPLAY_INPUT_TOKEN="+r.InputToken)
	switch r.Stream {
	case "pipe":
		if r.RTPHost == "" || r.RTPVideo <= 0 || r.RTPAudio <= 0 {
			return Spec{}, errors.New("the pipe stream needs somewhere to send its RTP")
		}
		env = append(env, "WITTYPLAY_RTP_HOST="+r.RTPHost, "WITTYPLAY_RTP_VIDEO="+strconv.Itoa(r.RTPVideo), "WITTYPLAY_RTP_AUDIO="+strconv.Itoa(r.RTPAudio))
	case "kasmvnc":
		if len(r.KasmToken) < 32 {
			return Spec{}, errors.New("KasmVNC needs a per-session secret")
		}
		env = append(env, "WITTYPLAY_KASM_PORT="+strconv.Itoa(KasmPort), "WITTYPLAY_KASM_TOKEN="+r.KasmToken)
	}
	for _, kv := range r.Env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || !envKey.MatchString(k) || slices.Contains(envDenied, k) || strings.HasPrefix(k, "WITTYPLAY_") {
			return Spec{}, fmt.Errorf("the variable %q can't be set", k)
		}
		env = append(env, kv)
	}

	mounts := []Mount{
		{Source: r.ROM, Target: ContainerROM + "/" + filepath.Base(r.ROM), ReadOnly: true},
		{Source: r.SaveDir, Target: ContainerSaves, ReadOnly: false},
	}
	if r.BIOSDir != "" {
		mounts = append(mounts, Mount{Source: r.BIOSDir, Target: ContainerBIOS, ReadOnly: true})
	}
	if r.ProgramDir != "" {
		if err := hostPath(r.ProgramDir); err != nil {
			return Spec{}, fmt.Errorf("the emulator's folder: %w", err)
		}
		mounts = append(mounts, Mount{Source: r.ProgramDir, Target: ContainerProgram, ReadOnly: true})
	}
	if r.AppImage && r.ProgramDir == "" {
		return Spec{}, errors.New("an AppImage needs the emulator's folder")
	}

	s := Spec{
		Name:  "wittyplay-" + r.SessionID[:min(16, len(r.SessionID))],
		Image: r.Image,
		User:  strconv.Itoa(l.UID) + ":" + strconv.Itoa(l.UID),
		Env:   env,
		Cmd:   slices.Clone(r.Cmd),
		Labels: map[string]string{
			LabelManaged: "true",
			LabelSession: r.SessionID,
			LabelUser:    strconv.FormatInt(r.UserID, 10),
			LabelEmu:     r.Emulator,
		},
		ReadOnlyRoot: true,
		CapDrop:      []string{"ALL"},
		// Docker applies its default seccomp profile unless told otherwise; it's named here so
		// the rule is written down, and no "seccomp=unconfined" can sneak in beside it.
		SecurityOpt: []string{"no-new-privileges:true"},
		Privileged:  false,
		Init:        true,
		PidsLimit:   l.Pids,
		MemoryBytes: l.MemoryMB << 20,
		NanoCPUs:    int64(l.CPUs * 1e9),
		ShmBytes:    l.ShmMB << 20,
		Tmpfs: map[string]string{
			"/tmp":           fmt.Sprintf("rw,noexec,nosuid,nodev,size=%dm,mode=1777", l.TmpMB),
			ContainerEmuHome: fmt.Sprintf("rw,noexec,nosuid,nodev,size=64m,uid=%d,gid=%d,mode=0700", l.UID, l.UID),
		},
		Mounts:      mounts,
		Network:     r.Network,
		StopTimeout: DefaultStop,
	}
	if r.AppImage {
		// The only exec-allowed tmpfs, and only for an AppImage. TMPDIR points the AppImage
		// runtime's --appimage-extract-and-run here.
		s.Tmpfs[ContainerAppRun] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm,uid=%d,gid=%d,mode=0700", l.AppMB, l.UID, l.UID)
		s.Env = append(s.Env, "APPIMAGE_EXTRACT_AND_RUN=1", "TMPDIR="+ContainerAppRun)
	}
	if r.PublishLoopback {
		port := KasmPort
		if r.Stream == "pipe" {
			port = InputPort
		}
		s.Publish = []Publish{{Port: port, Proto: "tcp", HostIP: netip.MustParseAddr("127.0.0.1")}}
		if r.Stream == "kasmvnc" {
			s.Publish = append(s.Publish, Publish{Port: InputPort, Proto: "tcp", HostIP: netip.MustParseAddr("127.0.0.1")})
		}
	}
	if r.GPU.DRI {
		s.Devices = []string{"/dev/dri"}
	}
	s.NvidiaGPUs = r.GPU.Nvidia
	return s, nil
}

func (l Limits) withDefaults() Limits {
	if l.UID == 0 {
		l.UID = DefaultUID
	}
	if l.Pids <= 0 {
		l.Pids = DefaultPids
	}
	if l.MemoryMB <= 0 {
		l.MemoryMB = DefaultMemoryMB
	}
	if l.CPUs <= 0 {
		l.CPUs = DefaultCPUs
	}
	if l.ShmMB <= 0 {
		l.ShmMB = DefaultShmMB
	}
	if l.TmpMB <= 0 {
		l.TmpMB = DefaultTmpMB
	}
	if l.AppMB <= 0 {
		l.AppMB = DefaultAppMB
	}
	return l
}

// hostPath checks a path that will be mounted: absolute, clean, and not one of the places that
// would hand a container the machine.
func hostPath(p string) error {
	if p == "" || !filepath.IsAbs(p) {
		return errors.New("isn't an absolute path")
	}
	if filepath.Clean(p) != p {
		return errors.New("isn't a clean path")
	}
	for _, bad := range []string{"/", "/etc", "/proc", "/sys", "/dev", "/var/run", "/run", "/boot", "/root"} {
		if p == bad || strings.HasPrefix(p, bad+"/") && (bad == "/proc" || bad == "/sys" || bad == "/dev") {
			return fmt.Errorf("%s can't be given to a container", p)
		}
	}
	if strings.Contains(p, "docker.sock") {
		return errors.New("the Docker socket is never given to a container")
	}
	return nil
}

// Check says what's wrong with a Spec, if anything: the same rules Build follows, for a Spec
// made some other way. The engine runs it before every create, so nothing weaker reaches Docker.
func Check(s Spec) error {
	switch {
	case s.User == "" || s.User == "0" || strings.HasPrefix(s.User, "0:") || strings.HasPrefix(s.User, "root"):
		return errors.New("a container never runs as root")
	case !s.ReadOnlyRoot:
		return errors.New("the root filesystem must be read-only")
	case !slices.Equal(s.CapDrop, []string{"ALL"}) || len(s.CapAdd) > 0:
		return errors.New("every capability must be dropped, and none added")
	case !slices.Contains(s.SecurityOpt, "no-new-privileges:true"):
		return errors.New("no-new-privileges must be set")
	case s.Privileged:
		return errors.New("a container is never privileged")
	case s.PidsLimit <= 0 || s.MemoryBytes <= 0 || s.NanoCPUs <= 0:
		return errors.New("a container needs pids, memory and CPU limits")
	case s.Network == "" || s.Network == "host" || s.Network == "bridge":
		return fmt.Errorf("network %q isn't WittyPlay's own", s.Network)
	case s.Labels[LabelManaged] != "true" || s.Labels[LabelSession] == "":
		return errors.New("a container must carry WittyPlay's labels")
	case !pinnedImage.MatchString(s.Image):
		return ErrNotPinned
	}
	for _, o := range s.SecurityOpt {
		if strings.Contains(o, "unconfined") || strings.HasPrefix(o, "seccomp") || strings.HasPrefix(o, "apparmor") {
			return fmt.Errorf("security option %q weakens the default profile", o)
		}
	}
	if _, ok := s.Tmpfs["/tmp"]; !ok {
		return errors.New("/tmp must be a tmpfs")
	}
	for target, opts := range s.Tmpfs {
		o := strings.Split(opts, ",")
		if !slices.Contains(o, "nosuid") || !slices.Contains(o, "nodev") {
			return fmt.Errorf("tmpfs %s must be nosuid and nodev", target)
		}
		if target != ContainerAppRun && !slices.Contains(o, "noexec") {
			return fmt.Errorf("tmpfs %s must be noexec; only %s may run programs", target, ContainerAppRun)
		}
	}
	for _, p := range s.Publish {
		if p.HostIP != netip.MustParseAddr("127.0.0.1") {
			return errors.New("a port is only ever published on 127.0.0.1")
		}
	}
	for _, m := range s.Mounts {
		if err := hostPath(m.Source); err != nil {
			return fmt.Errorf("mount %s: %w", m.Source, err)
		}
		if m.Target == ContainerROM || strings.HasPrefix(m.Target, ContainerROM+"/") || m.Target == ContainerBIOS || m.Target == ContainerProgram {
			if !m.ReadOnly {
				return fmt.Errorf("%s must be read-only", m.Target)
			}
		}
	}
	for _, d := range s.Devices {
		if d != "/dev/dri" {
			return fmt.Errorf("device %s isn't one WittyPlay passes through", d)
		}
	}
	return nil
}

// Pinned says an image reference names a digest.
func Pinned(ref string) bool { return pinnedImage.MatchString(ref) }
