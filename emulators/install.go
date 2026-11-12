// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits on what one install may take. An emulator's AppImage is a few hundred megabytes at
// most; anything far past that is a mistake or an attack.
const (
	DefaultMaxDownload = 1 << 30 // 1 GiB downloaded
	DefaultMaxUnpacked = 4 << 30 // 4 GiB unpacked
	maxFiles           = 100000  // entries in one archive
)

// Errors a caller shows to people.
var (
	ErrPlaceholder   = errors.New("nobody has checked this emulator's download yet (its hash is a placeholder), so it can't be installed; a developer can allow it for testing")
	ErrNonCommercial = errors.New("this emulator's licence forbids commercial use, so WittyPlay doesn't offer it for download")
	ErrNoArtifact    = errors.New("there's no download of this emulator for this platform; its container image may still run it")
	ErrHash          = errors.New("the download doesn't match the hash it was pinned to")
	ErrTooLarge      = errors.New("the download is larger than any emulator's should be")
	ErrBusy          = errors.New("this emulator is already being installed")
	ErrXZ            = errors.New("tar.xz archives can't be opened by this version")
	ErrNotInstalled  = errors.New("that emulator isn't installed")
)

// Installed is an emulator on this machine.
type Installed struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Dir         string `json:"dir"`   // <dataDir>/emulators/<id>/<version>
	Entry       string `json:"entry"` // the program, relative to Dir
	SHA256      string `json:"sha256"`
	Source      string `json:"source"`
	InstalledAt int64  `json:"installedAt"`
	// Unverified: it was installed with placeholders allowed, and its hash was only recorded.
	Unverified bool `json:"unverified,omitempty"`
}

// Installer installs emulators under DataDir/emulators.
type Installer struct {
	DataDir string
	Client  *http.Client
	Hosts   []string // DefaultHosts if nil
	// AllowPlaceholders installs an entry whose hash says TODO, recording what it got. For
	// developers checking a new entry, never for a server people use.
	AllowPlaceholders bool
	// OnProgress hears how far an install has got. Optional; called from the installing goroutine.
	OnProgress   func(id string, p Progress)
	MaxDownload  int64 // DefaultMaxDownload if 0
	MaxUnpacked  int64 // DefaultMaxUnpacked if 0
	GOOS, GOARCH string
	// Platform, if set, says where emulators run, which isn't always where this program does:
	// a Docker daemon in a virtual machine runs Linux programs whatever the server's own system
	// is. It's asked at each install.
	Platform func() (goos, goarch string)

	mu   sync.Mutex
	busy map[string]bool
}

// Root is the folder everything goes in.
func (in *Installer) Root() string { return filepath.Join(in.DataDir, "emulators") }

func (in *Installer) statePath() string { return filepath.Join(in.Root(), "installed.json") }

func (in *Installer) hosts() []string {
	if in.Hosts != nil {
		return in.Hosts
	}
	return DefaultHosts
}

