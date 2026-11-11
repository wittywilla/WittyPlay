// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package emulators is the list of standalone emulators WittyPlay knows, and what installs one.
// A standalone emulator is a desktop program, not a libretro core: PCSX2, Dolphin, RPCS3 and the
// like. It never runs in the module's own process or the game runner. It runs in a container
// (sandbox/docker), and only when an administrator has turned the Docker backend on.
//
// The list is built in. Each download is pinned to a SHA-256, and an entry whose hash nobody has
// checked yet says "TODO" and is refused at install time. Nothing here ever downloads a BIOS,
// firmware, keys or games: the person running the server brings those.
package emulators

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// Placeholder is what a hash or a digest says when nobody has checked the file yet.
const Placeholder = "TODO"

// How a program shows its picture.
const (
	DisplayX11      = "x11"
	DisplayWayland  = "wayland"
	DisplayHeadless = "headless"
)

// How its picture reaches a browser.
const (
	// StreamPipe: FFmpeg inside the container captures the display and the sound and sends RTP
	// to WittyPlay, which hands it to the same WebRTC session a libretro game has.
	StreamPipe = "pipe"
	// StreamKasmVNC: the container's KasmVNC, reached only through WittyPlay's authenticated
	// proxy. The fallback, for a program whose setup needs a desktop: a mouse, its own menus.
	StreamKasmVNC = "kasmvnc"
)

// Archive kinds.
const (
	ArchiveZip      = "zip"
	ArchiveTarGz    = "tar.gz"
	ArchiveTarXz    = "tar.xz" // listed so an entry can say it; the standard library can't open one
	ArchiveAppImage = "appimage"
)

// Artifact is one download of an emulator, for one platform.
type Artifact struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Size is the file's size in bytes, when the list knows it. A download of any other size
	// is refused before it is hashed.
	Size    int64  `json:"size,omitempty"`
	Archive string `json:"archive"`
	// Entry is the program to run, inside what the archive unpacks to.
	Entry string `json:"entry"`
}

// Placeholder says nobody has checked this file's hash.
func (a Artifact) Placeholder() bool { return a.SHA256 == Placeholder }

// Image is the container image an emulator runs in.
type Image struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"` // sha256:..., or Placeholder
}

// Pinned is the reference with its digest, which is what gets pulled, or "" when there's no
// digest to pin to yet.
func (i Image) Pinned() string {
	if i.Digest == "" || i.Digest == Placeholder {
		return ""
	}
	ref := i.Ref
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	return ref + "@" + i.Digest
}

// Emulator is one standalone emulator.
type Emulator struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Systems []string `json:"systems"`
	License string   `json:"license"`
	// LicenseURL is where the licence's text is, for the page that shows it before an install.
	LicenseURL string `json:"licenseUrl,omitempty"`
	Upstream   string `json:"upstream"`
	Version    string `json:"version"`
	// Requires is the least a machine needs to run it at all. A machine with less is told so
	// instead of being left to crawl.
	Requires Requires `json:"requires,omitzero"`
	// Players is how many controllers it takes; 0 means one.
	Players int `json:"players,omitempty"`
	// Bios names the firmware it looks for in the person's BIOS folder. Names may hold * and ?.
	Bios []BiosFile `json:"bios,omitempty"`
	// Settings are what a page offers for this emulator, and how each is given to it: settings.go.
	Settings []Setting `json:"settings,omitempty"`
	// NonCommercial: its licence forbids commercial use. WittyPlay never offers those for
	// download, as with cores; an administrator can still build an image of their own.
	NonCommercial bool `json:"nonCommercial,omitempty"`
	// Experimental: its own authors call it early work. Pages say so beside it.
	Experimental bool       `json:"experimental,omitempty"`
	Artifacts    []Artifact `json:"artifacts"`
	Display      string     `json:"display"`
	Stream       string     `json:"stream"`
	// Args come before the game's path on the program's command line.
	Args []string `json:"args,omitempty"`
	// NeedsBios: it won't start a game without firmware of the person's own.
	NeedsBios bool  `json:"needsBios,omitempty"`
	Image     Image `json:"image"`
}

