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
	"path"
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
	ArchiveTarXz    = "tar.xz"
	ArchiveAppImage = "appimage"
	Archive7z       = "7z"
	// ArchiveDMG is a Mac disk image, opened read-only with the Mac's own hdiutil and copied out
	// under the same rules as any archive. Only a Mac has one.
	ArchiveDMG = "dmg"
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
	// Args is the command line on this platform, without the program, when it differs from the
	// emulator's own Args: {game} is the game's file, {config} the folder its settings files are
	// in (ConfigDir), {bios} the BIOS folder and {settings} where the settings' own arguments go.
	// Without {game} the game goes last.
	Args []string `json:"args,omitempty"`
	// ConfigDir is where, inside the person's own folder for this emulator, this build keeps the
	// files its settings name: "config" when empty. Some builds take a folder on their command
	// line ({config}); others follow HOME or XDG_CONFIG_HOME, which point inside that folder.
	ConfigDir string `json:"configDir,omitempty"`
	// Portable: this build keeps its settings and data beside its own program, whatever it's
	// told, so each person gets a copy of it in their own folder to run (RPCS3 on Windows).
	Portable bool `json:"portable,omitempty"`
	// Rosetta: an Intel-only Mac build, which an Apple silicon Mac runs through Rosetta 2.
	Rosetta bool `json:"rosetta,omitempty"`
}

// ArchUniversal is a Mac build that runs natively on both kinds of processor.
const ArchUniversal = "universal"

// SettingsDir is where this build's settings files are, inside the person's own folder.
func (a *Artifact) SettingsDir() string {
	if a == nil || a.ConfigDir == "" {
		return "config"
	}
	return a.ConfigDir
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
	// Seed are firmware files the emulator writes to (xemu's hard disk image): each is copied
	// from the BIOS folder into the person's own folder the first time, as the BIOS folder is
	// given read-only.
	Seed []string `json:"seed,omitempty"`
	// Place are firmware and keys the emulator looks for inside its own data folder rather than
	// in a BIOS folder: each is copied there from the server's BIOS folder before a start, when
	// the owner has put it there. Nothing is ever fetched.
	Place []Placement `json:"place,omitempty"`
	// Manual: never downloaded; an administrator installs it and adds it (manual.go).
	Manual *Manual `json:"manual,omitempty"`
	// Own: an administrator's own program (own.go), never in the built-in list. Like is the
	// emulator in the list it's a build of, whose settings it takes.
	Own  bool   `json:"own,omitempty"`
	Like string `json:"like,omitempty"`
}

// Base is the id whose settings and controller set-up apply: an own program's Like, or its own.
func (e *Emulator) Base() string {
	if e.Own && e.Like != "" {
		return e.Like
	}
	return e.ID
}

