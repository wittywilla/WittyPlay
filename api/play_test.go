// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// liveHost is a server's host API as much as starting and playing games asks of it: items with
// their files, the live sessions it's told of, its accounts and its tools.
type liveHost struct {
	mu      sync.Mutex
	items   map[int64]service.Item
	hidden  map[int64]map[int64]bool // user → items they may not see
	started []host.Session
	ended   []string
	refuse  bool // the server says no to the next game
	ffmpeg  string
}

func (p *liveHost) client(t *testing.T) *host.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		path := strings.TrimPrefix(r.URL.Path, host.Base)
		p.mu.Lock()
		defer p.mu.Unlock()
		user := r.Header.Get(host.HeaderOnUser)
		switch {
		case strings.HasPrefix(path, "/items/"):
			var id, u int64
			fmt.Sscan(strings.TrimPrefix(path, "/items/"), &id)
			fmt.Sscan(user, &u)
			it, ok := p.items[id]
			if !ok || p.hidden[u][id] {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"no such item"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(it)
		case path == "/sessions" && r.Method == "POST":
			if p.refuse {
				w.WriteHeader(403)
				_, _ = w.Write([]byte(`{"error":"time is up"}`))
				return
			}
			var s host.Session
			_ = json.NewDecoder(r.Body).Decode(&s)
			p.started = append(p.started, s)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("h%d", len(p.started))})
		case strings.HasPrefix(path, "/sessions/") && r.Method == "DELETE":
			p.ended = append(p.ended, strings.TrimPrefix(path, "/sessions/"))
		case path == "/users":
			_ = json.NewEncoder(w).Encode(map[string]any{"users": []host.User{{ID: 3, Name: "willow", DisplayName: "Willow", Kind: "adult"}, {ID: 9, Name: "pat", Kind: "adult"}}})
		case path == "/tools":
			_ = json.NewEncoder(w).Encode(host.Tools{FFmpeg: p.ffmpeg, Encoders: map[string]bool{"libx264": true, "libvpx": true}, HardwareDone: true})
		case path == "/audit", path == "/notify":
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

func (p *liveHost) sessions() ([]host.Session, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]host.Session(nil), p.started...), append([]string(nil), p.ended...)
}

