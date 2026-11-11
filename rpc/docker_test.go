// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/stream"
)

const testImage = "ghcr.io/wittywilla/wittyplay-emu-test:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type rig struct {
	b    *DockerBackend
	fake *docker.Fake
	mux  *http.ServeMux
	dir  string
	// noBios leaves the BIOS folder as the test made it.
	noBios bool
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	fake := &docker.Fake{}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := stream.NewManager(stream.Config{Log: quiet, LoopbackOnly: true})
	b := NewDockerBackend(dir, func() (docker.Runtime, error) { return fake, nil }, mgr, quiet)
	t.Cleanup(b.StopAll)
	mux := http.NewServeMux()
	b.Routes(mux)
	return &rig{b: b, fake: fake, mux: mux, dir: dir}
}

// as makes a request from whoever the server says: user 0 is nobody signed in.
func (r *rig) as(t *testing.T, user int, admin bool, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user > 0 {
		req.Header.Set(HeaderUser, itoa(user))
	}
	if admin {
		req.Header.Set(HeaderAdmin, "1")
	}
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	return w
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func (r *rig) enable(t *testing.T, extra string) {
	t.Helper()
	body := `{"docker.enabled":true,"docker.publishLoopback":true` + extra + `}`
	if w := r.as(t, 1, true, "PUT", "/docker/settings", body); w.Code != http.StatusOK {
		t.Fatalf("enabling: %d %s", w.Code, w.Body)
	}
}

// install records an emulator as installed, as the installer would, without downloading it.
func (r *rig) install(t *testing.T, id, entry string) {
	t.Helper()
	d := filepath.Join(r.dir, "emulators", id, "1")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	st := map[string]emulators.Installed{id: {ID: id, Version: "1", Dir: d, Entry: entry, SHA256: strings.Repeat("0", 64)}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(r.dir, "emulators", "installed.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// launch starts a game the way the server does.
func (r *rig) launch(user, item int64, console, emu, rom string) (*DockerSession, error) {
	if !r.noBios {
		// Firmware "of the person's own", so an emulator that needs some starts.
		_ = os.MkdirAll(filepath.Join(r.dir, "bios"), 0o755)
		for _, n := range []string{"ps2-0230e.bin", "keys.txt"} {
			_ = os.WriteFile(filepath.Join(r.dir, "bios", n), []byte("x"), 0o644)
		}
	}
	return r.b.Launch(context.Background(), LaunchOptions{UserID: user, ItemID: item, Title: "A game", Console: console, ROM: rom, Emulator: emu, SystemDir: filepath.Join(r.dir, "bios")})
}

func refusal(err error) string {
	var no *Refusal
	if errors.As(err, &no) {
		return no.Code
	}
	return ""
}

func (r *rig) rom(t *testing.T) string {
	t.Helper()
	p := filepath.Join(r.dir, "game.iso")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDockerRoutesAreAdminOnly(t *testing.T) {
	r := newRig(t)
	routes := []struct{ method, path, body string }{
		{"GET", "/docker/status", ""},
		{"GET", "/docker/diagnostics", ""},
		{"GET", "/docker/settings", ""},
		{"PUT", "/docker/settings", `{"docker.enabled":true}`},
		{"GET", "/docker/emulators", ""},
		{"GET", "/docker/installed", ""},
		{"POST", "/docker/emulators/pcsx2/install", `{"license":"GPL-3.0-or-later"}`},
		{"GET", "/docker/emulators/pcsx2/install", ""},
		{"DELETE", "/docker/emulators/pcsx2/install", ""},
		{"DELETE", "/docker/emulators/pcsx2", ""},
	}
	for _, rt := range routes {
		if w := r.as(t, 0, false, rt.method, rt.path, rt.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out: %d, want 401", rt.method, rt.path, w.Code)
		}
		if w := r.as(t, 2, false, rt.method, rt.path, rt.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a non-admin: %d, want 403", rt.method, rt.path, w.Code)
		}
		// An admin header without an account is still nobody.
		req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		req.Header.Set(HeaderAdmin, "1")
		w := httptest.NewRecorder()
		r.mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with only the admin header: %d, want 401", rt.method, rt.path, w.Code)
		}
	}
	if r.b.Settings().Enabled {
		t.Fatal("a refused request changed the settings")
	}
	// The player's routes need someone signed in.
	for _, rt := range [][2]string{{"POST", "/docker/sessions/abc/connect"}, {"POST", "/docker/sessions/abc/pause"}, {"POST", "/docker/sessions/abc/resume"},
		{"DELETE", "/docker/sessions/abc"}, {"GET", "/docker/sessions"}, {"GET", "/docker/emulators/pcsx2/settings"}, {"PUT", "/docker/emulators/pcsx2/settings"}} {
		if w := r.as(t, 0, false, rt[0], rt[1], ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s signed out: %d, want 401", rt[0], rt[1], w.Code)
		}
	}
}

func TestDockerDisabledByDefault(t *testing.T) {
	r := newRig(t)
	if r.b.Settings().Enabled {
		t.Fatal("standalone emulators are on by default")
	}
	w := r.as(t, 1, true, "GET", "/docker/status", "")
	var st struct {
		Enabled   bool `json:"enabled"`
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != 200 {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if st.Enabled || st.Available {
		t.Errorf("status while off: %s", w.Body)
	}
	if w := r.as(t, 1, true, "POST", "/docker/emulators/pcsx2/install", `{"license":"GPL-3.0-or-later"}`); w.Code != http.StatusConflict {
		t.Errorf("install while off: %d %s", w.Code, w.Body)
	}
	// Nothing is offered, a start is refused, and nothing reaches Docker.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	if o := r.b.Offers(context.Background(), "ps2", ""); len(o) != 0 {
		t.Errorf("offers while off: %+v", o)
	}
	if _, err := r.launch(1, 7, "ps2", "pcsx2", r.rom(t)); refusal(err) != "off" {
		t.Fatalf("a start while off: %v", err)
	}
	if len(r.fake.Specs) != 0 {
		t.Errorf("Docker was used while off: %d specs", len(r.fake.Specs))
	}
	// So does a startup sweep.
	r.fake.Add(map[string]string{docker.LabelManaged: "true", docker.LabelSession: "old"}, true)
	r.b.Startup(context.Background())
	if r.fake.Live() != 1 {
		t.Error("the startup sweep ran while off")
	}
	// The schema says what's there, and that it's off.
	w = r.as(t, 1, true, "GET", "/docker/settings", "")
	if !strings.Contains(w.Body.String(), `"docker.enabled":false`) || !strings.Contains(w.Body.String(), `"schema"`) {
		t.Errorf("settings: %s", w.Body)
	}
}

func TestDockerUnavailableIsGraceful(t *testing.T) {
	r := newRig(t)
	r.fake.Down = true
	r.enable(t, `,"docker.images":{"pcsx2":"`+testImage+`"}`)
	w := r.as(t, 1, true, "GET", "/docker/status", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Errorf("status with Docker down: %d %s", w.Code, w.Body)
	}
	// Startup never fails.
	r.b.Startup(context.Background())

	// A start is refused in a sentence, and the game's page says why the button is off.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	if _, err := r.launch(1, 7, "ps2", "pcsx2", r.rom(t)); refusal(err) != "unavailable" {
		t.Fatalf("a start with Docker down: %v", err)
	}
	if o := r.b.Offers(context.Background(), "ps2", ""); len(o) != 1 || o[0].Ready || !strings.Contains(o[0].Why, "Docker") {
		t.Errorf("offers with Docker down: %+v", o)
	}
	// Diagnostics say so too, and never fail.
	if w := r.as(t, 1, true, "GET", "/docker/diagnostics", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"fail"`) {
		t.Errorf("diagnostics with Docker down: %d %s", w.Code, w.Body)
	}

	// No connection at all is the same.
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	none := NewDockerBackend(t.TempDir(), func() (docker.Runtime, error) { return nil, docker.ErrUnavailable }, Mgr, quiet)
	none.settings.Enabled = true
	none.Startup(context.Background())
	mux := http.NewServeMux()
	none.Routes(mux)
	req := httptest.NewRequest("GET", "/docker/status", nil)
	req.Header.Set(HeaderUser, "1")
	req.Header.Set(HeaderAdmin, "1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Errorf("status with no Docker: %d %s", rec.Code, rec.Body)
	}
}

func TestDockerSettingsAreChecked(t *testing.T) {
	r := newRig(t)
	for _, bad := range []string{
		`{"docker.enabled":true,"docker.network":"host"}`,
		`{"docker.maxContainers":99}`,
		`{"docker.backend":{"pcsx2":"root"}}`,
		`{"docker.backend":{"pcsx2":"local+webrtc"}}`,
		`{"docker.images":{"pcsx2":"ghcr.io/x/y:latest"}}`,
		`{"docker.runtime":"chroot"}`,
		`{"docker.unknown":1}`,
		`not json`,
	} {
		if w := r.as(t, 1, true, "PUT", "/docker/settings", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, w.Code)
		}
	}
	r.enable(t, `,"docker.maxContainers":3`)
	// They're kept, and a new process reads them back.
	again := NewDockerBackend(r.dir, nil, Mgr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s := again.Settings(); !s.Enabled || s.MaxContainers != 3 {
		t.Errorf("settings weren't kept: %+v", s)
	}
	// The schema is data a page can draw: every field says who may set it, and maps say their keys.
	var got struct {
		Schema []docker.Field `json:"schema"`
	}
	w := r.as(t, 1, true, "GET", "/docker/settings", "")
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Schema) == 0 {
		t.Fatalf("schema: %s", w.Body)
	}
	for _, f := range got.Schema {
		if f.Scope != "admin" || f.Label == "" || (f.Kind == "map" && (f.MapOf == "" || len(f.MapKeys) == 0)) || (f.Kind == "choice" && len(f.Choices) == 0) {
			t.Errorf("a field a page couldn't draw: %+v", f)
		}
	}
}

func TestDockerStartupSweep(t *testing.T) {
	r := newRig(t)
	r.b.settings.Enabled = true
	r.fake.Add(map[string]string{docker.LabelManaged: "true", docker.LabelSession: "old"}, true)
	r.fake.Add(map[string]string{"someone.else": "true"}, true)
	r.b.Startup(context.Background())
	if r.fake.Live() != 1 {
		t.Errorf("after the sweep %d containers are left, want only the one that isn't WittyPlay's", r.fake.Live())
	}
}

func TestDockerSessionLaunch(t *testing.T) {
	r := newRig(t)
	r.enable(t, `,"docker.maxContainers":4,"docker.memoryMB":8192,"docker.images":{"cemu":"`+testImage+`","pcsx2":"`+testImage+`"}`)
	rom := r.rom(t)

	// An emulator that doesn't play the console, one that isn't installed, one that isn't listed.
	if _, err := r.launch(5, 1, "snes", "pcsx2", rom); refusal(err) != "wrong-console" {
		t.Fatalf("a SNES game in PCSX2: %v", err)
	}
	if _, err := r.launch(5, 1, "dc", "flycast", rom); refusal(err) != "not-installed" {
		t.Fatalf("an emulator that isn't installed: %v", err)
	}
	if _, err := r.launch(5, 1, "ps2", "nope", rom); refusal(err) != "no-emulator" {
		t.Fatalf("an emulator that isn't listed: %v", err)
	}
	if len(r.fake.Specs) != 0 {
		t.Fatal("a refused start reached Docker")
	}

	// Cemu streams through KasmVNC.
	r.install(t, "cemu", "Cemu-2.6-x86_64.AppImage")
	ds, err := r.launch(5, 1, "wiiu", "cemu", rom)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if ds.Backend != docker.BackendDockerKasmVNC || !strings.HasPrefix(ds.KasmURL, "/kasm/"+ds.ID+"/") || len(ds.KasmURL) != len("/kasm/"+ds.ID+"/")+65 {
		t.Fatalf("session: %+v", ds)
	}
	spec := r.fake.Specs[len(r.fake.Specs)-1]
	if _, ok := spec.Tmpfs[docker.ContainerAppRun]; !ok {
		t.Errorf("an AppImage got no %s", docker.ContainerAppRun)
	}
	if err := docker.Check(spec); err != nil {
		t.Errorf("the spec fails Check: %v", err)
	}
	b, _ := json.Marshal(ds)
	for _, want := range []string{`"kasmUrl"`, `"backend":"docker+kasmvnc"`, `"stop":"/docker/sessions/` + ds.ID + `"`, `"players":1`, `"notices":[`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("start's answer lacks %s: %s", want, b)
		}
	}
	// Opening the same game again is the same session, not a second emulator.
	again, err := r.launch(5, 1, "wiiu", "cemu", rom)
	if err != nil || again.ID != ds.ID || len(r.fake.Specs) != 1 {
		t.Fatalf("rejoining: %v, %d specs", err, len(r.fake.Specs))
	}
	// The desktop is reached by the ticket in its address and nothing else: being the player, or
	// an administrator, without it is not enough.
	for _, who := range []int{5, 1} {
		if w := r.as(t, who, who == 1, "GET", "/kasm/"+ds.ID+"/", ""); w.Code != http.StatusNotFound {
			t.Errorf("the desktop without its ticket, as %d: %d", who, w.Code)
		}
	}
	if w := r.as(t, 0, false, "GET", ds.KasmURL, ""); w.Code == http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("the desktop with its ticket: %d, policy %q", w.Code, w.Header().Get("Content-Security-Policy"))
	}
	// Uninstalling an emulator in use is refused, and so is turning the backend off under a game.
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/cemu", ""); w.Code != http.StatusConflict {
		t.Errorf("uninstall in use: %d", w.Code)
	}
	if w := r.as(t, 1, true, "PUT", "/docker/settings", `{"docker.enabled":false}`); w.Code != http.StatusConflict {
		t.Errorf("turning off under a game: %d", w.Code)
	}
	// The list: the player and an administrator see it, anyone else sees nothing.
	if w := r.as(t, 5, false, "GET", "/docker/sessions", ""); !strings.Contains(w.Body.String(), ds.ID) || !strings.Contains(w.Body.String(), `"mine":true`) {
		t.Errorf("the player's list: %s", w.Body)
	}
	if w := r.as(t, 1, true, "GET", "/docker/sessions", ""); !strings.Contains(w.Body.String(), ds.ID) || !strings.Contains(w.Body.String(), `"mine":false`) {
		t.Errorf("an administrator's list: %s", w.Body)
	}
	if w := r.as(t, 6, false, "GET", "/docker/sessions", ""); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("someone else's list: %s", w.Body)
	}
	// Pause and resume are the player's; someone else gets "no such game", an admin included.
	if w := r.as(t, 6, true, "POST", "/docker/sessions/"+ds.ID+"/pause", ""); w.Code != http.StatusNotFound {
		t.Errorf("someone else pausing: %d", w.Code)
	}
	if w := r.as(t, 5, false, "POST", "/docker/sessions/"+ds.ID+"/pause", ""); w.Code != http.StatusNoContent {
		t.Errorf("pause: %d %s", w.Code, w.Body)
	}
	if w := r.as(t, 5, false, "GET", "/docker/sessions", ""); !strings.Contains(w.Body.String(), `"state":"paused"`) {
		t.Errorf("a paused game's state: %s", w.Body)
	}
	if w := r.as(t, 5, false, "POST", "/docker/sessions/"+ds.ID+"/resume", ""); w.Code != http.StatusNoContent {
		t.Errorf("resume: %d %s", w.Code, w.Body)
	}
	// Someone else can't stop it; an administrator can.
	if w := r.as(t, 6, false, "DELETE", "/docker/sessions/"+ds.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("someone else stopping: %d", w.Code)
	}
	if w := r.as(t, 1, true, "DELETE", "/docker/sessions/"+ds.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("an administrator stopping: %d %s", w.Code, w.Body)
	}
	<-ds.Done()
	if r.fake.Live() != 0 {
		t.Errorf("%d containers left after stopping", r.fake.Live())
	}

	// PCSX2 streams through the relay into WebRTC.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	ds, err = r.launch(5, 2, "ps2", "pcsx2", rom)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if ds.Backend != docker.BackendDockerWebRTC || ds.relay == nil || ds.Players != 4 || ds.Width != 1280 {
		t.Fatalf("session: %+v", ds)
	}
	b, _ = json.Marshal(ds)
	if !strings.Contains(string(b), `"connect":"/docker/sessions/`+ds.ID+`/connect"`) {
		t.Errorf("start's answer: %s", b)
	}
	if w := r.as(t, 6, false, "POST", "/docker/sessions/"+ds.ID+"/connect", "{}"); w.Code != http.StatusNotFound {
		t.Errorf("someone else connecting: %d", w.Code)
	}
	if w := r.as(t, 1, true, "POST", "/docker/sessions/"+ds.ID+"/connect", "{}"); w.Code != http.StatusNotFound {
		t.Errorf("an administrator connecting to someone's game: %d", w.Code)
	}
	if w := r.as(t, 5, false, "POST", "/docker/sessions/"+ds.ID+"/connect", `{"hello":{"v":1}}`); w.Code < 400 {
		t.Errorf("a bad hello was accepted: %d", w.Code)
	}
	// One game at a time for each person: another game ends this one. Someone else's is untouched.
	other, err := r.launch(6, 2, "ps2", "pcsx2", rom)
	if err != nil {
		t.Fatalf("a second person: %v", err)
	}
	next, err := r.launch(5, 3, "ps2", "pcsx2", rom)
	if err != nil {
		t.Fatalf("the same person's next game: %v", err)
	}
	<-ds.Done()
	if ds.c.Reason() != "replaced" || r.fake.Live() != 2 || r.b.Session(other.ID) == nil || r.b.Session(next.ID) == nil {
		t.Errorf("after starting another: reason %q, %d live", ds.c.Reason(), r.fake.Live())
	}

	// An administrator's "not offered" refuses it.
	r.b.StopAll()
	<-next.Done()
	<-other.Done()
	for r.b.Count() > 0 {
		runtime.Gosched()
	}
	r.enable(t, `,"docker.backend":{"pcsx2":"process"},"docker.images":{"pcsx2":"`+testImage+`"}`)
	if _, err := r.launch(5, 2, "ps2", "pcsx2", rom); refusal(err) != "turned-off" {
		t.Errorf("an emulator set to process: %v", err)
	}
	if o := r.b.Offers(context.Background(), "ps2", ""); len(o) != 0 {
		t.Errorf("an emulator set to process is offered: %+v", o)
	}
}

// TestDockerRefusesSmallMachines: a Raspberry Pi 3 is told plainly what it can't run, before
// anything is started, and the list says so beside each emulator.
func TestDockerRefusesSmallMachines(t *testing.T) {
	r := newRig(t)
	r.fake.Machine = &docker.Status{Arch: "arm64", CPUs: 4, MemoryMB: 906}
	r.enable(t, `,"docker.images":{"ppsspp":"`+testImage+`","pcsx2":"`+testImage+`"}`)
	r.install(t, "ppsspp", "PPSSPP.AppImage")
	_, err := r.launch(5, 1, "psp", "ppsspp", r.rom(t))
	var no *Refusal
	if !errors.As(err, &no) || no.Code != "too-small" || !strings.Contains(no.Text, "memory") || !strings.Contains(no.Text, "PPSSPP") {
		t.Fatalf("a start on a small machine: %v", err)
	}
	if len(r.fake.Specs) != 0 {
		t.Fatal("something was started on a machine too small for it")
	}
	if o := r.b.Offers(context.Background(), "psp", ""); len(o) != 1 || o[0].Ready || !strings.Contains(o[0].Why, "memory") {
		t.Errorf("offers on a small machine: %+v", o)
	}
	var list []CatalogEntry
	w := r.as(t, 1, true, "GET", "/docker/emulators", "")
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		switch c.ID {
		case "ppsspp":
			if c.Runnable || !strings.Contains(c.WhyNot, "memory") || !c.Installable {
				t.Errorf("ppsspp on a small arm64 machine: %+v", c)
			}
		case "pcsx2":
			// Its only download is for x86-64: the machine containers run on decides, not this one.
			if c.Installable || !strings.Contains(c.Why, "linux/arm64") {
				t.Errorf("pcsx2 on arm64: installable=%v why=%q", c.Installable, c.Why)
			}
		}
	}
	if w := r.as(t, 1, true, "GET", "/docker/diagnostics", ""); !strings.Contains(w.Body.String(), "too little memory") {
		t.Errorf("diagnostics on a small machine: %s", w.Body)
	}
	// A middling machine: the memory limit never promises more than there is.
	r2 := newRig(t)
	r2.fake.Machine = &docker.Status{Arch: "amd64", CPUs: 4, MemoryMB: 3000}
	r2.enable(t, `,"docker.images":{"flycast":"`+testImage+`"}`)
	r2.install(t, "flycast", "flycast-x86_64.AppImage")
	if _, err := r2.launch(5, 1, "dc", "flycast", r2.rom(t)); err != nil {
		t.Fatal(err)
	}
	if got := r2.fake.Specs[0].MemoryBytes >> 20; got > 3000 {
		t.Errorf("a container was promised %d MB on a machine with 3000", got)
	}
}

func TestDockerUnverifiedInstallIsNotRun(t *testing.T) {
	r := newRig(t)
	r.enable(t, `,"docker.images":{"pcsx2":"`+testImage+`"}`)
	r.install(t, "pcsx2", "pcsx2.AppImage")
	p := filepath.Join(r.dir, "emulators", "installed.json")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(p, []byte(strings.Replace(string(b), `"sha256"`, `"unverified":true,"sha256"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.launch(5, 1, "ps2", "pcsx2", r.rom(t)); refusal(err) != "unverified" {
		t.Fatalf("an unverified install was run: %v", err)
	}
}

// TestDockerLocalSandbox: with docker.runtime set to local, a game goes to the sandbox without
// Docker: no image, no network, and a socket for the picture and the input.
func TestDockerLocalSandbox(t *testing.T) {
	r := newRig(t)
	localFake := &docker.Fake{}
	r.b.Local, r.b.LocalMissing = localFake, func() []string { return nil }
	r.enable(t, `,"docker.runtime":"local","docker.memoryMB":8192`)
	r.install(t, "cemu", "Cemu-2.6-x86_64.AppImage")
	ds, err := r.launch(5, 1, "wiiu", "cemu", r.rom(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.fake.Specs) != 0 || len(localFake.Specs) != 1 {
		t.Fatalf("docker got %d specs and the local sandbox %d", len(r.fake.Specs), len(localFake.Specs))
	}
	spec := localFake.Specs[0]
	// Cemu's own stream is the desktop, which the local sandbox doesn't have: it's streamed.
	if ds.Backend != docker.BackendLocalWebRTC || ds.relay == nil || !spec.Local || spec.Image != "" || spec.Network != docker.LocalNetwork || len(spec.Publish) != 0 {
		t.Fatalf("session %+v, spec %+v", ds, spec)
	}
	if err := docker.Check(spec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ds.ioDir, "host.sock")); err != nil {
		t.Errorf("the bridge's socket: %v", err)
	}
	if w := r.as(t, 5, false, "DELETE", "/docker/sessions/"+ds.ID, ""); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	<-ds.Done()
	// A machine without it says what's missing instead.
	r.b.LocalMissing = func() []string { return []string{"bubblewrap (the bwrap program) isn't installed"} }
	if w := r.as(t, 1, true, "GET", "/docker/status", ""); !strings.Contains(w.Body.String(), "bubblewrap") || !strings.Contains(w.Body.String(), `"runtime":"local"`) {
		t.Errorf("status: %s", w.Body)
	}
}

func TestDockerCatalogAndInstalled(t *testing.T) {
	r := newRig(t)
	w := r.as(t, 1, true, "GET", "/docker/emulators", "")
	var list []CatalogEntry
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) < 10 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	for _, c := range list {
		if c.ID == "duckstation" && c.Installable {
			t.Error("a non-commercial emulator is offered for download")
		}
		if c.License == "" || c.LicenseURL == "" || c.Upstream == "" {
			t.Errorf("%s: a page couldn't show its licence: %+v", c.ID, c)
		}
	}
	if strings.Contains(w.Body.String(), r.dir) {
		t.Error("the list tells a browser where things are on this machine's disk")
	}
	r.install(t, "pcsx2", "pcsx2.AppImage")
	w = r.as(t, 1, true, "GET", "/docker/installed", "")
	if !strings.Contains(w.Body.String(), `"pcsx2"`) || strings.Contains(w.Body.String(), r.dir) {
		t.Errorf("installed: %s", w.Body)
	}
	// What's installed is version "1"; the list has a newer one.
	w = r.as(t, 1, true, "GET", "/docker/emulators", "")
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	for _, c := range list {
		if c.ID == "pcsx2" && (c.Installed == nil || !c.UpdateAvailable) {
			t.Errorf("pcsx2: %+v", c)
		}
	}
	r.enable(t, "")
	// The licence has to have been shown: an install that doesn't name it is refused.
	for _, body := range []string{"", `{}`, `{"license":"MIT"}`} {
		if w := r.as(t, 1, true, "POST", "/docker/emulators/pcsx2/install", body); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"license"`) {
			t.Errorf("install with body %q: %d %s", body, w.Code, w.Body)
		}
	}
	if w := r.as(t, 1, true, "POST", "/docker/emulators/nope/install", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("installing what isn't listed: %d", w.Code)
	}
	if w := r.as(t, 1, true, "GET", "/docker/emulators/pcsx2/install", ""); w.Code != http.StatusNotFound {
		t.Errorf("a job that never started: %d", w.Code)
	}
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/pcsx2/install", ""); w.Code != http.StatusNotFound {
		t.Errorf("cancelling a job that never started: %d", w.Code)
	}
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/pcsx2", ""); w.Code != http.StatusNoContent {
		t.Errorf("uninstall: %d %s", w.Code, w.Body)
	}
}

// TestDockerInstallJob: an install runs in the background, says how far it has got, can be
// cancelled, and a failed one says why. The download here is refused by the client, so nothing
// leaves the machine.
func TestDockerInstallJob(t *testing.T) {
	r := newRig(t)
	r.enable(t, "")
	block := make(chan struct{})
	r.b.inst.Client = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		select {
		case <-block:
			return nil, errors.New("no network in tests")
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})}
	w := r.as(t, 1, true, "POST", "/docker/emulators/flycast/install", `{"license":"GPL-2.0-only"}`)
	if w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"state":"running"`) {
		t.Fatalf("install: %d %s", w.Code, w.Body)
	}
	if w := r.as(t, 1, true, "POST", "/docker/emulators/flycast/install", `{"license":"GPL-2.0-only"}`); w.Code != http.StatusConflict {
		t.Errorf("installing twice at once: %d", w.Code)
	}
	if w := r.as(t, 1, true, "GET", "/docker/emulators", ""); !strings.Contains(w.Body.String(), `"job":{"id":"flycast","state":"running"`) {
		t.Errorf("the list doesn't show the job: %s", w.Body)
	}
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/flycast/install", ""); w.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d", w.Code)
	}
	waitJob(t, r, "flycast", "cancelled")
	// Again, and this time the download fails.
	close(block)
	if w := r.as(t, 1, true, "POST", "/docker/emulators/flycast/install", `{"license":"GPL-2.0-only"}`); w.Code != http.StatusAccepted {
		t.Fatalf("install again: %d %s", w.Code, w.Body)
	}
	j := waitJob(t, r, "flycast", "failed")
	if !strings.Contains(j.Error, "no network in tests") {
		t.Errorf("a failed job's reason: %q", j.Error)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func waitJob(t *testing.T, r *rig, id, state string) InstallJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		j, _ := r.b.Job(id)
		if j.State == state {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job is %q, not %q", j.State, state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestEmulatorSettings: the form is data, values are checked against it, each layer belongs to
// who it should, and what's set reaches the emulator's own files and the container.
func TestEmulatorSettings(t *testing.T) {
	r := newRig(t)
	r.enable(t, `,"docker.images":{"pcsx2":"`+testImage+`"}`)
	r.install(t, "pcsx2", "pcsx2.AppImage")
	var got emuSettings
	read := func(w *httptest.ResponseRecorder) {
		t.Helper()
		got = emuSettings{}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 {
			t.Fatalf("settings: %d %s", w.Code, w.Body)
		}
	}
	read(r.as(t, 5, false, "GET", "/docker/emulators/pcsx2/settings", ""))
	keys := map[string]emulators.Setting{}
	for _, s := range got.Schema {
		keys[s.Key] = s
		if s.Label == "" || (s.Scope != "admin" && s.Scope != "user") {
			t.Errorf("a setting a page couldn't draw: %+v", s)
		}
		if s.Fixed {
			t.Errorf("a fixed setting is offered: %s", s.Key)
		}
	}
	for _, k := range []string{"stream.size", "stream.kbps", "keys.p1", "keys.p4", "renderer", "scale"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("the schema lacks %s", k)
		}
	}
	if got.CanAdmin || got.Effective["renderer"] != "opengl" || got.Effective["scale"] != "1" {
		t.Errorf("defaults: %+v", got)
	}
	put := func(user int, admin bool, body string) *httptest.ResponseRecorder {
		return r.as(t, user, admin, "PUT", "/docker/emulators/pcsx2/settings", body)
	}
	// Refusals: the wrong scope for who's asking, an admin-only setting, values the schema doesn't have.
	for body, want := range map[string]int{
		`{"scope":"admin","values":{"renderer":"vulkan"}}`:  http.StatusForbidden,
		`{"scope":"user","values":{"renderer":"vulkan"}}`:   http.StatusForbidden,
		`{"scope":"game","game":9,"values":{"keys.p1":{}}}`: http.StatusForbidden,
		`{"scope":"user","values":{"scale":"9"}}`:           http.StatusBadRequest,
		`{"scope":"user","values":{"scale":2}}`:             http.StatusBadRequest,
		`{"scope":"user","values":{"nope":"x"}}`:            http.StatusBadRequest,
		`{"scope":"user","values":{"biosFolder":"/etc"}}`:   http.StatusBadRequest,
		`{"scope":"user","values":{"stream.kbps":99999}}`:   http.StatusBadRequest,
		`{"scope":"game","values":{"scale":"2"}}`:           http.StatusBadRequest,
		`{"scope":"everyone","values":{}}`:                  http.StatusBadRequest,
		`{"scope":"user","values":{"scale":"2"},"extra":1}`: http.StatusBadRequest,
	} {
		if w := put(5, false, body); w.Code != want {
			t.Errorf("%s: %d, want %d (%s)", body, w.Code, want, w.Body)
		}
	}
	// A key that isn't a key's name never reaches the sandbox's command line.
	if w := put(1, true, `{"scope":"admin","values":{"keys.p1":{"a":"x; rm -rf /"}}}`); w.Code != http.StatusBadRequest {
		t.Errorf("a key with a command in it: %d %s", w.Code, w.Body)
	}
	// An administrator sets everyone's; a person sets their own; a game has its own.
	read(put(1, true, `{"scope":"admin","values":{"renderer":"vulkan","scale":"2","stream.size":"1920x1080","keys.p1":{"a":"c"}}}`))
	read(put(5, false, `{"scope":"user","values":{"scale":"3","stream.kbps":3000}}`))
	read(put(5, false, `{"scope":"game","game":42,"values":{"scale":"4","widescreen":true}}`))
	if got.Effective["scale"] != "4" || got.Effective["renderer"] != "vulkan" || got.Game["scale"] != "4" || got.User["scale"] != "3" || got.Admin["scale"] != "2" {
		t.Errorf("layers: %+v", got)
	}
	read(r.as(t, 5, false, "GET", "/docker/emulators/pcsx2/settings", ""))
	if got.Effective["scale"] != "3" {
		t.Errorf("without the game: %v", got.Effective["scale"])
	}
	read(r.as(t, 6, false, "GET", "/docker/emulators/pcsx2/settings?game=42", ""))
	if got.Effective["scale"] != "2" || len(got.User) != 0 || len(got.Game) != 0 {
		t.Errorf("someone else sees this person's settings: %+v", got)
	}
	// They're kept: a new process reads them back.
	again := NewDockerBackend(r.dir, nil, Mgr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if s := again.emuSettings(emulators.Find("pcsx2"), 5, 42, false); s.Effective["scale"] != "4" {
		t.Errorf("settings weren't kept: %+v", s.Effective)
	}
	// And a start uses them: the emulator's own file, the stream's size, the keys.
	ds, err := r.launch(5, 42, "ps2", "pcsx2", r.rom(t))
	if err != nil {
		t.Fatal(err)
	}
	ini, err := os.ReadFile(filepath.Join(r.dir, "docker", "saves", "5", "pcsx2", "config", "PCSX2", "inis", "PCSX2.ini"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[EmuCore/GS]", "Renderer = 14", "upscale_multiplier = 4", "AspectRatio = 16:9", "[Folders]", "Bios = /bios"} {
		if !strings.Contains(string(ini), want) {
			t.Errorf("PCSX2.ini lacks %q:\n%s", want, ini)
		}
	}
	env := strings.Join(r.fake.Specs[0].Env, "\n")
	for _, want := range []string{"WITTYPLAY_WIDTH=1920", "WITTYPLAY_HEIGHT=1080", "WITTYPLAY_KBPS=3000", `WITTYPLAY_KEYS=[{"a":"c"}`} {
		if !strings.Contains(env, want) {
			t.Errorf("the container's environment lacks %s:\n%s", want, env)
		}
	}
	if ds.Width != 1920 || ds.Height != 1080 {
		t.Errorf("the session's size: %dx%d", ds.Width, ds.Height)
	}
	// Clearing a value goes back to the layer below.
	read(put(5, false, `{"scope":"game","game":42,"values":{"scale":null}}`))
	if got.Effective["scale"] != "3" {
		t.Errorf("after clearing: %v", got.Effective["scale"])
	}
}

func TestBiosStatus(t *testing.T) {
	r := newRig(t)
	r.noBios = true
	r.enable(t, `,"docker.images":{"pcsx2":"`+testImage+`"}`)
	r.install(t, "pcsx2", "pcsx2.AppImage")
	bios := filepath.Join(r.dir, "bios")
	if err := os.MkdirAll(bios, 0o755); err != nil {
		t.Fatal(err)
	}
	// Before the server has said where the BIOS folder is, nothing is claimed either way.
	if w := r.as(t, 1, true, "GET", "/docker/emulators", ""); strings.Contains(w.Body.String(), `"present"`) {
		t.Errorf("BIOS status without a folder: %s", w.Body)
	}
	if o := r.b.Offers(context.Background(), "ps2", bios); len(o) != 1 || o[0].Ready || !strings.Contains(o[0].Why, "firmware") {
		t.Fatalf("offers without a BIOS: %+v", o)
	}
	if _, err := r.launch(5, 1, "ps2", "pcsx2", r.rom(t)); refusal(err) != "needs-bios" {
		t.Fatalf("a start without a BIOS: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bios, "PS2-0230e-20080220.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if o := r.b.Offers(context.Background(), "ps2", bios); len(o) != 1 || !o[0].Ready {
		t.Fatalf("offers with a BIOS: %+v", o)
	}
	// The server's header tells the admin's page the same thing.
	req := httptest.NewRequest("GET", "/docker/emulators", nil)
	req.Header.Set(HeaderUser, "1")
	req.Header.Set(HeaderAdmin, "1")
	req.Header.Set(HeaderSystemDir, bios)
	w := httptest.NewRecorder()
	r.mux.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `{"name":"ps2-*.bin","present":true`) {
		t.Errorf("the list's BIOS status: %s", w.Body)
	}
	ds, err := r.launch(5, 1, "ps2", "pcsx2", r.rom(t))
	if err != nil {
		t.Fatal(err)
	}
	// The folder is given read-only.
	found := false
	for _, m := range r.fake.Specs[0].Mounts {
		if m.Target == docker.ContainerBIOS {
			found = m.ReadOnly && m.Source == bios
		}
	}
	if !found {
		t.Errorf("the BIOS folder's mount: %+v", r.fake.Specs[0].Mounts)
	}
	_ = ds
}

// TestEmulatorsNameConsolesTheLibraryHas: an emulator is offered on a game's page by the game's
// console id, so the list has to use WittyPlay's own ids, or be for a console an administrator
// adds as a custom one.
func TestEmulatorsNameConsolesTheLibraryHas(t *testing.T) {
	custom := []string{"gamecube", "wii", "xbox", "wiiu", "ps3", "naomi", "switch", "switch2"}
	m, err := emulators.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Emulators {
		for _, sys := range e.Systems {
			if wittyplay.ByID(sys) == nil && !slices.Contains(custom, sys) {
				t.Errorf("%s plays %q, which is no console the library has", e.ID, sys)
			}
			if !e.Plays(sys) || !e.Plays("custom-"+sys) || e.Plays("custom-") || e.Plays("") {
				t.Errorf("%s: Plays is wrong for %q", e.ID, sys)
			}
		}
	}
	if len(emulators.ForSystem("custom-gamecube")) == 0 || len(emulators.ForSystem("dc")) == 0 || len(emulators.ForSystem("ps1")) == 0 {
		t.Error("an emulator isn't found by its console")
	}
}
