// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package cores finds libretro cores on this machine and installs them on an
// administrator's request. A core is someone else's native code, so downloads
// are held to one host, one list of names and one file.
package cores

import (
	"archive/zip"
	"bytes"
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
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wittywilla/wittyplay"
)

// Core is a core file on this machine.
type Core struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	License       string `json:"license"` // "" when we don't know this core
	Path          string `json:"-"`
	Source        string `json:"source"` // installed | retroarch | system
	SHA256        string `json:"sha256,omitempty"`
	Installed     int64  `json:"installedAt,omitempty"`
	NonCommercial bool   `json:"nonCommercial,omitempty"`
	// For custom cores.
	Version       string   `json:"version,omitempty"`
	Consoles      []string `json:"consoles,omitempty"`
	LicenseSource string   `json:"licenseSource,omitempty"` // info | table | admin
	Assets        string   `json:"-"`
	WantsGPU      bool     `json:"-"` // a custom core that reported needing a graphics card
	// Unapproved is a file someone placed that our table doesn't know and nobody has
	// added: it stays listed so it can be, and nothing runs it until then.
	Unapproved bool `json:"unapproved,omitempty"`
}

// Manager looks in one folder it owns, plus folders other programs own.
type Manager struct {
	Dir    string // installs go here; an administrator may also place files here
	System string // a core's support files go here, in the folder cores are told is theirs
	Client *http.Client
	Extra  []string // folders to read cores from; DefaultExtra if nil
	// Custom lists the approved custom cores, when they are switched on.
	Custom func() []Core
}

const (
	downloadHost = "buildbot.libretro.com"
	maxDownload  = 256 << 20
)

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// libraryExt is this platform's shared library extension.
func libraryExt() string {
	switch runtime.GOOS {
	case "windows":
		return ".dll"
	case "darwin":
		return ".dylib"
	}
	return ".so"
}

func fileName(id string) string { return id + "_libretro" + libraryExt() }

// DefaultExtra are the folders RetroArch and system packages keep cores in.
// Cores there were put there by someone else and are only ever read.
func DefaultExtra() []string {
	home, _ := os.UserHomeDir()
	var out []string
	switch runtime.GOOS {
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			out = append(out, filepath.Join(d, "RetroArch", "cores"))
		}
		if d := os.Getenv("ProgramFiles"); d != "" {
			out = append(out, filepath.Join(d, "RetroArch", "cores"))
		}
		out = append(out, `C:\RetroArch-Win64\cores`, `C:\RetroArch\cores`)
	case "darwin":
		if home != "" {
			out = append(out, filepath.Join(home, "Library", "Application Support", "RetroArch", "cores"))
		}
	default:
		if home != "" {
			out = append(out, filepath.Join(home, ".config", "retroarch", "cores"))
		}
		out = append(out, "/usr/lib/libretro", "/usr/lib64/libretro", "/usr/local/lib/libretro", "/usr/lib/x86_64-linux-gnu/libretro", "/usr/lib/aarch64-linux-gnu/libretro")
	}
	return out
}