// playAs is a request from a person, with a sign-in and a kind.
func playAs(t *testing.T, h http.Handler, method, path string, user int64, kind string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if b, ok := body.([]byte); ok {
		rd = bytes.NewReader(b)
	} else {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Header.Set(service.HeaderUser, itoa(user))
	r.Header.Set(service.HeaderKind, kind)
	r.Header.Set(service.HeaderSignIn, "signin-"+itoa(user))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func liveItem(id int64, console, path string) service.Item {
	return service.Item{ID: id, Kind: "game", Title: "Game " + itoa(id), Facets: map[string]string{"console": console}, Files: []service.File{{Path: path}}}
}

func TestSavesAndBattery(t *testing.T) {
	ph := &liveHost{items: map[int64]service.Item{7: liveItem(7, "snes", "/games/a.sfc")}}
	a, h := testApp(t, ph.client(t))
	dir := a.saveDir(3, 7)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name, _ := stream.SlotName(1)
	_ = os.WriteFile(filepath.Join(dir, name+".state"), []byte("state one"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, name+".png"), []byte("png"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, name+".json"), []byte(`{"core":"snes9x"}`), 0o600)

	w := call(t, h, "GET", "/games/7/saves", 3, nil)
	var saves []saveSlot
	_ = json.Unmarshal(w.Body.Bytes(), &saves)
	if w.Code != 200 || len(saves) != 1 || saves[0].Slot != 1 || !saves[0].HasThumb || saves[0].Core != "snes9x" {
		t.Fatalf("saves: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/games/7/saves", 4, nil); w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("someone else's saves: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/games/8/saves", 3, nil); w.Code != 404 {
		t.Fatalf("a game that isn't there: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/7/saves/1/thumb", 3, nil); w.Code != 200 || w.Body.String() != "png" || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("thumb: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/games/7/saves/99999/thumb", 3, nil); w.Code != 404 {
		t.Fatalf("no such slot: %d", w.Code)
	}

	// Out with a header, and back in to another slot.
	w = call(t, h, "GET", "/games/7/saves/1/file", 3, nil)
	if w.Code != 200 || !bytes.HasPrefix(w.Body.Bytes(), stateFileMagic) || !strings.Contains(w.Header().Get("Content-Disposition"), "Game 7") {
		t.Fatalf("export: %d %q", w.Code, w.Body)
	}
	exported := w.Body.Bytes()
	if w := playAs(t, h, "PUT", "/games/7/saves/2/file", 3, "guest", exported); w.Code != 403 {
		t.Fatalf("a guest brought a save: %d", w.Code)
	}
	if w := call(t, h, "PUT", "/games/7/saves/0/file", 3, exported); w.Code != 400 {
		t.Fatalf("into the automatic slot: %d", w.Code)
	}
	w = call(t, h, "PUT", "/games/7/saves/2/file", 3, exported)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"warning":""`) {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	two, _ := stream.SlotName(2)
	if b, _ := os.ReadFile(filepath.Join(dir, two+".state")); string(b) != "state one" {
		t.Fatalf("imported %q", b)
	}
	// Damaged, and from another console's game.
	bad := append([]byte(nil), exported...)
	bad[len(bad)-1] ^= 1
	if w := call(t, h, "PUT", "/games/7/saves/2/file", 3, bad); w.Code != 422 {
		t.Fatalf("damaged: %d", w.Code)
	}
	if w := call(t, h, "PUT", "/games/7/saves/2/file", 3, liveStateFile(t, "nes", []byte("x"))); w.Code != 422 {
		t.Fatalf("another console's: %d", w.Code)
	}
	// What each account may keep is held to.
	old := maxAccountGameBytes
	maxAccountGameBytes = 10
	if w := call(t, h, "PUT", "/games/7/saves/3/file", 3, []byte("plain state")); w.Code != 409 {
		t.Fatalf("over the account's room: %d %s", w.Code, w.Body)
	}
	maxAccountGameBytes = old

	if w := call(t, h, "DELETE", "/games/7/saves/2", 3, nil); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := call(t, h, "DELETE", "/games/7/saves/2", 3, nil); w.Code != 404 {
		t.Fatalf("delete again: %d", w.Code)
	}

	// The battery save.
	if w := call(t, h, "GET", "/games/7/battery", 3, nil); w.Code != 404 {
		t.Fatalf("no battery yet: %d", w.Code)
	}
	if w := call(t, h, "PUT", "/games/7/battery", 3, []byte{}); w.Code != 422 {
		t.Fatalf("empty: %d", w.Code)
	}
	if w := call(t, h, "PUT", "/games/7/battery", 3, []byte("sram")); w.Code != 200 {
		t.Fatalf("battery in: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "PUT", "/games/7/battery", 3, []byte("sram 2")); w.Code != 200 {
		t.Fatalf("battery again: %d", w.Code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "save.srm.bak")); string(b) != "sram" {
		t.Fatalf("the old one wasn't kept: %q", b)
	}
	if w := call(t, h, "GET", "/games/7/battery", 3, nil); w.Code != 200 || w.Body.String() != "sram 2" {
		t.Fatalf("battery out: %d %q", w.Code, w.Body)
	}
	if w := call(t, h, "PUT", "/games/7/battery", 3, make([]byte, maxImportSRM+1)); w.Code != 413 {
		t.Fatalf("too large: %d", w.Code)
	}
}

func liveStateFile(t *testing.T, console string, body []byte) []byte {
	t.Helper()
	h, _ := json.Marshal(stateHeader{Console: console, Title: "Other", Size: int64(len(body)), CRC32: crc32.ChecksumIEEE(body)})
	var b bytes.Buffer
	b.Write(stateFileMagic)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(h)))
	b.Write(h)
	b.Write(body)
	return b.Bytes()
}

// A game in an archive is unpacked once, and prepare says how that goes; with no core for its
// console, it says so before unpacking anything.
func TestPrepare(t *testing.T) {
	t.Setenv("WMS_GAMING_TESTCORE", "1")
	dir := t.TempDir()
	plain := filepath.Join(dir, "Game.wittytest")
	_ = os.WriteFile(plain, []byte("a test game"), 0o644)
	packed := filepath.Join(dir, "Packed.zip")
	writeLiveZip(t, packed, "Packed.wittytest", []byte("a test game in a zip"))
	snesZip := filepath.Join(dir, "Snes.zip")
	writeLiveZip(t, snesZip, "Snes.sfc", make([]byte, 1024))
	ph := &liveHost{items: map[int64]service.Item{
		7: liveItem(7, wittyplay.TestConsole, plain), 8: liveItem(8, wittyplay.TestConsole, packed),
		9: liveItem(9, "snes", snesZip), 10: liveItem(10, "nosuch", plain), 11: liveItem(11, rigConsole, plain)}}
	_, h := testApp(t, ph.client(t))
	if w := call(t, h, "POST", "/games/7/prepare", 3, map[string]any{}); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"ready"`) {
		t.Fatalf("not packed: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/games/10/prepare", 3, map[string]any{}); w.Code != 404 {
		t.Fatalf("a console this server doesn't play: %d", w.Code)
	}
	if w := call(t, h, "POST", "/games/9/prepare", 3, map[string]any{}); w.Code != 409 || !strings.Contains(w.Body.String(), `"needCore":true`) {
		t.Fatalf("no core: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/games/11/play", 3, map[string]any{}); w.Code != 409 || !strings.Contains(w.Body.String(), `"rig":true`) {
		t.Fatalf("a rig's game: %d %s", w.Code, w.Body)
	}
	var st struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	for range 200 {
		w := call(t, h, "POST", "/games/8/prepare", 3, map[string]any{})
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		if w.Code != 200 || st.State == "ready" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.State != "ready" {
		t.Fatalf("unpacking: %+v", st)
	}
}

func writeLiveZip(t *testing.T, path, name string, body []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	e, _ := zw.Create(name)
	_, _ = e.Write(body)
	_ = zw.Close()
	_ = f.Close()
}

var liveRunnerOnce = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "wittyplay-api-runner-")
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "wittyplay-game-runner")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "../cmd/wittyplay-game-runner")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("%w\n%s", err, b)
	}
	return out, nil
})