func (in *Installer) platform() (string, string) {
	if in.Platform != nil {
		if goos, goarch := in.Platform(); goos != "" && goarch != "" {
			return goos, goarch
		}
	}
	goos, goarch := in.GOOS, in.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// Artifact is the download of e for where emulators run, or nil.
func (in *Installer) Artifact(e *Emulator) *Artifact {
	goos, goarch := in.platform()
	return e.For(goos, goarch)
}

// List returns what's installed, by id.
func (in *Installer) List() ([]Installed, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.readState()
	if err != nil {
		return nil, err
	}
	out := make([]Installed, 0, len(st))
	for _, v := range st {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Get returns one installed emulator, or nil.
func (in *Installer) Get(id string) *Installed {
	list, err := in.List()
	if err != nil {
		return nil
	}
	for _, v := range list {
		if v.ID == id {
			return &v
		}
	}
	return nil
}

func (in *Installer) readState() (map[string]Installed, error) {
	st := map[string]Installed{}
	b, err := os.ReadFile(in.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("the list of installed emulators is unreadable: %w", err)
	}
	return st, nil
}

func (in *Installer) writeState(st map[string]Installed) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(in.statePath(), b, 0o644)
}

func (in *Installer) claim(id string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.busy == nil {
		in.busy = map[string]bool{}
	}
	if in.busy[id] {
		return false
	}
	in.busy[id] = true
	return true
}

func (in *Installer) release(id string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	delete(in.busy, id)
}

// Install downloads one emulator from the list, checks its hash before anything is unpacked,
// unpacks it into a staging folder, and moves that into place in one step.
func (in *Installer) Install(ctx context.Context, e *Emulator) (*Installed, error) {
	if e == nil {
		return nil, errors.New("no such emulator")
	}
	if e.Manual != nil {
		return nil, ErrManual
	}
	if !validID.MatchString(e.ID) || !validVersion.MatchString(e.Version) {
		return nil, errors.New("that emulator's name or version can't be a folder name")
	}
	if e.NonCommercial {
		return nil, ErrNonCommercial
	}
	goos, goarch := in.platform()
	art := e.For(goos, goarch)
	if art == nil {
		return nil, ErrNoArtifact
	}
	if art.Placeholder() && !in.AllowPlaceholders {
		return nil, ErrPlaceholder
	}
	if err := CheckURL(art.URL, in.hosts()); err != nil {
		return nil, err
	}
	if !in.claim(e.ID) {
		return nil, ErrBusy
	}
	defer in.release(e.ID)

	dir := filepath.Join(in.Root(), e.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(dir, ".staging-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	// The download is kept beside the staging folder under a name made from the hash it must
	// have, so that a try that was cut short is carried on from, and a part of some other
	// version's file never is. A file nobody has hashed has no such name and always starts again.
	file := filepath.Join(stage, "download")
	keep := !art.Placeholder()
	if keep {
		file = filepath.Join(dir, ".partial-"+art.SHA256[:16])
		in.clearPartials(dir, filepath.Base(file), filepath.Base(stage))
	}
	var progress func(Progress)
	if in.OnProgress != nil {
		progress = func(p Progress) { in.OnProgress(e.ID, p) }
	}
	sum, size, err := in.fetch(ctx, art.URL, file, art.Size, progress)
	if err != nil {
		return nil, err
	}
	if !art.Placeholder() && sum != art.SHA256 {
		_ = os.Remove(file) // nothing of it can be trusted, so nothing of it is kept
		return nil, fmt.Errorf("%w: expected %s, got %s", ErrHash, art.SHA256, sum)
	}
	if keep {
		defer os.Remove(file)
	}
	if progress != nil {
		progress(Progress{Phase: PhaseUnpacking, Got: size, Total: size})
	}
	tree := filepath.Join(stage, "tree")
	if err := os.Mkdir(tree, 0o755); err != nil {
		return nil, err
	}
	limit := in.MaxUnpacked
	if limit <= 0 {
		limit = DefaultMaxUnpacked
	}
	if err := unpack(art.Archive, file, size, tree, art.Entry, limit); err != nil {
		return nil, fmt.Errorf("unpacking %s: %w", e.Name, err)
	}
	if err := markEntry(tree, art.Entry); err != nil {
		return nil, err
	}

	final := filepath.Join(dir, e.Version)
	var old string
	if _, err := os.Lstat(final); err == nil {
		old = filepath.Join(dir, ".old-"+randHex())
		if err := os.Rename(final, old); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(tree, final); err != nil {
		if old != "" {
			_ = os.Rename(old, final)
		}
		return nil, err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}

	rec := Installed{ID: e.ID, Version: e.Version, Dir: final, Entry: filepath.ToSlash(art.Entry), SHA256: sum, Source: art.URL, InstalledAt: time.Now().Unix(), Unverified: art.Placeholder()}
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.readState()
	if err != nil {
		return nil, err
	}
	if prev, ok := st[e.ID]; ok && prev.Dir != final {
		_ = os.RemoveAll(prev.Dir) // another version: one at a time
	}
	st[e.ID] = rec
	if err := in.writeState(st); err != nil {
		return nil, err
	}
	return &rec, nil
}

// clearPartials removes downloads and staging folders left from other versions and other tries.
func (in *Installer) clearPartials(dir string, keep ...string) {
	list, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range list {
		n := d.Name()
		if !slices.Contains(keep, n) && (strings.HasPrefix(n, ".partial-") || strings.HasPrefix(n, ".staging-") || strings.HasPrefix(n, ".old-")) {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
}

// Uninstall removes an emulator and everything under its folder. Saves and settings aren't
// there: they're in each person's own folder.
func (in *Installer) Uninstall(id string) error {
	if !validID.MatchString(id) {
		return errors.New("bad emulator name")
	}
	if !in.claim(id) {
		return ErrBusy
	}
	defer in.release(id)
	in.mu.Lock()
	defer in.mu.Unlock()
	st, err := in.readState()
	if err != nil {
		return err
	}
	rec, ok := st[id]
	if !ok {
		return ErrNotInstalled
	}
	// An emulator the administrator installed themselves is only forgotten: its files are
	// theirs, wherever they put it.
	if rec.Source != SourceManual {
		if err := os.RemoveAll(filepath.Join(in.Root(), id)); err != nil {
			return err
		}
	}
	delete(st, id)
	return in.writeState(st)
}

// Progress is how far an install has got.
type Progress struct {
	Phase string // downloading | verifying | unpacking | done
	Got   int64
	Total int64 // -1: not known
	// Resumed is how much of the download was kept from an earlier try.
	Resumed int64
}

// Phases of an install.
const (
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseUnpacking   = "unpacking"
	PhaseDone        = "done"
)

// fetch brings one file to disk at "to" and returns its SHA-256 in hex and its size. It carries
// on from what an earlier try left there when the server allows (a Range request), and starts
// again when it doesn't. Whatever it carried on from, the hash is taken over the whole file as it
// is on disk at the end, so a part that was tampered with between tries is caught like any other
// bad download. It refuses more than the cap, and anything that isn't want bytes when want is
// known.
func (in *Installer) fetch(ctx context.Context, url, to string, want int64, progress func(Progress)) (string, int64, error) {
	client := http.Client{}
	if in.Client != nil {
		client = *in.Client
	}
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Minute
	}
	hosts := in.hosts()
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("the download redirected too many times")
		}
		if err := CheckURL(r.URL.String(), hosts); err != nil {
			return fmt.Errorf("the download tried to go somewhere else: %w", err)
		}
		return nil
	}
	limit := in.MaxDownload
	if limit <= 0 {
		limit = DefaultMaxDownload
	}
	if want > limit {
		return "", 0, ErrTooLarge
	}
	var have int64
	if st, err := os.Lstat(to); err == nil {
		if !st.Mode().IsRegular() || st.Size() > limit || (want > 0 && st.Size() > want) {
			if err := os.Remove(to); err != nil {
				return "", 0, err
			}
		} else {
			have = st.Size()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, CleanURLError(err)
	}
	if have > 0 && (want <= 0 || have < want) {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
	}
	if want > 0 && have == want {
		// All of it is here from last time: only the hash is left to check.
		sum, err := hashFile(to)
		return sum, have, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, CleanURLError(err)
	}
	defer resp.Body.Close()
	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent && have > 0 &&
		strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(have, 10)+"-"):
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		have = 0
		flags |= os.O_TRUNC
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
		// What's here is no part of what the server has now: start again next time.
		_ = os.Remove(to)
		return "", 0, errors.New("the download changed since it was started; try again")
	default:
		return "", 0, fmt.Errorf("the download server answered %s", resp.Status)
	}
	total := int64(-1)
	if resp.ContentLength >= 0 {
		total = have + resp.ContentLength
	}
	if total > limit || (want > 0 && total >= 0 && total != want) {
		return "", 0, ErrTooLarge
	}
	if want > 0 {
		total = want
	}
	f, err := os.OpenFile(to, flags, 0o600)
	if err != nil {
		return "", 0, err
	}
	var dst io.Writer = f
	if progress != nil {
		progress(Progress{Phase: PhaseDownloading, Got: have, Total: total, Resumed: have})
		dst = &progressWriter{w: f, got: have, lastReport: have, fn: func(got int64) {
			progress(Progress{Phase: PhaseDownloading, Got: got, Total: total, Resumed: have})
		}}
	}
	n, err := io.Copy(dst, io.LimitReader(resp.Body, limit-have+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err // what arrived stays, for the next try to carry on from
	}
	size := have + n
	if size > limit || (want > 0 && size > want) {
		_ = os.Remove(to)
		return "", 0, ErrTooLarge
	}
	if want > 0 && size < want {
		return "", 0, errors.New("the download ended early; try again and it carries on from here")
	}
	if progress != nil {
		progress(Progress{Phase: PhaseVerifying, Got: size, Total: size, Resumed: have})
	}
	sum, err := hashFile(to)
	return sum, size, err
}

func hashFile(name string) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// progressWriter reports bytes written, at most every 1 MiB.
type progressWriter struct {
	w          io.Writer
	got        int64
	lastReport int64
	fn         func(got int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.got += int64(n)
	if p.got-p.lastReport >= 1<<20 || err != nil {
		p.lastReport = p.got
		p.fn(p.got)
	}
	return n, err
}

// writeAtomic replaces a file in one step.
func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+randHex())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randHex() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
