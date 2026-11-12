// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestBuiltinDownloadsArePinned: nothing in the list can be installed on trust. Every download
// has a checked hash and a size, comes over https from a release's own address, and names a
// version; an entry that can't say all that isn't offered.
func TestBuiltinDownloadsArePinned(t *testing.T) {
	m, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Emulators {
		if e.License == "" || e.LicenseURL == "" || e.Requires.MemoryMB == 0 || e.Requires.CPUs == 0 {
			t.Errorf("%s: no licence address or no requirements", e.ID)
		}
		if e.Version == Placeholder && len(e.Artifacts) > 0 {
			t.Errorf("%s: a download without a version", e.ID)
		}
		for _, a := range e.Artifacts {
			if a.Placeholder() || !validHash.MatchString(a.SHA256) {
				t.Errorf("%s %s/%s: the download isn't pinned to a hash", e.ID, a.OS, a.Arch)
			}
			if a.Size <= 0 || a.Size > DefaultMaxDownload {
				t.Errorf("%s %s/%s: size %d", e.ID, a.OS, a.Arch, a.Size)
			}
			if !slices.Contains([]string{"linux", "darwin", "windows", "freebsd"}, a.OS) || !slices.Contains([]string{"amd64", "arm64", ArchUniversal}, a.Arch) ||
				(a.Arch == ArchUniversal && a.OS != "darwin") {
				t.Errorf("%s: a download for %s/%s, where nothing runs an emulator", e.ID, a.OS, a.Arch)
			}
			// Pinned to a release by its tag (or Dolphin's numbered builds), never "latest": a
			// moving address would only ever fail its hash.
			tagged := strings.HasPrefix(a.URL, "https://github.com/") && strings.Contains(a.URL, "/releases/download/")
			dolphin := strings.HasPrefix(a.URL, "https://dl.dolphin-emu.org/releases/")
			if !(tagged || dolphin) || strings.Contains(a.URL, "/latest/") {
				t.Errorf("%s: %s isn't a tagged release asset", e.ID, a.URL)
			}
		}
		if err := checkSettings(e.Settings); err != nil {
			t.Errorf("%s: %v", e.ID, err)
		}
	}
}

