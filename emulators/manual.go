// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Emulators an administrator installs themselves. Some emulators are in the list with
// everything WittyPlay knows about them (their settings, controllers, firmware, command line)
// and no download at all: the Switch's and the Switch 2's. WittyPlay never fetches one; an
// administrator installs it from its own project and points the server at it, and WittyPlay
// checks the program is that emulator and notes its version. From then on it plays like any
// other.

// SourceManual marks an install the administrator made themselves.
const SourceManual = "manual"

// Manual says how to recognise an emulator installed by hand.
type Manual struct {
	// Download is the project's own page, shown to the administrator. Never fetched.
	Download string `json:"download"`
	// Programs are the program's file names on each system (GOOS), as path.Match patterns:
	// "Ryujinx.exe", "ryujinx-*.AppImage".
	Programs map[string][]string `json:"programs"`
	// BundleIDs are a Mac app's CFBundleIdentifier, any of which proves it.
	BundleIDs []string `json:"bundleIds,omitempty"`
	// Look are where people install it, per system, tried in order by Detect. ~ is the home
	// folder; $NAME a variable of the module's environment.
	Look map[string][]string `json:"look,omitempty"`
	// VersionFrom is a regular expression whose first group is the version, matched against the
	// program's file name (a Linux AppImage's name carries it).
	VersionFrom string `json:"versionFrom,omitempty"`
	// Notes are things to say beside it: the state of what it plays.
	Notes string `json:"notes,omitempty"`
	// Beside are files one of which sits beside the program (inside a Mac app, beside its
	// program) when the name and the bundle identifier aren't enough to tell it apart.
	Beside []string `json:"beside,omitempty"`
	// Args, ConfigDir and Portable are, per system, what an artifact says for a download:
	// its command line, where its settings files are in the person's own folder, and whether
	// it keeps its data beside its program (each person then runs a copy of their own).
	Args      map[string][]string `json:"args,omitempty"`
	ConfigDir map[string]string   `json:"configDir,omitempty"`
	Portable  []string            `json:"portable,omitempty"`
	// Place are, per system, the keys and firmware it looks for in its own data folder, copied
	// there from the server's BIOS folder before a start.
	Place map[string][]Placement `json:"place,omitempty"`
}

// ManualArtifact is what a download's artifact would say, for an emulator added by hand on
// goos: its command line, its settings folder and whether it's portable.
func (e *Emulator) ManualArtifact(goos string) *Artifact {
	if e.Manual == nil {
		return nil
	}
	return &Artifact{OS: goos, Args: e.Manual.Args[goos], ConfigDir: e.Manual.ConfigDir[goos], Portable: slices.Contains(e.Manual.Portable, goos)}
}

// Placements are the keys and firmware to copy for goos: the emulator's own, then a manual
// install's for that system. Each is relative to the person's own folder for the emulator.
func (e *Emulator) Placements(goos string) []Placement {
	out := slices.Clone(e.Place)
	if e.Manual != nil {
		out = append(out, e.Manual.Place[goos]...)
	}
	return out
}

// ErrManual: this emulator is installed by hand only.
var ErrManual = errors.New("this emulator is never downloaded: install it yourself from its own project and add it in Settings, Standalone emulators")

// Identified is a program that was checked to be the emulator.
type Identified struct {
	Program string `json:"program"` // the program to run
	Dir     string `json:"dir"`     // the folder it's run from, given read-only
	Entry   string `json:"entry"`   // the program, relative to Dir
	Version string `json:"version,omitempty"`
}

// Identify checks a path is this emulator, on this system: a Mac app bundle (or the program
// inside one) whose bundle identifier is the emulator's, or a program whose name is. It says
// in words what's wrong when it isn't.
func Identify(e *Emulator, p string) (Identified, error) {
	return identify(e, p, runtime.GOOS)
}

// IdentifyFor is Identify for the system the emulator will run on, which for a container is
// Linux whatever the server runs on: a Linux AppImage on a Mac's disk, for Docker Desktop.
func IdentifyFor(e *Emulator, p, goos string) (Identified, error) { return identify(e, p, goos) }

