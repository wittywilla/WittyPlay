// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinManifestIsValid(t *testing.T) {
	m, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pcsx2", "dolphin", "duckstation", "ppsspp", "xemu", "cemu", "rpcs3", "melonds", "flycast"}
	for _, id := range want {
		if Find(id) == nil {
			t.Errorf("%s is missing from the built-in list", id)
		}
	}
	for _, e := range m.Emulators {
		for _, a := range e.Artifacts {
			// A hash is either checked or plainly a placeholder: nothing in between that looks real.
			if a.SHA256 != Placeholder && !validHash.MatchString(a.SHA256) {
				t.Errorf("%s: sha256 %q", e.ID, a.SHA256)
			}
		}
		if e.Image.Pinned() == "" && e.Image.Digest != Placeholder {
			t.Errorf("%s: an image digest that's neither pinned nor a placeholder", e.ID)
		}
	}
	if len(m.Placeholders()) == 0 {
		t.Log("no placeholders left")
	}
	// No Switch emulator is ever offered for download: docs/STANDALONE-EMULATORS.md, "The Switch".
	for _, e := range m.Emulators {
		if slices.Contains(e.Systems, "switch") || slices.Contains(e.Systems, "switch2") {
			t.Errorf("%s plays the Switch, and the list must offer none", e.ID)
		}
	}
	// Every platform with a download that runs natively plays here: a Mac and Windows have
	// downloads of every emulator whose project publishes one.
	for _, id := range []string{"pcsx2", "dolphin", "ppsspp", "xemu", "cemu", "rpcs3", "melonds", "flycast"} {
		e := Find(id)
		for _, p := range [][2]string{{"darwin", "arm64"}, {"windows", "amd64"}} {
			if e.For(p[0], p[1]) == nil {
				t.Errorf("%s has no download for %s/%s", id, p[0], p[1])
			}
		}
	}
}

func TestManifestRejectsBadEntries(t *testing.T) {
	base := `{"id":"x","name":"X","systems":["s"],"license":"MIT","upstream":"https://example.org","version":"1.0","display":"x11","stream":"pipe","image":{"ref":"ghcr.io/a/b:1","digest":"TODO"},"artifacts":[%s]}`
	art := func(url, sum, archive, entry string) string {
		return `{"os":"linux","arch":"amd64","url":"` + url + `","sha256":"` + sum + `","archive":"` + archive + `","entry":"` + entry + `"}`
	}
	cases := map[string]string{
		"http":        art("http://github.com/a", "TODO", "zip", "x"),
		"bad host":    art("https://evil.example/a", "TODO", "zip", "x"),
		"short hash":  art("https://github.com/a", "abc", "zip", "x"),
		"upper hash":  art("https://github.com/a", strings.Repeat("A", 64), "zip", "x"),
		"bad archive": art("https://github.com/a", "TODO", "rar", "x"),
		"entry out":   art("https://github.com/a", "TODO", "zip", "../x"),
	}
	for name, a := range cases {
		doc := `{"schema":1,"emulators":[` + strings.Replace(base, "%s", a, 1) + `]}`
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := `{"schema":1,"emulators":[` + strings.Replace(base, "%s", art("https://github.com/a", "TODO", "zip", "x"), 1) + `]}`
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("a good entry was refused: %v", err)
	}
	if _, err := Parse([]byte(strings.Replace(good, `"digest":"TODO"`, `"digest":"sha256:1234"`, 1))); err == nil {
		t.Error("a short image digest was accepted")
	}
	if _, err := Parse([]byte(strings.Replace(good, `"stream":"pipe"`, `"stream":"kasmvnc","display":"wayland"`, 1))); err == nil {
		t.Error("KasmVNC on Wayland was accepted")
	}
}

// server serves one body over TLS and returns an installer that trusts it.
func server(t *testing.T, body []byte) (*Installer, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://evil.example/x", http.StatusFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	in := &Installer{DataDir: t.TempDir(), Client: srv.Client(), Hosts: []string{"127.0.0.1"}, GOOS: "linux", GOARCH: "amd64"}
	return in, srv.URL
}