// Requires is the least an emulator needs.
type Requires struct {
	MemoryMB int64   `json:"memoryMB,omitempty"`
	CPUs     float64 `json:"cpus,omitempty"`
	GPU      bool    `json:"gpu,omitempty"` // unplayably slow without a graphics card passed through
}

// BiosFile is firmware an emulator looks for. WittyPlay never downloads one.
type BiosFile struct {
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

// For returns the artifact for a platform, or nil.
func (e *Emulator) For(goos, goarch string) *Artifact {
	for i := range e.Artifacts {
		if e.Artifacts[i].OS == goos && e.Artifacts[i].Arch == goarch {
			return &e.Artifacts[i]
		}
	}
	return nil
}

// Here returns the artifact for this machine, or nil.
func (e *Emulator) Here() *Artifact { return e.For(runtime.GOOS, runtime.GOARCH) }

// Manifest is the whole list.
type Manifest struct {
	Schema    int        `json:"schema"`
	Note      string     `json:"note,omitempty"`
	Emulators []Emulator `json:"emulators"`
}

//go:embed manifest.json
var manifestJSON []byte

var (
	builtinOnce sync.Once
	builtin     *Manifest
	builtinErr  error
)

// Builtin is the list that comes with this version. A test holds it valid, so an error here
// is a build that shouldn't have been released.
func Builtin() (*Manifest, error) {
	builtinOnce.Do(func() { builtin, builtinErr = Parse(manifestJSON) })
	return builtin, builtinErr
}

// Find returns an emulator from the built-in list, or nil.
func Find(id string) *Emulator {
	m, err := Builtin()
	if err != nil {
		return nil
	}
	for i := range m.Emulators {
		if m.Emulators[i].ID == id {
			e := m.Emulators[i]
			return &e
		}
	}
	return nil
}

// ForSystem lists the emulators that can play a console.
func ForSystem(system string) []Emulator {
	m, err := Builtin()
	if err != nil {
		return nil
	}
	var out []Emulator
	for _, e := range m.Emulators {
		if slices.Contains(e.Systems, system) {
			out = append(out, e)
		}
	}
	return out
}

// Parse reads a manifest and checks it.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("the emulator list is unreadable: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

var (
	validID      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	validVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	validHash    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	validDigest  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	validImage   = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(:[0-9]+)?(/[a-z0-9]+([._-][a-z0-9]+)*)+(:[A-Za-z0-9_][A-Za-z0-9._-]{0,127})?$`)
)

// Validate checks every entry: names, platforms, hosts, hashes and digests. A hash or digest may
// be Placeholder, and nothing else that isn't one.
func (m *Manifest) Validate() error {
	if m.Schema != 1 {
		return fmt.Errorf("the emulator list is schema %d; this version reads 1", m.Schema)
	}
	seen := map[string]bool{}
	for _, e := range m.Emulators {
		bad := func(format string, a ...any) error {
			return fmt.Errorf("emulator %q: %s", e.ID, fmt.Sprintf(format, a...))
		}
		if !validID.MatchString(e.ID) {
			return bad("the id isn't a plain name")
		}
		if seen[e.ID] {
			return bad("listed twice")
		}
		seen[e.ID] = true
		if e.Name == "" || e.License == "" || len(e.Systems) == 0 {
			return bad("needs a name, a licence and at least one console")
		}
		if e.Version != Placeholder && !validVersion.MatchString(e.Version) {
			return bad("the version %q can't be a folder name", e.Version)
		}
		if u, err := url.Parse(e.Upstream); err != nil || u.Scheme != "https" || u.Host == "" {
			return bad("the upstream address must be https")
		}
		if !slices.Contains([]string{DisplayX11, DisplayWayland, DisplayHeadless}, e.Display) {
			return bad("display %q is none of x11, wayland or headless", e.Display)
		}
		if !slices.Contains([]string{StreamPipe, StreamKasmVNC}, e.Stream) {
			return bad("stream %q is neither pipe nor kasmvnc", e.Stream)
		}
		if e.Stream == StreamKasmVNC && e.Display != DisplayX11 {
			return bad("KasmVNC shows an X display, so it needs display x11")
		}
		if !validImage.MatchString(e.Image.Ref) || strings.Contains(e.Image.Ref, "@") {
			return bad("the image reference %q isn't a plain registry/name:tag", e.Image.Ref)
		}
		if e.Image.Digest != Placeholder && !validDigest.MatchString(e.Image.Digest) {
			return bad("the image digest must be sha256:<64 hex> or %s", Placeholder)
		}
		if e.LicenseURL != "" {
			if u, err := url.Parse(e.LicenseURL); err != nil || u.Scheme != "https" || u.Host == "" {
				return bad("the licence address must be https")
			}
		}
		if e.Players < 0 || e.Players > 4 || e.Requires.MemoryMB < 0 || e.Requires.CPUs < 0 {
			return bad("players is 0 to 4, and requirements can't be negative")
		}
		for _, b := range e.Bios {
			if b.Name == "" || strings.ContainsAny(b.Name, "/\\") {
				return bad("a BIOS file is named by its name alone: %q", b.Name)
			}
		}
		if err := checkSettings(e.Settings); err != nil {
			return bad("%v", err)
		}
		plat := map[string]bool{}
		for _, a := range e.Artifacts {
			if a.OS == "" || a.Arch == "" {
				return bad("an artifact without a platform")
			}
			if plat[a.OS+"/"+a.Arch] {
				return bad("two artifacts for %s/%s", a.OS, a.Arch)
			}
			plat[a.OS+"/"+a.Arch] = true
			if err := CheckURL(a.URL, DefaultHosts); err != nil {
				return bad("%v", err)
			}
			if a.SHA256 != Placeholder && !validHash.MatchString(a.SHA256) {
				return bad("the sha256 for %s/%s must be 64 lowercase hex or %s", a.OS, a.Arch, Placeholder)
			}
			if a.Size < 0 {
				return bad("a negative size for %s/%s", a.OS, a.Arch)
			}
			if !slices.Contains([]string{ArchiveZip, ArchiveTarGz, ArchiveTarXz, ArchiveAppImage}, a.Archive) {
				return bad("archive %q isn't one this reads", a.Archive)
			}
			if !filepath.IsLocal(filepath.FromSlash(a.Entry)) {
				return bad("the entry %q isn't inside what it unpacks to", a.Entry)
			}
		}
	}
	return nil
}

// Placeholders lists what in the list still says TODO, for a report.
func (m *Manifest) Placeholders() []string {
	var out []string
	for _, e := range m.Emulators {
		if e.Version == Placeholder {
			out = append(out, e.ID+": version")
		}
		if e.Image.Digest == Placeholder {
			out = append(out, e.ID+": image digest")
		}
		for _, a := range e.Artifacts {
			if a.Placeholder() {
				out = append(out, e.ID+": sha256 for "+a.OS+"/"+a.Arch)
			}
		}
	}
	return out
}

// DefaultHosts are where an emulator may be downloaded from: GitHub's release assets, and the
// projects' own sites. A redirect anywhere else is refused, as cores' downloads are.
var DefaultHosts = []string{
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
	"pcsx2.net",
	"dl.dolphin-emu.org",
	"www.duckstation.org",
	"www.ppsspp.org",
	"xemu.app",
	"cemu.info",
	"rpcs3.net",
	"melonds.kuribo64.net",
}

// ErrHost is a download address on no list.
var ErrHost = errors.New("that address isn't an approved download host")

// CheckURL says whether an address may be fetched: https, no credentials, on a host listed.
func CheckURL(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("a bad download address: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the download address %q isn't https", raw)
	}
	if u.User != nil {
		return fmt.Errorf("the download address %q carries credentials", raw)
	}
	if !slices.Contains(hosts, strings.ToLower(u.Hostname())) {
		return fmt.Errorf("%w: %s", ErrHost, u.Hostname())
	}
	return nil
}