func identify(e *Emulator, p, goos string) (Identified, error) {
	if e.Manual == nil {
		return Identified{}, fmt.Errorf("%s isn't added by hand", e.Name)
	}
	if p == "" || !filepath.IsAbs(p) {
		return Identified{}, errors.New("give the program's full path on this server")
	}
	p = filepath.Clean(p)
	st, err := os.Stat(p)
	if err != nil {
		return Identified{}, fmt.Errorf("nothing is there: %w", err)
	}
	// A Mac app: the bundle, or a program inside it.
	if bundle := appBundle(p); bundle != "" {
		return identifyBundle(e, bundle)
	}
	if st.IsDir() {
		// A folder: the program in it.
		for _, pat := range e.Manual.Programs[goos] {
			if m, _ := filepath.Glob(filepath.Join(p, pat)); len(m) > 0 {
				return identify(e, m[0], goos)
			}
		}
		return Identified{}, fmt.Errorf("there's no %s program in that folder", e.Name)
	}
	if !st.Mode().IsRegular() {
		return Identified{}, errors.New("that isn't a program file")
	}
	base := filepath.Base(p)
	if !matchesAny(e.Manual.Programs[goos], base) {
		return Identified{}, fmt.Errorf("%s isn't %s's program on this system (it's called %s)", base, e.Name, strings.Join(e.Manual.Programs[goos], " or "))
	}
	if goos != "windows" && st.Mode().Perm()&0o111 == 0 {
		return Identified{}, errors.New("that file isn't marked as a program that can run")
	}
	if !besideOK(e, filepath.Dir(p), base) {
		return Identified{}, fmt.Errorf("%s has that name, but nothing beside it says it's %s (%s)", base, e.Name, strings.Join(e.Manual.Beside, " or "))
	}
	id := Identified{Program: p, Dir: filepath.Dir(p), Entry: base}
	if e.Manual.VersionFrom != "" {
		if m := regexp.MustCompile(e.Manual.VersionFrom).FindStringSubmatch(base); len(m) > 1 {
			id.Version = m[1]
		}
	}
	if id.Version == "" {
		id.Version = fileVersion(p)
	}
	return id, nil
}

// besideOK says one of the files that prove the emulator is beside its program, when it names
// any. An AppImage carries them inside, where they can't be seen: its name has to do.
func besideOK(e *Emulator, dir, program string) bool {
	if len(e.Manual.Beside) == 0 || strings.HasSuffix(strings.ToLower(program), ".appimage") {
		return true
	}
	for _, f := range e.Manual.Beside {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			return true
		}
	}
	return false
}

func matchesAny(pats []string, name string) bool {
	for _, pat := range pats {
		if ok, _ := path.Match(strings.ToLower(pat), strings.ToLower(name)); ok {
			return true
		}
	}
	return false
}

// appBundle is the .app folder p is or is inside, or "".
func appBundle(p string) string {
	for d := p; d != filepath.Dir(d); d = filepath.Dir(d) {
		if strings.HasSuffix(strings.ToLower(d), ".app") {
			if st, err := os.Stat(filepath.Join(d, "Contents", "Info.plist")); err == nil && st.Mode().IsRegular() {
				return d
			}
		}
	}
	return ""
}

// identifyBundle reads a Mac app's Info.plist for its identifier, program and version.
func identifyBundle(e *Emulator, bundle string) (Identified, error) {
	info, err := readPlist(filepath.Join(bundle, "Contents", "Info.plist"))
	if err != nil {
		return Identified{}, fmt.Errorf("the app's Info.plist can't be read: %w", err)
	}
	ident, _ := info["CFBundleIdentifier"].(string)
	if !slices.Contains(e.Manual.BundleIDs, ident) {
		return Identified{}, fmt.Errorf("that app is %q, not %s", ident, e.Name)
	}
	exe, _ := info["CFBundleExecutable"].(string)
	if exe == "" || strings.ContainsAny(exe, "/\\") {
		return Identified{}, errors.New("the app doesn't name its program")
	}
	prog := filepath.Join(bundle, "Contents", "MacOS", exe)
	if st, err := os.Stat(prog); err != nil || !st.Mode().IsRegular() {
		return Identified{}, errors.New("the app's program is missing")
	}
	if !besideOK(e, filepath.Dir(prog), exe) {
		return Identified{}, fmt.Errorf("that app is %q, but nothing in it says it's %s", ident, e.Name)
	}
	// Some keep the real version in the long string and leave the short one behind.
	version, _ := info["CFBundleLongVersionString"].(string)
	if version == "" {
		version, _ = info["CFBundleShortVersionString"].(string)
	}
	parent := filepath.Dir(bundle)
	rel, _ := filepath.Rel(parent, prog)
	return Identified{Program: prog, Dir: parent, Entry: filepath.ToSlash(rel), Version: version}, nil
}