// List returns the cores here. A core installed through us wins over the same
// core elsewhere.
func (m *Manager) List() []Core {
	seen := map[string]bool{}
	out := []Core{}
	if m.Custom != nil {
		for _, c := range m.Custom() {
			seen[c.ID] = true
			out = append(out, c)
		}
	}
	read := func(dir, source string) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			id, ok := strings.CutSuffix(name, "_libretro"+libraryExt())
			if !ok || e.IsDir() || !validID.MatchString(id) || seen[id] {
				continue
			}
			seen[id] = true
			c := Core{ID: id, Name: id, Path: filepath.Join(dir, name), Source: source, NonCommercial: wittyplay.IsNonCommercial(id)}
			if known, ok := wittyplay.CoreLicense(id); ok {
				c.Name, c.License = known.Name, known.License
			}
			c.Unapproved = !Offered(id)
			if source == "installed" {
				c.SHA256, c.Installed = m.record(id)
			}
			out = append(out, c)
		}
	}
	read(m.Dir, "installed")
	extra := m.Extra
	if extra == nil {
		extra = DefaultExtra()
	}
	for _, dir := range extra {
		src := "system"
		if strings.Contains(strings.ToLower(dir), "retroarch") {
			src = "retroarch"
		}
		read(dir, src)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Find returns a core by name, or nil.
func (m *Manager) Find(id string) *Core {
	for _, c := range m.List() {
		if c.ID == id {
			return &c
		}
	}
	return nil
}

// ForConsole picks the core to play a console with: the one asked for if it's
// here, else the first in the console's list that is.
func (m *Manager) ForConsole(c *wittyplay.Console, preferred string) *Core {
	all := m.List()
	find := func(id string) *Core {
		for i := range all {
			if all[i].ID == id {
				return &all[i]
			}
		}
		return nil
	}
	if preferred != "" {
		if got := find(preferred); got != nil {
			return got
		}
	}
	for _, want := range c.Cores {
		if got := find(want.ID); got != nil {
			return got
		}
	}
	return nil
}

// AllForConsole is every core here that can play a console, the one asked for first.
func (m *Manager) AllForConsole(c *wittyplay.Console, preferred string) []Core {
	all := m.List()
	var out []Core
	add := func(id string) {
		for _, got := range out {
			if got.ID == id {
				return
			}
		}
		for i := range all {
			if all[i].ID == id {
				out = append(out, all[i])
			}
		}
	}
	if preferred != "" {
		add(preferred)
	}
	for _, want := range c.Cores {
		add(want.ID)
	}
	return out
}

// Playable is ForConsole for a game about to start: a core that would refuse to
// run for want of a BIOS gives way to the next one that can. It also returns the
// core wanted, when nothing can run, so the message can say what's missing.
func (m *Manager) Playable(c *wittyplay.Console, preferred, systemDir string) (ok, blocked *Core) {
	all := m.List()
	find := func(id string) *Core {
		for i := range all {
			if all[i].ID == id {
				return &all[i]
			}
		}
		return nil
	}
	if preferred != "" {
		if got := find(preferred); got != nil {
			if wittyplay.MissingBIOS(got.ID, systemDir) != "" {
				return nil, got
			}
			return got, nil
		}
	}
	for _, want := range c.Cores {
		got := find(want.ID)
		if got == nil {
			continue
		}
		if wittyplay.MissingBIOS(got.ID, systemDir) == "" {
			return got, nil
		}
		if blocked == nil {
			blocked = got
		}
	}
	return nil, blocked
}

// CanDownload says whether this platform has prebuilt cores to download.
func CanDownload() bool { return platformPath() != "" }

func platformPath() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "windows/x86_64"
	case "linux/amd64":
		return "linux/x86_64"
	case "linux/arm64":
		return "linux/aarch64"
	case "linux/arm":
		return "linux/armv7-neon-hf"
	case "darwin/amd64":
		return "apple/osx/x86_64"
	case "darwin/arm64":
		return "apple/osx/arm64"
	}
	return ""
}

// Offered says whether a core is on our download list. The list only holds
// cores whose licences allow it (wittyplay.Consoles).
func Offered(id string) bool {
	for i := range wittyplay.Consoles {
		for _, c := range wittyplay.Consoles[i].Cores {
			if c.ID == id {
				return true
			}
		}
	}
	return false
}

// DownloadURL is where a core comes from. It is built only from our own table
// and platform, never from a request.
func DownloadURL(id string) (string, error) {
	if !Offered(id) {
		return "", fmt.Errorf("%q is not a core this server offers", id)
	}
	p := platformPath()
	if p == "" {
		return "", errors.New("there are no prebuilt cores for this platform; install them with your system's packages or place them in the cores folder")
	}
	return "https://" + downloadHost + "/nightly/" + p + "/latest/" + fileName(id) + ".zip", nil
}

type record struct {
	SHA256      string `json:"sha256"`
	Source      string `json:"source"`
	InstalledAt int64  `json:"installedAt"`
}

func (m *Manager) record(id string) (string, int64) {
	b, err := os.ReadFile(filepath.Join(m.Dir, id+".core.json"))
	if err != nil {
		return "", 0
	}
	var r record
	if json.Unmarshal(b, &r) != nil {
		return "", 0
	}
	return r.SHA256, r.InstalledAt
}

// downloads are the installs under way, by folder and core, for InstallProgress. The module is
// one long-running process, so another call can read what an Install is doing.
var downloads sync.Map // downloadKey -> *download

type download struct{ got, total atomic.Int64 }

func downloadKey(dir, id string) string { return dir + "\x00" + id }

// InstallProgress says how far an Install under way has got with the file it's fetching now:
// the bytes received, and the size the download server gave, 0 when it didn't give one. A core
// with support files fetches those after itself, and the count starts again for them. ok is
// false when the core isn't being installed.
func (m *Manager) InstallProgress(id string) (got, total int64, ok bool) {
	v, found := downloads.Load(downloadKey(m.Dir, id))
	if !found {
		return 0, 0, false
	}
	d := v.(*download)
	return d.got.Load(), d.total.Load(), true
}

