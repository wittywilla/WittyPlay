// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/stream"
)

// ---- a server, as much as the cores and consoles routes ask of it ----

type coreHost struct {
	mu      sync.Mutex
	machine bool                   // Hello's gamingCustomCores
	items   map[int64]service.Item // what this person may see
	counts  map[string]int         // games by console
	libs    []host.Library
	audits  []map[string]string
	users   []host.User
}

func (p *coreHost) audited(action string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.audits {
		if a["action"] == action {
			return true
		}
	}
	return false
}

func newCoreHost(t *testing.T) (*coreHost, *host.Client) {
	t.Helper()
	ph := &coreHost{items: map[int64]service.Item{}, counts: map[string]int{}, users: []host.User{{ID: 1, Name: "owner", Kind: "adult", Admin: true}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		ph.mu.Lock()
		defer ph.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		enc := json.NewEncoder(w)
		switch {
		case p == "/hello":
			_ = enc.Encode(host.Hello{Module: "wittyplay", Machine: map[string]bool{"gamingCustomCores": ph.machine}})
		case p == "/items/counts":
			_ = enc.Encode(map[string]any{"counts": ph.counts})
		case p == "/items":
			lib, _ := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64)
			out := []service.Item{}
			for _, it := range ph.items {
				if lib == 0 || it.Library == lib {
					it.Files = nil
					out = append(out, it)
				}
			}
			_ = enc.Encode(map[string]any{"items": out})
		case strings.HasPrefix(p, "/items/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/items/"), 10, 64)
			it, ok := ph.items[id]
			if !ok {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"no such item"}`))
				return
			}
			_ = enc.Encode(it)
		case p == "/libraries":
			_ = enc.Encode(map[string]any{"libraries": ph.libs})
		case p == "/audit":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			ph.audits = append(ph.audits, b)
			_ = enc.Encode(map[string]any{"ok": true})
		case p == "/users":
			_ = enc.Encode(map[string]any{"users": ph.users})
		case p == "/tools":
			_ = enc.Encode(host.Tools{Encoders: map[string]bool{"libx264": true}})
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return ph, host.NewAt(srv.URL, "t", nil)
}

// coresApp is a WittyPlay whose cores are only those a test places, with no runner, on a server
// whose graphics card is whatever the test says.
func coresApp(t *testing.T) (*App, http.Handler, *coreHost) {
	t.Helper()
	ph, hc := newCoreHost(t)
	old := extraCoreDirs
	extraCoreDirs = func() []string { return []string{} }
	t.Cleanup(func() { extraCoreDirs = old })
	oldProbe := probeGPU
	probeGPU = func(string, string, *slog.Logger) stream.GPUProbe { return stream.GPUProbe{Name: "A test card"} }
	t.Cleanup(func() { probeGPU = oldProbe })
	t.Cleanup(func() { hostOS = runtime.GOOS })
	t.Setenv("WMS_WITTYPLAY_RUNNER", filepath.Join(t.TempDir(), "no-such-runner"))
	a, h := testApp(t, hc)
	t.Cleanup(func() { enginesMu.Lock(); delete(engines, a); enginesMu.Unlock() })
	return a, h, ph
}

// setCardAnswer gives the graphics card check its answer.
func setCardAnswer(a *App, p stream.GPUProbe) {
	e := a.eng()
	e.gpuMu.Lock()
	e.gpu, e.gpuAt, e.gpuChecking = p, time.Now(), false
	e.gpuMu.Unlock()
}

// putCores puts core files where an administrator would: never loaded.
func putCores(t *testing.T, a *App, ids ...string) {
	t.Helper()
	dir := a.Cores().Dir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if err := os.WriteFile(filepath.Join(dir, id+"_libretro"+cores.LibraryExt()), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

var (
	adminID = service.Identity{User: 1, Admin: true, Kind: "adult"}
	otherID = service.Identity{User: 2, Kind: "adult"}
)

// asWho calls a route as someone; Confirmed sends the server's word that their password was right.
func asWho(t *testing.T, h http.Handler, method, path string, id service.Identity, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(service.HeaderUser, strconv.FormatInt(id.User, 10))
	r.Header.Set(service.HeaderKind, id.Kind)
	if id.Admin {
		r.Header.Set(service.HeaderAdmin, "1")
	}
	if id.Confirmed {
		r.Header.Set(service.HeaderConfirmed, "1")
	}
	if id.Item != nil {
		r.Header.Set(service.HeaderItem, service.EncodeItem(*id.Item))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// addCoreGame is a game on the server, in a folder of its own.
func addCoreGame(t *testing.T, ph *coreHost, id int64, console, name string, data []byte) service.Item {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	it := service.Item{ID: id, Library: 1, Kind: "game", Title: name, Facets: map[string]string{"console": console}, Files: []service.File{{Path: p, Size: int64(len(data))}}}
	ph.mu.Lock()
	ph.items[id] = it
	ph.mu.Unlock()
	return it
}

// ---- what each core is good at ----

// Every core the server knows where to download has a line saying what it's good at, and no
// line is for a core it doesn't know.
func TestEveryCoreHasANote(t *testing.T) {
	for id := range coreBuilds {
		n, ok := coreNotes[id]
		if !ok {
			t.Errorf("%s has no note", id)
			continue
		}
		if n.About == "" || !strings.HasSuffix(n.About, ".") {
			t.Errorf("%s: %q should be a sentence", id, n.About)
		}
		switch n.Strength {
		case "", strengthAccuracy, strengthSpeed, strengthBalanced:
		default:
			t.Errorf("%s: strength %q", id, n.Strength)
		}
	}
	for id := range coreNotes {
		if _, ok := coreBuilds[id]; !ok {
			t.Errorf("%s has a note, and no line in corebuilds.txt", id)
		}
	}
}

// A core that won't start without a BIOS says so, and says when one is there.
func TestCoreBIOSNeed(t *testing.T) {
	system := t.TempDir()
	if b := coreBIOS("fceumm", system); b != nil {
		t.Errorf("FCEUmm needs no BIOS: %+v", b)
	}
	b := coreBIOS("mednafen_psx", system)
	if b == nil || b.Found || len(b.Files) == 0 {
		t.Fatalf("Beetle PSX with no BIOS: %+v", b)
	}
	if err := os.WriteFile(filepath.Join(system, "scph5502.bin"), make([]byte, 512<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if b := coreBIOS("mednafen_psx", system); b == nil || !b.Found {
		t.Fatalf("Beetle PSX with a European BIOS: %+v", b)
	}
}

// ---- the routes ----

// The picker's view of a console: every core it can play on, installed or not, with what each is
// good at, where it runs, and whether it can be installed here; and only for administrators.
func TestCoreChoicesForAConsole(t *testing.T) {
	a, h, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A test card"})
	for _, c := range []struct{ method, path string }{
		{"GET", "/gaming/consoles/nes/core"}, {"PUT", "/gaming/consoles/nes/core"}, {"GET", "/gaming/cores/fceumm/install"},
		{"POST", "/gaming/cores/fceumm/install"}, {"DELETE", "/gaming/cores/fceumm"}, {"GET", "/gaming"}, {"DELETE", "/gaming/unpacked"},
		{"GET", "/gaming/games/7/core"}, {"PUT", "/gaming/games/7/core"},
	} {
		if w := asWho(t, h, c.method, c.path, otherID, nil); w.Code != 403 {
			t.Errorf("%s %s for someone who isn't an admin: %d", c.method, c.path, w.Code)
		}
	}
	if w := asWho(t, h, "GET", "/gaming/consoles/nope/core", adminID, nil); w.Code != 404 {
		t.Errorf("an unknown console: %d", w.Code)
	}
	if w := asWho(t, h, "GET", "/gaming/cores/..%2Fevil/install", adminID, nil); w.Code != 404 {
		t.Errorf("a path is not a core name: %d", w.Code)
	}
	putCores(t, a, "nestopia")
	w := asWho(t, h, "GET", "/gaming/consoles/nes/core", adminID, nil)
	var v coreChoicesView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v.Console != "nes" || len(v.Choices) < 3 || v.Choices[0].ID != "fceumm" {
		t.Fatalf("%+v", v)
	}
	byID := map[string]coreChoice{}
	for _, c := range v.Choices {
		byID[c.ID] = c
		if c.About == "" || c.Support == nil || c.License == "" {
			t.Errorf("%s: about %q, support %v, licence %q", c.ID, c.About, c.Support, c.License)
		}
	}
	if c := byID["nestopia"]; !c.Installed || c.CanInstall {
		t.Errorf("an installed core: %+v", c)
	}
	if c := byID["fceumm"]; c.Installed || c.CanInstall != cores.CanDownload() || !c.Recommended || c.Strength == "" {
		t.Errorf("a core to install: %+v", c)
	}
	if v.Effective != "nestopia" || v.Choice != "" {
		t.Errorf("with only Nestopia installed it plays, and nothing was chosen: %q %q", v.Effective, v.Choice)
	}
	// A core that isn't here can't be chosen; one that is, can, and it's written down.
	if w := asWho(t, h, "PUT", "/gaming/consoles/nes/core", adminID, map[string]any{"core": "mesen"}); w.Code != 400 {
		t.Errorf("a core that isn't installed: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/consoles/nes/core", adminID, map[string]any{"core": "nestopia"}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	_ = json.Unmarshal(asWho(t, h, "GET", "/gaming/consoles/nes/core", adminID, nil).Body.Bytes(), &v)
	if v.Choice != "nestopia" || a.PreferredCores(context.Background())["nes"] != "nestopia" || !ph.audited("console-default") {
		t.Errorf("choice %q", v.Choice)
	}
	// Nothing is being installed.
	var p struct {
		Installing      bool
		Received, Total int64
	}
	if err := json.Unmarshal(asWho(t, h, "GET", "/gaming/cores/fceumm/install", adminID, nil).Body.Bytes(), &p); err != nil || p.Installing {
		t.Errorf("%+v %v", p, err)
	}
	// The consoles every signed-in person sees carry the same lines.
	var list []struct {
		ID        string              `json:"id"`
		CoreAbout map[string]coreNote `json:"coreAbout"`
	}
	_ = json.Unmarshal(asWho(t, h, "GET", "/games/consoles", otherID, nil).Body.Bytes(), &list)
	found := false
	for _, c := range list {
		if c.ID == "nes" {
			found = c.CoreAbout["fceumm"].About == coreNotes["fceumm"].About
		}
	}
	if !found {
		t.Error("GET /games/consoles has no coreAbout for the NES's cores")
	}
}

// A game's own core: what could play it, choosing one that fits, and going back to the console's.
func TestAGamesOwnCore(t *testing.T) {
	a, h, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A test card"})
	putCores(t, a, "fceumm", "nestopia")
	addCoreGame(t, ph, 7, "nes", "Game.nes", []byte("NES\x1a"))
	if w := asWho(t, h, "GET", "/gaming/games/8/core", adminID, nil); w.Code != 404 {
		t.Errorf("a game the server doesn't have: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/games/7/core", adminID, map[string]any{"core": "snes9x"}); w.Code != 400 {
		t.Errorf("another console's core: %d", w.Code)
	}
	if w := asWho(t, h, "PUT", "/gaming/games/7/core", adminID, map[string]any{"core": "nestopia"}); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	a.NoteCorePlay(context.Background(), "nestopia", 7, 2, 0, 600, "")
	a.NoteCorePlay(context.Background(), "fceumm", 7, 2, 2, 900, "it crashed")
	w := asWho(t, h, "GET", "/gaming/games/7/core", adminID, nil)
	var v struct {
		Choice, Effective string
		Options           []gameCoreOption `json:"options"`
		Choices           []coreChoice     `json:"choices"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if v.Choice != "nestopia" || v.Effective != "nestopia" || len(v.Options) != 2 {
		t.Fatalf("%+v", v)
	}
	for _, o := range v.Options {
		if (o.ID == "nestopia") != o.Best || o.Here == nil {
			t.Errorf("%+v", o)
		}
		if o.ID == "fceumm" && (o.Failures != 2 || o.LastError != "it crashed") {
			t.Errorf("how FCEUmm went: %+v", o)
		}
	}
	if w := asWho(t, h, "PUT", "/gaming/games/7/core", adminID, map[string]any{"core": ""}); w.Code != 200 || a.gameCoreChoice(context.Background(), 7) != "" {
		t.Errorf("back to the console's: %d", w.Code)
	}
}

// The settings page's view, installing, and taking a core and the unpacked games away.
func TestTheGamingView(t *testing.T) {
	a, h, _ := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A test card"})
	putCores(t, a, "fceumm")
	w := asWho(t, h, "GET", "/gaming", adminID, nil)
	var v struct {
		Consoles   []ConsoleView    `json:"consoles"`
		Cores      []cores.Core     `json:"cores"`
		GPU        stream.GPUProbe  `json:"gpu"`
		SystemDir  string           `json:"systemDir"`
		BIOSFolder string           `json:"biosFolder"`
		Encoders   []string         `json:"encoders"`
		Running    int              `json:"running"`
		Unpacked   *json.RawMessage `json:"unpackedBytes"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(v.Consoles) < 20 || len(v.Cores) != 1 || v.GPU.Name != "A test card" || v.SystemDir == "" || v.BIOSFolder == "" || v.Unpacked == nil || len(v.Encoders) != 1 {
		t.Errorf("%+v", v)
	}
	if w := asWho(t, h, "DELETE", "/gaming/unpacked", adminID, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"unpackedBytes":0`) {
		t.Errorf("emptying the unpacked games: %d %s", w.Code, w.Body)
	}
	// Installing: what isn't offered, and what is (from the build server, stood in for here).
	if w := asWho(t, h, "POST", "/gaming/cores/not_a_core/install", adminID, nil); w.Code != 404 {
		t.Errorf("a core nobody offers: %d", w.Code)
	}
	old := installCore
	t.Cleanup(func() { installCore = old })
	installCore = func(_ context.Context, m *cores.Manager, id string) (*cores.Core, error) {
		if id == "nestopia" {
			return nil, errors.New("download: 404 Not Found")
		}
		return &cores.Core{ID: id, SHA256: "abc"}, nil
	}
	if has, known := coreBuildFor("mesen", runtime.GOOS, runtime.GOARCH); !known || has {
		if w := asWho(t, h, "POST", "/gaming/cores/mesen/install", adminID, nil); w.Code != 200 {
			t.Errorf("installing: %d %s", w.Code, w.Body)
		}
	}
	if has, known := coreBuildFor("nestopia", runtime.GOOS, runtime.GOARCH); !known || has {
		if w := asWho(t, h, "POST", "/gaming/cores/nestopia/install", adminID, nil); w.Code != 502 || !strings.Contains(w.Body.String(), "download server") {
			t.Errorf("not on the download server: %d %s", w.Code, w.Body)
		}
	}
	// Removing: only what this server installed.
	if w := asWho(t, h, "DELETE", "/gaming/cores/..%2Fx", adminID, nil); w.Code != 404 {
		t.Errorf("a path: %d", w.Code)
	}
	if w := asWho(t, h, "DELETE", "/gaming/cores/mesen", adminID, nil); w.Code != 404 {
		t.Errorf("a core that isn't here: %d", w.Code)
	}
}