// The whole of playing, with the real runner and its test core: starting a game as a host
// session, the game's routes, saving, someone joining with a code, rooms, screenshots, the
// connection limits, and the server ending the game.
func TestPlayingAGame(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no FFmpeg on PATH")
	}
	runner, err := liveRunnerOnce()
	if err != nil {
		t.Fatalf("building the runner: %v", err)
	}
	t.Setenv("WMS_WITTYPLAY_RUNNER", runner)
	t.Setenv("WMS_GAMING_TESTCORE", "1")
	dir := t.TempDir()
	rom := filepath.Join(dir, "Game.wittytest")
	_ = os.WriteFile(rom, []byte("a test game"), 0o644)
	packed := filepath.Join(dir, "Packed.zip")
	writeLiveZip(t, packed, "Packed.wittytest", []byte("a test game in a zip"))
	ph := &liveHost{ffmpeg: ffmpeg, items: map[int64]service.Item{7: liveItem(7, wittyplay.TestConsole, rom), 8: liveItem(8, wittyplay.TestConsole, packed)},
		hidden: map[int64]map[int64]bool{5: {7: true}}}
	a, h := testApp(t, ph.client(t))
	t.Cleanup(rpc.Mgr.StopAll)

	// The server says no: nothing starts.
	ph.refuse = true
	if w := call(t, h, "POST", "/games/7/play", 3, map[string]any{}); w.Code != 403 {
		t.Fatalf("refused by the server: %d %s", w.Code, w.Body)
	}
	if rpc.Mgr.Count() != 0 {
		t.Fatal("a game started that the server refused")
	}
	ph.refuse = false

	w := playAs(t, h, "POST", "/games/7/play", 3, "adult", map[string]any{"device": "Living room TV", "client": "browser"})
	if w.Code != 201 {
		t.Fatalf("play: %d %s", w.Code, w.Body)
	}
	var info stream.Info
	_ = json.Unmarshal(w.Body.Bytes(), &info)
	if info.ID == "" || info.ItemID != 7 || info.Console != wittyplay.TestConsole || len(info.Modes) == 0 {
		t.Fatalf("info: %s", w.Body)
	}
	sid := info.ID
	started, _ := ph.sessions()
	if len(started) != 1 || started[0].Kind != "game" || started[0].User != 3 || started[0].Item != 7 || started[0].SignIn != "signin-3" || started[0].Title != "Game 7" {
		t.Fatalf("the host session: %+v", started)
	}
	if w := call(t, h, "GET", "/play/"+sid, 3, nil); w.Code != 200 {
		t.Fatalf("get: %d", w.Code)
	}
	if w := call(t, h, "GET", "/play/"+sid, 9, nil); w.Code != 404 {
		t.Fatalf("someone else's game: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/playing", 3, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"device":"Living room TV"`) {
		t.Fatalf("playing: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/games/playing", 9, nil); w.Code != 204 {
		t.Fatalf("playing nothing: %d", w.Code)
	}
	if item, title, ok := playingNow(3); !ok || item != 7 || title != "Game 7" {
		t.Fatalf("playingNow: %d %q %v", item, title, ok)
	}

	// Saving, and what a person does to their game.
	if w := call(t, h, "POST", "/play/"+sid+"/action", 3, map[string]any{"type": "save", "slot": 1}); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/play/"+sid+"/action", 3, map[string]any{"type": "nonsense"}); w.Code != 400 {
		t.Fatalf("unknown action: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/7/saves", 3, nil); !strings.Contains(w.Body.String(), `"slot":1`) {
		t.Fatalf("saves after saving: %s", w.Body)
	}
	if w := call(t, h, "PUT", "/games/7/battery", 3, []byte("sram")); w.Code != 409 {
		t.Fatalf("a battery save while playing: %d", w.Code)
	}
	if w := call(t, h, "GET", "/play/"+sid+"/stats", 3, nil); w.Code != 200 {
		t.Fatalf("stats: %d", w.Code)
	}

	// Someone joins with a code; someone whose limits hide the game can't.
	if w := call(t, h, "POST", "/play/"+sid+"/invite", 9, nil); w.Code != 404 {
		t.Fatalf("a code from someone else: %d", w.Code)
	}
	w = call(t, h, "POST", "/play/"+sid+"/invite", 3, nil)
	var inv struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &inv)
	if w.Code != 200 || len(inv.Code) != seatCodeLen {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/play/join", 5, map[string]any{"code": inv.Code}); w.Code != 403 {
		t.Fatalf("a profile the game is hidden from joined: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/play/join", 9, map[string]any{"code": "ZZZZ"}); w.Code != 404 {
		t.Fatalf("a wrong code: %d", w.Code)
	}
	w = call(t, h, "POST", "/play/join", 9, map[string]any{"code": strings.ToLower(inv.Code)})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"seat":1`) || !strings.Contains(w.Body.String(), `"host":"Willow"`) {
		t.Fatalf("join: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/play/"+sid, 9, nil); w.Code != 200 {
		t.Fatalf("watching a game joined: %d", w.Code)
	}
	if w := call(t, h, "POST", "/play/"+sid+"/action", 9, map[string]any{"type": "reset"}); w.Code != 404 {
		t.Fatalf("someone who joined reset the host's game: %d", w.Code)
	}
	if w := call(t, h, "DELETE", "/play/"+sid, 9, nil); w.Code != 404 {
		t.Fatalf("someone who joined stopped the host's game: %d", w.Code)
	}
	if w := call(t, h, "GET", "/play/"+sid+"/seats", 9, nil); w.Code != 200 || strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("seats as a guest of the game: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "PUT", "/play/"+sid+"/seats/9", 3, map[string]any{"seat": 3}); w.Code != 200 || !strings.Contains(w.Body.String(), `"seat":3`) {
		t.Fatalf("moving a player: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "PUT", "/play/"+sid+"/seats/9", 3, map[string]any{"seat": 0}); w.Code != 400 {
		t.Fatalf("to the host's seat: %d", w.Code)
	}
	if item, _, ok := playingNow(9); !ok || item != 7 {
		t.Fatal("someone who joined isn't playing")
	}
	if w := call(t, h, "DELETE", "/play/"+sid+"/seats/9", 4, nil); w.Code != 404 {
		t.Fatalf("someone outside the game freed a seat: %d", w.Code)
	}
	if w := call(t, h, "DELETE", "/play/"+sid+"/seats/9", 9, nil); w.Code != 204 {
		t.Fatalf("leaving: %d", w.Code)
	}
	if w := call(t, h, "GET", "/play/"+sid, 9, nil); w.Code != 404 {
		t.Fatalf("watching after leaving: %d", w.Code)
	}
	if w := call(t, h, "POST", "/play/join", 9, map[string]any{"code": inv.Code}); w.Code != 200 {
		t.Fatalf("joining again: %d %s", w.Code, w.Body)
	}

	// Rooms: a Kids profile's game can't be opened; the host's can.
	if w := playAs(t, h, "POST", "/play/"+sid+"/room", 3, "kids", map[string]any{"open": true}); w.Code != 403 {
		t.Fatalf("a Kids profile opened a room: %d", w.Code)
	}
	if w := playAs(t, h, "POST", "/play/"+sid+"/room", 3, "adult", map[string]any{"open": true}); w.Code != 200 || !strings.Contains(w.Body.String(), `"open":true`) {
		t.Fatalf("opening a room: %d %s", w.Code, w.Body)
	}
	if w := playAs(t, h, "POST", "/play/join", 5, "kids", map[string]any{"room": sid}); w.Code != 404 {
		t.Fatalf("a Kids profile joined an open room: %d", w.Code)
	}

	// The connection's limits.
	if w := call(t, h, "POST", "/play/"+sid+"/stream", 3, map[string]any{"hello": map[string]any{"v": 1}, "sdp": "x"}); w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"bad-hello"`) {
		t.Fatalf("an old hello: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "GET", "/play/nosuch/stream", 3, nil); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"no-session"`) {
		t.Fatalf("a socket to no game: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "POST", "/play/nosuch/stream", 3, map[string]any{}); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"no-session"`) {
		t.Fatalf("no such game: %d %s", w.Code, w.Body)
	}
	for range peerOfferBurst {
		call(t, h, "POST", "/play/"+sid+"/stream", 3, map[string]any{"hello": map[string]any{"v": 1}})
	}
	if w := call(t, h, "POST", "/play/"+sid+"/stream", 3, map[string]any{}); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("past the offers: %d", w.Code)
	}

	// A screenshot kept in its memories, shown and deleted. The test core draws nothing until
	// someone watches, so with nobody connected there's no picture to keep, and it says so.
	if _, err := rpc.Mgr.Get(sid).Screenshot(); err != nil {
		if w := call(t, h, "POST", "/play/"+sid+"/screenshot", 3, nil); w.Code != 409 {
			t.Fatalf("keeping a screenshot of nothing: %d %s", w.Code, w.Body)
		}
		if w := call(t, h, "GET", "/play/"+sid+"/screenshot", 3, nil); w.Code != 409 {
			t.Fatalf("a screenshot of nothing: %d", w.Code)
		}
	}
	if w := call(t, h, "POST", "/play/"+sid+"/screenshot", 9, nil); w.Code != 404 {
		t.Fatalf("someone who joined kept a screenshot: %d", w.Code)
	}
	shot := struct{ Name string }{Name: "1800000000000000000.png"}
	_ = os.MkdirAll(a.shotsDir(3, 7), 0o700)
	_ = os.WriteFile(filepath.Join(a.shotsDir(3, 7), shot.Name), []byte("\x89PNG"), 0o600)
	if w := call(t, h, "GET", "/games/7/shots/"+shot.Name, 3, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("a kept screenshot: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/7/shots/"+shot.Name, 9, nil); w.Code != 404 {
		t.Fatalf("someone else's screenshot: %d", w.Code)
	}
	if w := call(t, h, "GET", "/games/7/shots/..%2Fx.png", 3, nil); w.Code != 404 {
		t.Fatalf("a name that isn't one: %d", w.Code)
	}
	if w := call(t, h, "DELETE", "/games/7/shots/"+shot.Name, 3, nil); w.Code != 204 {
		t.Fatalf("deleting it: %d", w.Code)
	}

	// The server ends the game: its person's time ran out.
	b, _ := json.Marshal(host.SessionStop{ID: "h1", User: 3, Reason: "time-up"})
	for _, f := range eventHandlers[host.EventSessionStop] {
		f(a, b)
	}
	waitLive(t, func() bool { return rpc.Mgr.Get(sid) == nil })
	waitLive(t, func() bool { _, ended := ph.sessions(); return len(ended) == 1 && ended[0] == "h1" })
	if _, _, ok := playingNow(3); ok {
		t.Fatal("still playing after the game ended")
	}
	if w := call(t, h, "GET", "/play/"+sid, 3, nil); w.Code != 404 {
		t.Fatalf("an ended game: %d", w.Code)
	}

	// Another game, stopped by its person: the answer comes once it has saved.
	w = call(t, h, "POST", "/games/7/play", 3, map[string]any{"resume": true})
	_ = json.Unmarshal(w.Body.Bytes(), &info)
	if w.Code != 201 {
		t.Fatalf("play again: %d %s", w.Code, w.Body)
	}
	if w := call(t, h, "DELETE", "/play/"+info.ID, 3, nil); w.Code != 200 {
		t.Fatalf("stop: %d", w.Code)
	}
	if rpc.Mgr.Get(info.ID) != nil {
		t.Fatal("the game went on after stopping")
	}
	waitLive(t, func() bool { _, ended := ph.sessions(); return len(ended) == 2 })

	// A game from an archive: unpacked as it starts, and its space let go when it ends.
	w = call(t, h, "POST", "/games/8/play", 3, map[string]any{})
	_ = json.Unmarshal(w.Body.Bytes(), &info)
	if w.Code != 201 || info.ItemID != 8 {
		t.Fatalf("play from an archive: %d %s", w.Code, w.Body)
	}
	if a.archives().Used() == 0 {
		t.Fatal("nothing was unpacked")
	}
	if w := call(t, h, "DELETE", "/play/"+info.ID, 3, nil); w.Code != 200 {
		t.Fatalf("stop: %d", w.Code)
	}
}

func waitLive(t *testing.T, ok func() bool) {
	t.Helper()
	for range 200 {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("it never happened")
}
