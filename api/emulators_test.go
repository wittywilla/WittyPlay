// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/rpc"
)

// A game in a standalone emulator is a host session like any game: the server is told, a refusal
// is the backend's words, and the server ending the session stops the emulator.
func TestPlayWithAnEmulator(t *testing.T) {
	dir := t.TempDir()
	rom := filepath.Join(dir, "Game.iso")
	_ = os.WriteFile(rom, []byte("a disc"), 0o644)
	ph := &liveHost{items: map[int64]service.Item{7: liveItem(7, "ps2", rom), 8: liveItem(8, "ps2", filepath.Join(dir, "gone.iso")), 9: liveItem(9, rigConsole, rom)}}
	a, h := testApp(t, ph.client(t))
	t.Setenv("WMS_WITTYPLAY_RUNNER", "/bin/true") // Configure needs a runner; none is started here

	var mu sync.Mutex
	var launched []rpc.LaunchOptions
	var stopped []string
	refusal := (*rpc.Refusal)(nil)
	ended := make(chan struct{})
	oldLaunch, oldStop, oldDone := launchEmulator, stopEmulator, emulatorDone
	t.Cleanup(func() { launchEmulator, stopEmulator, emulatorDone = oldLaunch, oldStop, oldDone })
	launchEmulator = func(_ context.Context, o rpc.LaunchOptions) (*rpc.DockerSession, error) {
		mu.Lock()
		defer mu.Unlock()
		if refusal != nil {
			return nil, refusal
		}
		launched = append(launched, o)
		return &rpc.DockerSession{ID: "0123456789abcdef", UserID: o.UserID, ItemID: o.ItemID, Emulator: o.Emulator, Name: "PCSX2", KasmURL: "/kasm/0123456789abcdef/" + strings.Repeat("a", 64) + "/"}, nil
	}
	emulatorDone = func(*rpc.DockerSession) <-chan struct{} { return ended }
	stopEmulator = func(_ context.Context, id string) error {
		mu.Lock()
		defer mu.Unlock()
		stopped = append(stopped, id)
		close(ended)
		return nil
	}

	if w := call(t, h, "POST", "/games/7/play/emulator", 3, map[string]any{"emulator": "../x"}); w.Code != 400 {
		t.Fatalf("a name that isn't an emulator's: %d", w.Code)
	}
	if w := call(t, h, "POST", "/games/9/play/emulator", 3, map[string]any{"emulator": "pcsx2"}); w.Code != 404 {
		t.Fatalf("a rig's game: %d", w.Code)
	}
	if w := call(t, h, "POST", "/games/8/play/emulator", 3, map[string]any{"emulator": "pcsx2"}); w.Code != 404 {
		t.Fatalf("a missing file: %d", w.Code)
	}
	refusal = &rpc.Refusal{Code: "no-bios", Text: "PCSX2 needs a BIOS."}
	if w := call(t, h, "POST", "/games/7/play/emulator", 3, map[string]any{"emulator": "pcsx2"}); w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"no-bios"`) {
		t.Fatalf("refused: %d %s", w.Code, w.Body)
	}
	refusal = &rpc.Refusal{Code: "busy", Text: "Busy.", Busy: true}
	if w := call(t, h, "POST", "/games/7/play/emulator", 3, map[string]any{"emulator": "pcsx2"}); w.Code != 503 {
		t.Fatalf("busy: %d", w.Code)
	}
	if _, ended := ph.sessions(); len(ended) != 2 {
		t.Fatalf("the server's sessions for refused starts weren't let go: %v", ended)
	}
	refusal = nil

	w := playAs(t, h, "POST", "/games/7/play/emulator", 3, "adult", map[string]any{"emulator": "pcsx2"})
	var s emulatorSession
	_ = json.Unmarshal(w.Body.Bytes(), &s)
	if w.Code != 201 || s.ID != "0123456789abcdef" || s.Stop != "/api/v1/modules/wittyplay/docker/sessions/0123456789abcdef" ||
		!strings.HasPrefix(s.KasmURL, "/api/v1/modules/wittyplay/kasm/") || s.Notices == nil {
		t.Fatalf("started: %d %s", w.Code, w.Body)
	}
	if len(launched) != 1 || launched[0].ROM != rom || launched[0].UserID != 3 || launched[0].SystemDir != a.SystemDir() {
		t.Fatalf("launched with %+v", launched)
	}
	started, _ := ph.sessions()
	if last := started[len(started)-1]; last.Kind != "game" || last.Item != 7 || last.SignIn != "signin-3" {
		t.Fatalf("host session: %+v", last)
	}
	if !live.has(s.ID) {
		t.Fatal("the game isn't followed")
	}

	// The server ends it: the emulator is stopped, and the session let go.
	b, _ := json.Marshal(host.SessionStop{ID: "h3", User: 3, Reason: "signed-out"})
	for _, f := range eventHandlers[host.EventSessionStop] {
		f(a, b)
	}
	waitLive(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(stopped) == 1 })
	waitLive(t, func() bool { _, ended := ph.sessions(); return len(ended) == 3 && ended[2] == "h3" })
	waitLive(t, func() bool { return !live.has(s.ID) })
}

// The backend's routes are the module's own now, and the desktop's ticket route lets nothing
// through for a game that isn't one of ours.
func TestDockerAndKasmRoutes(t *testing.T) {
	ph := &liveHost{}
	_, h := testApp(t, ph.client(t))
	if w := call(t, h, "GET", "/docker/sessions", 0, nil); w.Code != 401 {
		t.Fatalf("nobody: %d", w.Code)
	}
	if w := call(t, h, "GET", "/docker/sessions", 3, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) == "" {
		t.Fatalf("sessions: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/docker/settings", 3, nil); w.Code != 403 {
		t.Fatalf("an administrator's route: %d", w.Code)
	}
	r := httptest.NewRequest("GET", "/docker/settings", nil)
	r.Header.Set(service.HeaderUser, "1")
	r.Header.Set(service.HeaderAdmin, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("settings as an administrator: %d %s", w.Code, w.Body)
	}
	ticket := strings.Repeat("a", 64)
	for _, p := range []string{"/kasm/0123456789abcdef/" + ticket + "/", "/kasm/0123456789abcdef/short/", "/kasm/NOTHEX!!/" + ticket + "/x"} {
		if w := call(t, h, "GET", p, 0, nil); w.Code != 404 {
			t.Fatalf("%s: %d", p, w.Code)
		}
	}
	// A redirect within the desktop is made relative to where it was asked.
	rec := httptest.NewRecorder()
	k := &kasmAnswer{ResponseWriter: rec, base: "/kasm/s/t/", path: "/kasm/s/t/a/b"}
	k.Header().Set("Location", "/kasm/s/t/vnc.html")
	k.WriteHeader(302)
	if got := rec.Header().Get("Location"); got != "../vnc.html" {
		t.Fatalf("location %q", got)
	}
	rec = httptest.NewRecorder()
	k = &kasmAnswer{ResponseWriter: rec, base: "/kasm/s/t/", path: "/kasm/s/t/"}
	k.Header().Set("Location", "https://elsewhere.example/")
	k.WriteHeader(302)
	if got := rec.Header().Get("Location"); got != "" {
		t.Fatalf("a redirect away: %q", got)
	}
}