// Install downloads one core: from one host, over HTTPS, with its size capped,
// taking exactly one file from the archive and writing it in one step.
func (m *Manager) Install(ctx context.Context, id string) (*Core, error) {
	url, err := DownloadURL(id)
	if err != nil {
		return nil, err
	}
	if !validID.MatchString(id) {
		return nil, errors.New("bad core name")
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return nil, err
	}
	d := &download{}
	key := downloadKey(m.Dir, id)
	downloads.Store(key, d)
	defer downloads.CompareAndDelete(key, d)
	body, err := m.fetch(ctx, url, d)
	if err != nil {
		return nil, err
	}
	lib, err := extractOne(body, fileName(id))
	if err != nil {
		return nil, fmt.Errorf("unpacking %s: %w", id, err)
	}
	sum := sha256.Sum256(lib)
	if err := writeFile(filepath.Join(m.Dir, fileName(id)), lib, 0o755); err != nil {
		return nil, err
	}
	if assets := wittyplay.Traits(id).Assets; assets != "" {
		if err := m.installAssets(ctx, assets, d); err != nil {
			_ = os.Remove(filepath.Join(m.Dir, fileName(id)))
			return nil, fmt.Errorf("installing %s's support files: %w", id, err)
		}
	}
	rec, _ := json.Marshal(record{SHA256: hex.EncodeToString(sum[:]), Source: url, InstalledAt: time.Now().Unix()})
	if err := writeFile(filepath.Join(m.Dir, id+".core.json"), rec, 0o644); err != nil {
		return nil, err
	}
	return m.Find(id), nil
}

// maxAssets caps what a core's support files may unpack to.
const maxAssets = 256 << 20

// installAssets unpacks a core's support files from the same host into the system
// folder. The name comes from our own table. Nothing may land outside that folder.
func (m *Manager) installAssets(ctx context.Context, name string, d *download) error {
	if m.System == "" {
		return errors.New("there is no system folder to put them in")
	}
	body, err := m.fetch(ctx, "https://"+downloadHost+"/assets/system/"+name+".zip", d)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	var total uint64
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > maxAssets {
			return errors.New("the files are larger than any core's should be")
		}
	}
	for _, f := range zr.File {
		rel := filepath.FromSlash(f.Name)
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("the archive names a path outside its folder: %q", f.Name)
		}
		out := filepath.Join(m.System, rel)
		if strings.HasSuffix(f.Name, "/") {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxAssets+1))
		rc.Close()
		if err != nil {
			return err
		}
		if err := writeFile(out, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// fetch downloads one file, counting what arrives in d when it's given.
func (m *Manager) fetch(ctx context.Context, url string, d *download) ([]byte, error) {
	client := *http.DefaultClient
	if m.Client != nil {
		client = *m.Client
	}
	client.Timeout = 5 * time.Minute
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.Hostname() != downloadHost || len(via) > 3 {
			return errors.New("the download tried to go somewhere else")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the download server answered %s", resp.Status)
	}
	var body io.Reader = resp.Body
	if d != nil {
		d.got.Store(0)
		d.total.Store(max(resp.ContentLength, 0))
		body = &counting{r: body, n: &d.got}
	}
	b, err := io.ReadAll(io.LimitReader(body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDownload {
		return nil, errors.New("the download is larger than any core should be")
	}
	return b, nil
}

// counting adds what it reads to n.
type counting struct {
	r io.Reader
	n *atomic.Int64
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// extractOne takes the named file from the top of a zip and ignores the rest.
func extractOne(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		if f.UncompressedSize64 > maxDownload {
			return nil, errors.New("the core is larger than any core should be")
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		out, err := io.ReadAll(io.LimitReader(rc, maxDownload+1))
		if err != nil {
			return nil, err
		}
		if len(out) > maxDownload {
			return nil, errors.New("the core is larger than any core should be")
		}
		return out, nil
	}
	return nil, errors.New("the archive does not hold the expected file")
}

// writeFile gives the file its mode as it makes it. This runs inside the module's sandbox, which
// refuses chmod, so setting the mode afterwards would stop every core from installing.
func writeFile(path string, b []byte, mode os.FileMode) error {
	var tmp *os.File
	for try := 0; ; try++ {
		var r [8]byte
		if _, err := rand.Read(r[:]); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".core-"+hex.EncodeToString(r[:])), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			tmp = f
			break
		}
		if !errors.Is(err, os.ErrExist) || try > 8 {
			return err
		}
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Remove deletes a core we installed. Cores elsewhere belong to whoever put them
// there.
func (m *Manager) Remove(id string) error {
	if !validID.MatchString(id) {
		return errors.New("bad core name")
	}
	if err := os.Remove(filepath.Join(m.Dir, fileName(id))); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.Dir, id+".core.json"))
	return nil
}
