// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"GET", "/docker/settings", ""},
		{"PUT", "/docker/settings", `{"docker.enabled":true}`},
		{"GET", "/docker/emulators", ""},
		{"GET", "/docker/installed", ""},
		{"POST", "/docker/emulators/pcsx2/install", ""},
		{"GET", "/docker/emulators/pcsx2/install", ""},
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
	for _, p := range []string{"/kasm/abc/", "/docker/sessions/abc/connect"} {
		method := "GET"
		if strings.HasSuffix(p, "connect") {
			method = "POST"
		}
		if w := r.as(t, 0, false, method, p, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s signed out: %d, want 401", p, w.Code)
		}
	}
}

func TestDockerDisabledByDefault(t *testing.T) {
	r := newRig(t)
	if r.b.Settings().Enabled {
		t.Fatal("Docker is on by default")
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
		t.Errorf("status with Docker off: %s", w.Body)
	}
	if w := r.as(t, 1, true, "POST", "/docker/emulators/pcsx2/install", ""); w.Code != http.StatusConflict {
		t.Errorf("install with Docker off: %d %s", w.Code, w.Body)
	}
	// Starting a game takes the process path, and nothing reaches Docker.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	o := stream.Options{UserID: 1, Console: "ps2", ROM: r.rom(t)}
	if _, handled, err := r.b.Start(context.Background(), &o); handled || err != nil {
		t.Fatalf("Start with Docker off: handled=%v err=%v", handled, err)
	}
	if len(r.fake.Specs) != 0 || len(o.Notices) != 0 {
		t.Errorf("Docker was used while off: %d specs, notices %v", len(r.fake.Specs), o.Notices)
	}
	// So does a startup sweep.
	r.fake.Add(map[string]string{docker.LabelManaged: "true", docker.LabelSession: "old"}, true)
	r.b.Startup(context.Background())
	if r.fake.Live() != 1 {
		t.Error("the startup sweep ran with Docker off")
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

	// A game falls back to the process sandbox with a word about why.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	o := stream.Options{UserID: 1, Console: "ps2", ROM: r.rom(t)}
	if _, handled, err := r.b.Start(context.Background(), &o); handled || err != nil {
		t.Fatalf("Start with Docker down: handled=%v err=%v", handled, err)
	}
	if len(o.Notices) != 1 || !strings.Contains(o.Notices[0], "Docker") {
		t.Errorf("notices: %v", o.Notices)
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
		`{"docker.images":{"pcsx2":"ghcr.io/x/y:latest"}}`,
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

func TestDockerSessionStart(t *testing.T) {
	r := newRig(t)
	r.enable(t, `,"docker.images":{"cemu":"`+testImage+`","pcsx2":"`+testImage+`"}`)

	// A console no installed emulator plays takes the process path.
	o := stream.Options{UserID: 5, Console: "snes", ROM: r.rom(t)}
	if _, handled, _ := r.b.Start(context.Background(), &o); handled {
		t.Fatal("a SNES game went to Docker")
	}

	// Cemu streams through KasmVNC.
	r.install(t, "cemu", "Cemu-2.6-x86_64.AppImage")
	o = stream.Options{UserID: 5, Console: "wiiu", ROM: r.rom(t)}
	ds, handled, err := r.b.Start(context.Background(), &o)
	if !handled || err != nil {
		t.Fatalf("Start: handled=%v err=%v", handled, err)
	}
	if ds.Backend != docker.BackendDockerKasmVNC || !strings.HasPrefix(ds.KasmURL, "/kasm/"+ds.ID+"/?ticket=") {
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
	if !strings.Contains(string(b), `"kasmUrl"`) || !strings.Contains(string(b), `"backend":"docker+kasmvnc"`) {
		t.Errorf("start's answer: %s", b)
	}
	// Only the player may use the desktop, and only with the ticket.
	if w := r.as(t, 6, true, "GET", "/kasm/"+ds.ID+"/", ""); w.Code != http.StatusNotFound {
		t.Errorf("someone else's desktop: %d", w.Code)
	}
	if w := r.as(t, 5, false, "GET", "/kasm/"+ds.ID+"/", ""); w.Code != http.StatusForbidden {
		t.Errorf("the desktop without a ticket: %d", w.Code)
	}
	// Uninstalling an emulator in use is refused.
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/cemu", ""); w.Code != http.StatusConflict {
		t.Errorf("uninstall in use: %d", w.Code)
	}
	if w := r.as(t, 5, false, "DELETE", "/docker/sessions/"+ds.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("stop: %d %s", w.Code, w.Body)
	}
	<-ds.Done()
	if r.fake.Live() != 0 {
		t.Errorf("%d containers left after stopping", r.fake.Live())
	}

	// PCSX2 streams through the relay into WebRTC.
	r.install(t, "pcsx2", "pcsx2.AppImage")
	o = stream.Options{UserID: 5, Console: "ps2", ROM: r.rom(t)}
	ds, handled, err = r.b.Start(context.Background(), &o)
	if !handled || err != nil {
		t.Fatalf("Start: handled=%v err=%v", handled, err)
	}
	if ds.Backend != docker.BackendDockerWebRTC || ds.relay == nil {
		t.Fatalf("session: %+v", ds)
	}
	b, _ = json.Marshal(ds)
	if !strings.Contains(string(b), `"connect":"/docker/sessions/`+ds.ID+`/connect"`) {
		t.Errorf("start's answer: %s", b)
	}
	if w := r.as(t, 6, false, "POST", "/docker/sessions/"+ds.ID+"/connect", "{}"); w.Code != http.StatusNotFound {
		t.Errorf("someone else connecting: %d", w.Code)
	}
	if w := r.as(t, 5, false, "POST", "/docker/sessions/"+ds.ID+"/connect", `{"hello":{"v":1}}`); w.Code < 400 {
		t.Errorf("a bad hello was accepted: %d", w.Code)
	}

	// An administrator's override sends it back to the process path.
	r.b.StopAll()
	r.enable(t, `,"docker.backend":{"pcsx2":"process"},"docker.images":{"pcsx2":"`+testImage+`"}`)
	o = stream.Options{UserID: 5, Console: "ps2", ROM: r.rom(t)}
	if _, handled, _ := r.b.Start(context.Background(), &o); handled {
		t.Error("an emulator set to process went to Docker")
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
	}
	r.install(t, "pcsx2", "pcsx2.AppImage")
	w = r.as(t, 1, true, "GET", "/docker/installed", "")
	if !strings.Contains(w.Body.String(), `"pcsx2"`) {
		t.Errorf("installed: %s", w.Body)
	}
	r.enable(t, "")
	if w := r.as(t, 1, true, "POST", "/docker/emulators/nope/install", ""); w.Code != http.StatusNotFound {
		t.Errorf("installing what isn't listed: %d", w.Code)
	}
	if w := r.as(t, 1, true, "GET", "/docker/emulators/pcsx2/install", ""); w.Code != http.StatusNotFound {
		t.Errorf("a job that never started: %d", w.Code)
	}
	if w := r.as(t, 1, true, "DELETE", "/docker/emulators/pcsx2", ""); w.Code != http.StatusNoContent {
		t.Errorf("uninstall: %d %s", w.Code, w.Body)
	}
}
