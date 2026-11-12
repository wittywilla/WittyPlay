// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// An administrator's own emulator program: one already installed on this server, by its owner,
// from wherever they chose. It's how a console WittyPlay offers no emulator for is played (the
// Switch), and how a FreeBSD server uses an emulator from its packages. WittyPlay downloads and
// checks nothing for it, so the page that adds one says so; it runs in the same sandbox as any
// other emulator, and only on this machine (or the local sandbox), never in a container.

// OwnPrefix starts the id of an administrator's own emulator.
const OwnPrefix = "own-"

// Own is what an administrator says about their program.
type Own struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Systems []string `json:"systems"`
	// Program is the program's full path on this server.
	Program string `json:"program"`
	// Args is its command line, with {game}, {config}, {bios} and {settings} as in the list.
	Args []string `json:"args,omitempty"`
	// ConfigDir is where its settings files end up inside each person's folder for it.
	ConfigDir string `json:"configDir,omitempty"`
	// Like says it's a build of an emulator in the list (an rpcs3 from FreeBSD's packages), so
	// that emulator's settings and controller set-up apply to it.
	Like  string `json:"like,omitempty"`
	Added int64  `json:"added"`
}

var (
	ownMu   sync.RWMutex
	ownList []Own
	ownSlug = regexp.MustCompile(`[^a-z0-9]+`)
)

// MaxOwn bounds how many own programs a server keeps.
const MaxOwn = 32

// OwnID makes an id from a name.
func OwnID(name string) string {
	s := strings.Trim(ownSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "emulator"
	}
	return OwnPrefix + s
}

// CheckOwn holds an own program to what can safely be run: a real program file the server can
// see, a name, consoles by WittyPlay's ids, and a command line with no line breaks.
func CheckOwn(o Own, consoleOK func(string) bool) (Own, error) {
	o.Name = strings.TrimSpace(o.Name)
	if o.Name == "" || utf8.RuneCountInString(o.Name) > 60 || strings.ContainsAny(o.Name, "\x00\r\n") {
		return o, errors.New("give it a name of up to 60 characters")
	}
	if o.ID == "" {
		o.ID = OwnID(o.Name)
	}
	if !strings.HasPrefix(o.ID, OwnPrefix) || !validID.MatchString(o.ID) {
		return o, errors.New("that isn't an own program's id")
	}
	if len(o.Systems) == 0 || len(o.Systems) > 8 {
		return o, errors.New("choose the consoles it plays, up to eight")
	}
	for _, s := range o.Systems {
		if consoleOK != nil && !consoleOK(s) {
			return o, fmt.Errorf("%q isn't a console on this server", s)
		}
	}
	if !filepath.IsAbs(o.Program) || filepath.Clean(o.Program) != o.Program {
		return o, errors.New("give the program's full path on this server")
	}
	st, err := os.Stat(o.Program)
	if err != nil {
		return o, fmt.Errorf("the program isn't there: %w", err)
	}
	if !st.Mode().IsRegular() {
		return o, errors.New("that path isn't a program file")
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0 {
		return o, errors.New("that file isn't marked as a program that can run")
	}
	if len(o.Args) > 32 {
		return o, errors.New("too many arguments")
	}
	for _, a := range o.Args {
		if len(a) > 512 || strings.ContainsAny(a, "\x00\r\n") {
			return o, errors.New("an argument is too long or has a line break")
		}
	}
	if o.ConfigDir != "" && !filepath.IsLocal(filepath.FromSlash(o.ConfigDir)) {
		return o, errors.New("the settings folder must be inside each person's own folder")
	}
	if o.Like != "" && findBuiltin(o.Like) == nil {
		return o, fmt.Errorf("there's no emulator %q in the list", o.Like)
	}
	if o.Added == 0 {
		o.Added = time.Now().Unix()
	}
	return o, nil
}

// SetOwn makes these the administrator's own programs, which Find and ForSystem then know.
func SetOwn(list []Own) {
	ownMu.Lock()
	ownList = slices.Clone(list)
	ownMu.Unlock()
}

// OwnList is the administrator's own programs.
func OwnList() []Own {
	ownMu.RLock()
	defer ownMu.RUnlock()
	return slices.Clone(ownList)
}

// Emulator is an own program as a list entry: no downloads, nothing to pin, its own command line
// as this machine's one artifact, and the settings of the emulator it's like.
func (o Own) Emulator() Emulator {
	e := Emulator{ID: o.ID, Name: o.Name, Systems: o.Systems, License: "Unknown (your own program)", Upstream: "", Version: "own",
		Display: DisplayX11, Stream: StreamPipe, Args: o.Args, Own: true, Image: Image{Digest: Placeholder}}
	if like := findBuiltin(o.Like); like != nil {
		e.Settings, e.Requires, e.Players, e.Bios, e.NeedsBios, e.Like = like.Settings, like.Requires, like.Players, like.Bios, like.NeedsBios, like.ID
		if len(e.Args) == 0 {
			e.Args = like.Args
		}
	}
	e.Artifacts = []Artifact{{OS: runtime.GOOS, Arch: runtime.GOARCH, Archive: "own", Entry: filepath.Base(o.Program), ConfigDir: o.ConfigDir, Args: o.Args}}
	return e
}

// Installed is an own program as the installer records one: its folder and its file.
func (o Own) Installed() Installed {
	return Installed{ID: o.ID, Version: "own", Dir: filepath.Dir(o.Program), Entry: filepath.Base(o.Program), Source: "own", InstalledAt: o.Added}
}

func findOwn(id string) *Emulator {
	ownMu.RLock()
	defer ownMu.RUnlock()
	for _, o := range ownList {
		if o.ID == id {
			e := o.Emulator()
			return &e
		}
	}
	return nil
}

func ownFor(system string) []Emulator {
	ownMu.RLock()
	defer ownMu.RUnlock()
	var out []Emulator
	for _, o := range ownList {
		if slices.Contains(o.Systems, system) {
			out = append(out, o.Emulator())
		}
	}
	return out
}

// LoadOwn reads the own programs kept in a file; a missing file is none.
func LoadOwn(path string) ([]Own, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Own
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("the list of your own emulator programs is unreadable: %w", err)
	}
	if len(list) > MaxOwn {
		list = list[:MaxOwn]
	}
	return list, nil
}

// SaveOwn keeps the own programs in a file.
func SaveOwn(path string, list []Own) error {
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, b, 0o600)
}