// readPlist reads a property list, XML or binary, with the Mac's own plutil.
func readPlist(p string) (map[string]any, error) {
	if b, err := os.ReadFile(p); err == nil && len(b) < 1<<20 {
		if out, ok := xmlPlist(b); ok {
			return out, nil
		}
	}
	out, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", p).Output()
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(out, &m)
}

// xmlPlist reads the string values of an XML property list's top-level dictionary, which is
// all an app's identifier, program and version need.
func xmlPlist(b []byte) (map[string]any, bool) {
	s := string(b)
	if !strings.Contains(s, "<plist") {
		return nil, false
	}
	re := regexp.MustCompile(`<key>([^<]{1,200})</key>\s*<string>([^<]{0,400})</string>`)
	out := map[string]any{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if _, seen := out[m[1]]; !seen {
			out[m[1]] = strings.TrimSpace(m[2])
		}
	}
	return out, len(out) > 0
}

// Detect looks in drop (a folder of the module's own an administrator puts it in: the only
// way in for a server in a container), then, on this system, where people install it, and
// answers the first that is it. goos is the system it will run on.
func Detect(e *Emulator, drop, goos string) (Identified, bool) {
	if e.Manual == nil {
		return Identified{}, false
	}
	if drop != "" {
		if id, err := identify(e, drop, goos); err == nil {
			return id, true
		}
		if entries, err := os.ReadDir(drop); err == nil {
			for _, d := range entries {
				if id, err := identify(e, filepath.Join(drop, d.Name()), goos); err == nil {
					return id, true
				}
			}
		}
	}
	if goos != runtime.GOOS {
		return Identified{}, false
	}
	home, _ := os.UserHomeDir()
	for _, p := range e.Manual.Look[runtime.GOOS] {
		p = os.Expand(p, os.Getenv)
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		matches, _ := filepath.Glob(p)
		for _, m := range matches {
			if id, err := identify(e, m, goos); err == nil {
				return id, true
			}
		}
	}
	return Identified{}, false
}

// RegisterManual records an emulator the administrator installed, as it was identified.
func (in *Installer) RegisterManual(e *Emulator, id Identified) (*Installed, error) {
	if e.Manual == nil {
		return nil, ErrManual
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.readState()
	if err != nil {
		return nil, err
	}
	if prev, ok := st[e.ID]; ok && prev.Source != SourceManual {
		return nil, fmt.Errorf("%s is already installed by WittyPlay", e.Name)
	}
	version := id.Version
	if version == "" {
		version = "unknown"
	}
	rec := Installed{ID: e.ID, Version: version, Dir: id.Dir, Entry: filepath.ToSlash(id.Entry), Source: SourceManual, InstalledAt: time.Now().Unix()}
	st[e.ID] = rec
	if err := os.MkdirAll(in.Root(), 0o755); err != nil {
		return nil, err
	}
	if err := in.writeState(st); err != nil {
		return nil, err
	}
	return &rec, nil
}

// ManualFile is where an administrator can name their emulators' programs without a page, for
// a server with no screen or in a container: {"ryujinx": "/opt/ryujinx/Ryujinx"}. It's read when
// the module starts.
func (in *Installer) ManualFile() string { return filepath.Join(in.Root(), "manual.json") }

// LoadManualFile registers what the file names, and says what it couldn't.
func (in *Installer) LoadManualFile(goos string) []error {
	b, err := os.ReadFile(in.ManualFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []error{err}
	}
	var want map[string]string
	if err := json.Unmarshal(b, &want); err != nil {
		return []error{fmt.Errorf("%s is unreadable: %w", in.ManualFile(), err)}
	}
	var errs []error
	for idName, p := range want {
		e := Find(idName)
		if e == nil || e.Manual == nil {
			errs = append(errs, fmt.Errorf("%s: no emulator by that name is added by hand", idName))
			continue
		}
		id, err := identify(e, p, goos)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", idName, err))
			continue
		}
		if _, err := in.RegisterManual(e, id); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", idName, err))
		}
	}
	return errs
}