// Placement is one file or folder from the BIOS folder, and where it goes in the person's own
// folder for the emulator.
type Placement struct {
	From string `json:"from"` // in the BIOS folder: a file, or a folder whose contents are copied
	To   string `json:"to"`   // relative to the person's own folder for the emulator
	Note string `json:"note,omitempty"`
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
// A Mac build for both processors fits either; an Intel-only one fits Apple silicon through
// Rosetta 2, when there's nothing native.
func (e *Emulator) For(goos, goarch string) *Artifact {
	for i := range e.Artifacts {
		if e.Artifacts[i].OS == goos && e.Artifacts[i].Arch == goarch {
			return &e.Artifacts[i]
		}
	}
	for i := range e.Artifacts {
		if a := &e.Artifacts[i]; a.OS == goos && goos == "darwin" && a.Arch == ArchUniversal {
			return a
		}
	}
	for i := range e.Artifacts {
		if a := &e.Artifacts[i]; goos == "darwin" && goarch == "arm64" && a.OS == "darwin" && a.Arch == "amd64" && a.Rosetta {
			return a
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

// Find returns an emulator from the built-in list, or an administrator's own program, or nil.
func Find(id string) *Emulator {
	if strings.HasPrefix(id, OwnPrefix) {
		return findOwn(id)
	}
	return findBuiltin(id)
}

func findBuiltin(id string) *Emulator {
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
		if e.Plays(system) {
			out = append(out, e)
		}
	}
	return append(out, ownFor(system)...)
}

// CustomPrefix starts the id of a console an administrator added themselves (WittyPlay's
// custom consoles). Before GameCube, Wii, Wii U, PS3, Xbox and Switch were consoles of WittyPlay's
// own, an administrator added custom-gamecube and the like for an emulator to play; the server
// moves those to the real consoles, and until it has, an emulator still plays them.
const CustomPrefix = "custom-"

// legacyCustom are the names the old documentation gave those custom consoles.
var legacyCustom = map[string]string{"custom-gamecube": "gc", "custom-ngc": "gc"}

// Plays says whether an emulator plays a console's games, by the console's id.
func (e *Emulator) Plays(console string) bool {
	if console == "" {
		return false
	}
	if real, ok := legacyCustom[console]; ok {
		console = real
	}
	return slices.Contains(e.Systems, console) || slices.Contains(e.Systems, strings.TrimPrefix(console, CustomPrefix))
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
		if e.Manual != nil {
			if len(e.Artifacts) > 0 {
				return bad("an emulator added by hand has no downloads")
			}
			if len(e.Manual.Programs) == 0 || e.Manual.Download == "" {
				return bad("an emulator added by hand needs its program's names and its project's page")
			}
			for goos, pats := range e.Manual.Programs {
				for _, pat := range pats {
					if _, err := path.Match(pat, ""); err != nil || strings.ContainsAny(pat, "/\\") || pat == "" {
						return bad("the program name %q for %s isn't a file name", pat, goos)
					}
				}
			}
		}
		var places []Placement
		places = append(places, e.Place...)
		if e.Manual != nil {
			for _, list := range e.Manual.Place {
				places = append(places, list...)
			}
		}
		for _, p := range places {
			if p.From == "" || p.To == "" || !filepath.IsLocal(filepath.FromSlash(p.From)) || !filepath.IsLocal(filepath.FromSlash(p.To)) {
				return bad("a firmware placement %q -> %q isn't inside its folders", p.From, p.To)
			}
		}
		for _, f := range e.Seed {
			if f == "" || !filepath.IsLocal(f) || filepath.Base(f) != f {
				return bad("the firmware file %q to copy isn't a plain name", f)
			}
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
			if !slices.Contains([]string{ArchiveZip, ArchiveTarGz, ArchiveTarXz, ArchiveAppImage, Archive7z, ArchiveDMG}, a.Archive) {
				return bad("archive %q isn't one this reads", a.Archive)
			}
			if !filepath.IsLocal(filepath.FromSlash(a.Entry)) {
				return bad("the entry %q isn't inside what it unpacks to", a.Entry)
			}
			if a.ConfigDir != "" && !filepath.IsLocal(filepath.FromSlash(a.ConfigDir)) {
				return bad("the settings folder %q isn't inside the person's own", a.ConfigDir)
			}
			if a.Archive == ArchiveDMG && a.OS != "darwin" {
				return bad("a disk image is only for a Mac")
			}
			for _, arg := range a.Args {
				if strings.ContainsAny(arg, "\x00\n") {
					return bad("an argument with a line break")
				}
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

// SafeURL is an address as it may be logged or shown: no login, no query, no fragment. What
// can't be parsed is left out altogether.
func SafeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(an address that can't be read)"
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

// URLHost is an address's host alone, for a log.
func URLHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Hostname()
	}
	return ""
}

// CleanURLError takes the login and query out of the address in an error from net/http, which
// quotes the whole address it was asked for.
func CleanURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		c := *ue
		c.URL = SafeURL(ue.URL)
		return &c
	}
	return err
}

// CheckURL says whether an address may be fetched: https, no credentials, on a host listed.
// What it says never repeats a login or a query.
func CheckURL(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // which, unlike the url.Error, doesn't quote the address
		}
		return fmt.Errorf("a bad download address: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("the download address %q isn't https", SafeURL(raw))
	}
	if u.User != nil {
		return fmt.Errorf("the download address %q carries credentials", SafeURL(raw))
	}
	if !slices.Contains(hosts, strings.ToLower(u.Hostname())) {
		return fmt.Errorf("%w: %s", ErrHost, u.Hostname())
	}
	return nil
}