func emu(url, sum, archive, entry string) *Emulator {
	return &Emulator{ID: "test", Name: "Test", Version: "1.0", Artifacts: []Artifact{{OS: "linux", Arch: "amd64", URL: url, SHA256: sum, Archive: archive, Entry: entry}}}
}

func hash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func zipOf(t *testing.T, files map[string]string, links map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	for name, target := range links {
		h := &zip.FileHeader{Name: name}
		h.SetMode(os.ModeSymlink | 0o777)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(target))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallZipAndUninstall(t *testing.T) {
	body := zipOf(t, map[string]string{"bin/emu": "#!/bin/sh\n", "share/readme": "hi"}, map[string]string{"emu": "bin/emu"})
	in, url := server(t, body)
	got, err := in.Install(context.Background(), emu(url+"/emu.zip", hash(body), ArchiveZip, "bin/emu"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != filepath.Join(in.DataDir, "emulators", "test", "1.0") {
		t.Fatalf("installed into %s", got.Dir)
	}
	st, err := os.Stat(filepath.Join(got.Dir, "bin", "emu"))
	if err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("the program isn't there and executable: %v", err)
	}
	if list, _ := in.List(); len(list) != 1 || list[0].SHA256 != hash(body) {
		t.Fatalf("installed state: %+v", list)
	}
	// Nothing is left staged.
	ents, _ := os.ReadDir(filepath.Join(in.DataDir, "emulators", "test"))
	if len(ents) != 1 {
		t.Fatalf("left behind: %v", ents)
	}
	if err := in.Uninstall("test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(got.Dir); !os.IsNotExist(err) {
		t.Fatal("still there after uninstalling")
	}
	if err := in.Uninstall("test"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("uninstalling twice: %v", err)
	}
}

func TestInstallAppImage(t *testing.T) {
	body := []byte("\x7fELF pretend AppImage")
	in, url := server(t, body)
	got, err := in.Install(context.Background(), emu(url+"/e.AppImage", hash(body), ArchiveAppImage, "Emu.AppImage"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(got.Dir, "Emu.AppImage"))
	if err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("the AppImage isn't executable: %v %v", st, err)
	}
}

func TestInstallTarGz(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "emu/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "emu/run", Typeflag: tar.TypeReg, Mode: 0o4755, Size: 2})
	_, _ = tw.Write([]byte("hi"))
	_ = tw.Close()
	_ = gz.Close()
	body := buf.Bytes()
	in, url := server(t, body)
	got, err := in.Install(context.Background(), emu(url+"/e.tgz", hash(body), ArchiveTarGz, "emu/run"))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(filepath.Join(got.Dir, "emu", "run"))
	if st.Mode()&os.ModeSetuid != 0 {
		t.Fatal("a setuid bit came through")
	}
}

func TestInstallRefusals(t *testing.T) {
	body := zipOf(t, map[string]string{"emu": "x"}, nil)
	in, url := server(t, body)
	ctx := context.Background()

	if _, err := in.Install(ctx, emu(url+"/e.zip", strings.Repeat("0", 64), ArchiveZip, "emu")); !errors.Is(err, ErrHash) {
		t.Errorf("bad hash: %v", err)
	}
	if _, err := in.Install(ctx, emu(url+"/e.zip", Placeholder, ArchiveZip, "emu")); !errors.Is(err, ErrPlaceholder) {
		t.Errorf("placeholder: %v", err)
	}
	if _, err := in.Install(ctx, emu("https://evil.example/e.zip", hash(body), ArchiveZip, "emu")); !errors.Is(err, ErrHost) {
		t.Errorf("host off the list: %v", err)
	}
	if _, err := in.Install(ctx, emu(strings.Replace(url, "https", "http", 1)+"/e.zip", hash(body), ArchiveZip, "emu")); err == nil {
		t.Error("plain http was accepted")
	}
	if _, err := in.Install(ctx, emu(url+"/redirect", hash(body), ArchiveZip, "emu")); err == nil || !strings.Contains(err.Error(), "somewhere else") {
		t.Errorf("a redirect off the list: %v", err)
	}
	nc := emu(url+"/e.zip", hash(body), ArchiveZip, "emu")
	nc.NonCommercial = true
	if _, err := in.Install(ctx, nc); !errors.Is(err, ErrNonCommercial) {
		t.Errorf("non-commercial: %v", err)
	}
	// A .tar.xz is opened now; one that isn't really xz is refused, and nothing is installed.
	if _, err := in.Install(ctx, emu(url+"/e.txz", hash(body), ArchiveTarXz, "emu")); err == nil || !strings.Contains(err.Error(), "xz") {
		t.Errorf("not really xz: %v", err)
	}
	if _, err := in.Install(ctx, emu(url+"/e.zip", hash(body), ArchiveZip, "missing")); err == nil {
		t.Error("an archive without its program was accepted")
	}
	if list, _ := in.List(); len(list) != 0 {
		t.Errorf("a refused install left state: %+v", list)
	}

	// A developer may allow a placeholder, and the result says it wasn't checked.
	in.AllowPlaceholders = true
	got, err := in.Install(ctx, emu(url+"/e.zip", Placeholder, ArchiveZip, "emu"))
	if err != nil || !got.Unverified || got.SHA256 != hash(body) {
		t.Errorf("an allowed placeholder: %+v %v", got, err)
	}
}

func TestInstallOversize(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 4096)
	in, url := server(t, body)
	in.MaxDownload = 1024
	if _, err := in.Install(context.Background(), emu(url+"/e", hash(body), ArchiveAppImage, "e")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize download: %v", err)
	}
	// Small to download, large unpacked.
	z := zipOf(t, map[string]string{"emu": strings.Repeat("z", 1<<20)}, nil)
	in2, url2 := server(t, z)
	in2.MaxUnpacked = 1 << 10
	if _, err := in2.Install(context.Background(), emu(url2+"/e.zip", hash(z), ArchiveZip, "emu")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize unpacked: %v", err)
	}
}

func TestZipSlipAndLinks(t *testing.T) {
	cases := map[string][]byte{
		"dotdot":         zipOf(t, map[string]string{"../evil": "x", "emu": "x"}, nil),
		"deep dotdot":    zipOf(t, map[string]string{"a/../../evil": "x", "emu": "x"}, nil),
		"absolute":       zipOf(t, map[string]string{"/etc/evil": "x", "emu": "x"}, nil),
		"backslash":      zipOf(t, map[string]string{`..\evil`: "x", "emu": "x"}, nil),
		"link out":       zipOf(t, map[string]string{"emu": "x"}, map[string]string{"l": "../../etc"}),
		"link absolute":  zipOf(t, map[string]string{"emu": "x"}, map[string]string{"l": "/etc/passwd"}),
		"through a link": zipOf(t, map[string]string{"l/x": "x", "emu": "x"}, map[string]string{"l": "d"}),
	}
	for name, body := range cases {
		in, url := server(t, body)
		_, err := in.Install(context.Background(), emu(url+"/e.zip", hash(body), ArchiveZip, "emu"))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := os.Stat(filepath.Join(in.DataDir, "evil")); err == nil {
			t.Errorf("%s: wrote outside", name)
		}
	}

	// The same rules for tar.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	in, url := server(t, buf.Bytes())
	if _, err := in.Install(context.Background(), emu(url+"/e.tgz", hash(buf.Bytes()), ArchiveTarGz, "emu")); err == nil {
		t.Error("tar slip accepted")
	}
	buf.Reset()
	gz = gzip.NewWriter(&buf)
	tw = tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"})
	_ = tw.Close()
	_ = gz.Close()
	in, url = server(t, buf.Bytes())
	if _, err := in.Install(context.Background(), emu(url+"/e.tgz", hash(buf.Bytes()), ArchiveTarGz, "emu")); err == nil {
		t.Error("a hard link was accepted")
	}
}