// TestInstallCarriesOn: a download that was cut short is carried on from where it stopped, the
// whole file is still hashed, and a part that doesn't belong is never trusted.
func TestInstallCarriesOn(t *testing.T) {
	body := []byte(strings.Repeat("0123456789abcdef", 4096)) // 64 KiB
	var cut atomic.Bool
	var ranges atomic.Int32
	cut.Store(true)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranges.Add(1)
		}
		if cut.Load() {
			// Say how much is coming, send a part, and hang up.
			w.Header().Set("Content-Length", "65536")
			_, _ = w.Write(body[:20000])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		http.ServeContent(w, r, "e.AppImage", time.Time{}, strings.NewReader(string(body)))
	}))
	t.Cleanup(srv.Close)
	in := &Installer{DataDir: t.TempDir(), Client: srv.Client(), Hosts: []string{"127.0.0.1"}, GOOS: "linux", GOARCH: "amd64"}
	var last Progress
	in.OnProgress = func(_ string, p Progress) {
		if p.Phase == PhaseDownloading {
			last = p
		}
	}
	e := emu(srv.URL+"/e.AppImage", hash(body), ArchiveAppImage, "Emu.AppImage")
	e.Artifacts[0].Size = int64(len(body))

	if _, err := in.Install(context.Background(), e); err == nil {
		t.Fatal("a download that was cut short installed")
	}
	part := filepath.Join(in.Root(), "test", ".partial-"+hash(body)[:16])
	st, err := os.Stat(part)
	if err != nil || st.Size() == 0 || st.Size() >= int64(len(body)) {
		t.Fatalf("what arrived wasn't kept: %v %v", st, err)
	}
	kept := st.Size()
	cut.Store(false)
	got, err := in.Install(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	if ranges.Load() != 1 || last.Resumed != kept || got.SHA256 != hash(body) {
		t.Errorf("carried on: %d range requests, resumed %d of %d kept", ranges.Load(), last.Resumed, kept)
	}
	if b, _ := os.ReadFile(filepath.Join(got.Dir, "Emu.AppImage")); string(b) != string(body) {
		t.Error("the installed file isn't the download")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("the partial download was left behind")
	}

	// A part someone changed between tries: the whole file fails its hash, and none of it is kept.
	if err := in.Uninstall("test"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, []byte(strings.Repeat("X", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Install(context.Background(), e); !errors.Is(err, ErrHash) {
		t.Fatalf("a tampered part: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Error("a download that failed its hash was kept")
	}
	if list, _ := in.List(); len(list) != 0 {
		t.Error("something was installed from a download that failed its hash")
	}
}

// TestInstallHoldsTheSize: a file that isn't the size the list says is refused before anything
// is unpacked, whatever the server claims.
func TestInstallHoldsTheSize(t *testing.T) {
	body := []byte("\x7fELF pretend AppImage, somewhat longer than it should be")
	in, url := server(t, body)
	e := emu(url+"/e.AppImage", hash(body), ArchiveAppImage, "Emu.AppImage")
	e.Artifacts[0].Size = 10
	if _, err := in.Install(context.Background(), e); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a file larger than the list says: %v", err)
	}
	if list, _ := in.List(); len(list) != 0 {
		t.Fatal("it was installed")
	}
}

// TestInstallForWhereEmulatorsRun: the download is chosen for the machine containers run on.
func TestInstallForWhereEmulatorsRun(t *testing.T) {
	body := []byte("\x7fELF")
	in, url := server(t, body)
	in.GOOS, in.GOARCH = "darwin", "arm64" // the server's own machine
	e := emu(url+"/e.AppImage", hash(body), ArchiveAppImage, "Emu.AppImage")
	if _, err := in.Install(context.Background(), e); !errors.Is(err, ErrNoArtifact) {
		t.Fatalf("on a Mac with no word from Docker: %v", err)
	}
	in.Platform = func() (string, string) { return "linux", "amd64" } // Docker's virtual machine
	if _, err := in.Install(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsAreChecked(t *testing.T) {
	list := Find("pcsx2").Settings
	if len(list) == 0 {
		t.Fatal("pcsx2 has no settings")
	}
	if _, err := CleanValues(list, ScopeUser, Values{"scale": "2", "widescreen": true}); err != nil {
		t.Fatal(err)
	}
	var se *ScopeError
	if _, err := CleanValues(list, ScopeUser, Values{"renderer": "vulkan"}); !errors.As(err, &se) {
		t.Errorf("a person set an administrator's setting: %v", err)
	}
	for _, bad := range []Values{{"scale": "5"}, {"scale": 2.0}, {"widescreen": "yes"}, {"nope": 1.0}, {"biosFolder": "/etc"}} {
		if _, err := CleanValues(list, ScopeAdmin, bad); err == nil {
			t.Errorf("%v was accepted", bad)
		}
	}
	// A file can't raise a person's layer over an administrator's setting.
	eff := Effective(list, Values{"renderer": "vulkan"}, Values{"renderer": "software", "scale": "3"}, Values{"scale": "4"})
	if eff["renderer"] != "vulkan" || eff["scale"] != "4" || eff["biosFolder"] != "/bios" {
		t.Errorf("effective: %v", eff)
	}
	// A value that no longer fits (the list changed) falls back rather than reaching the emulator.
	if eff := Effective(list, Values{"scale": "99"}, nil, nil); eff["scale"] != "1" {
		t.Errorf("a stale value was used: %v", eff["scale"])
	}
	// Bad schemas don't get into a manifest.
	for name, s := range map[string]Setting{
		"a path out":       {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{INI: &INITarget{File: "../../etc/passwd", Section: "s", Key: "k"}}},
		"an absolute path": {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{INI: &INITarget{File: "/etc/passwd", Section: "s", Key: "k"}}},
		"the loader":       {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{Env: "LD_PRELOAD"}},
		"the sandbox's":    {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{Env: "WITTYPLAY_INPUT_TOKEN"}},
		"no scope":         {Key: "a", Kind: KindText, Label: "A", Default: ""},
		"a bad default":    {Key: "a", Kind: KindChoice, Label: "A", Default: "z", Scope: ScopeUser, Choices: []Choice{{Value: "x", Label: "X"}}},
		"a key with =":     {Key: "a", Kind: KindText, Label: "A", Default: "", Scope: ScopeUser, Apply: &Apply{INI: &INITarget{File: "a.ini", Section: "s", Key: "k=v\nx"}}},
	} {
		if checkSettings([]Setting{s}) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEditINI(t *testing.T) {
	old := "; PCSX2's own file\r\n[UI]\nTheme = dark\n\n[EmuCore/GS]\nRenderer = 12\nVsyncEnable = 1\n\n[Folders]\nBios = bios\n"
	got := string(EditINI([]byte(old), []INIEdit{{"EmuCore/GS", "Renderer", "14"}, {"EmuCore/GS", "upscale_multiplier", "3"}, {"Folders", "Bios", "/bios"}, {"New", "Key", "v"}}))
	want := "; PCSX2's own file\n[UI]\nTheme = dark\n\n[EmuCore/GS]\nRenderer = 14\nVsyncEnable = 1\nupscale_multiplier = 3\n\n[Folders]\nBios = /bios\n\n[New]\nKey = v\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := string(EditINI(nil, []INIEdit{{"A", "k", "v"}})); got != "[A]\nk = v\n" {
		t.Errorf("a new file: %q", got)
	}
}

// TestSettingsFilesStayInTheirFolder: the emulator can write to its settings folder, and an
// emulator is treated as something that may have been taken over. A link it leaves there must
// not turn WittyPlay's next write into a write anywhere else.
func TestSettingsFilesStayInTheirFolder(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim.ini")
	if err := os.WriteFile(victim, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := Applied{Files: map[string][]INIEdit{"PCSX2/inis/PCSX2.ini": {{"EmuCore/GS", "Renderer", "14"}}}}
	if err := plan.WriteFiles(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "PCSX2", "inis", "PCSX2.ini")); !strings.Contains(string(b), "Renderer = 14") {
		t.Fatalf("the file: %q", b)
	}
	// The file itself becomes a link out.
	ini := filepath.Join(dir, "PCSX2", "inis", "PCSX2.ini")
	if err := os.Remove(ini); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, ini); err != nil {
		t.Skip("no symbolic links here")
	}
	if err := plan.WriteFiles(dir); err == nil {
		t.Error("a link in place of the settings file was written through")
	}
	// A folder on the way becomes a link out.
	if err := os.RemoveAll(filepath.Join(dir, "PCSX2")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "PCSX2")); err != nil {
		t.Fatal(err)
	}
	if err := plan.WriteFiles(dir); err == nil {
		t.Error("a link in place of the settings folder was followed")
	}
	if b, _ := os.ReadFile(victim); string(b) != "untouched\n" {
		t.Errorf("a file outside the settings folder was changed: %q", b)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 1 {
		t.Errorf("something was made outside the settings folder: %v", ents)
	}
}

func TestSettingsStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	st := &Store{Path: p}
	if err := st.Set("pcsx2", ScopeAdmin, 1, 0, Values{"renderer": "vulkan"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("pcsx2", ScopeUser, 5, 0, Values{"scale": "2"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("pcsx2", ScopeGame, 5, 42, Values{"scale": "3"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Set("pcsx2", ScopeGame, 5, 0, Values{"scale": "3"}); err == nil {
		t.Error("a game's settings without a game")
	}
	again := &Store{Path: p}
	a, u, g := again.Layers("pcsx2", 5, 42)
	if a["renderer"] != "vulkan" || u["scale"] != "2" || g["scale"] != "3" {
		t.Errorf("read back: %v %v %v", a, u, g)
	}
	if _, u, g := again.Layers("pcsx2", 6, 42); len(u) != 0 || len(g) != 0 {
		t.Error("one person's settings are another's")
	}
	if err := again.ForgetUser(5); err != nil {
		t.Fatal(err)
	}
	if _, u, g := (&Store{Path: p}).Layers("pcsx2", 5, 42); len(u) != 0 || len(g) != 0 {
		t.Error("a forgotten account's settings are still there")
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode: %v %v", st.Mode(), err)
	}
}
