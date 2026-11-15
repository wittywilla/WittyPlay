// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/gamelogos"
	"github.com/wittywilla/wittyplay/gamemeta"
	"github.com/wittywilla/wittyplay/wittyplayfile"
)

func callRaw(t *testing.T, h http.Handler, method, path string, user int64, admin bool, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set(service.HeaderUser, itoa(user))
	if admin {
		r.Header.Set(service.HeaderAdmin, "1")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestGamePicturesAPI(t *testing.T) {
	f := newGameFixture(t)
	f.host.addGame(7, 1, "snes", "Made Up Quest", filepath.Join(f.dir, "Made Up Quest (USA).sfc"))
	f.host.addGame(8, 1, "snes", "Hidden Quest", filepath.Join(f.dir, "Hidden Quest (USA).sfc"))
	f.host.hidden[8] = true
	var got struct {
		Server     wittyplayfile.Doc             `json:"server"`
		Games      map[string]wittyplayfile.Look `json:"games"`
		CaseStyles []string                      `json:"caseStyles"`
	}
	w := callRaw(t, f.h, "GET", "/games/pictures", 3, false, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Games) != 0 || len(got.CaseStyles) != len(wittyplayfile.CaseStyles) {
		t.Fatalf("empty: %d %s", w.Code, w.Body)
	}
	doc := []byte(`{"wittyplay":1,"default":{"picture":"case"},"consoles":{"ps1":{"picture":"fitted"}},
		"games":[{"match":{"file":"made up quest (usa).sfc"},"case":"generic-box"},{"match":{"title":"Nobody Has This"},"label":"title"},{"match":{"title":"Hidden Quest"},"label":"title"}],"x-mine":1}`)
	if w := callRaw(t, f.h, "PUT", "/games/pictures", 3, false, doc); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer set the server's pictures: %d", w.Code)
	}
	w = callRaw(t, f.h, "PUT", "/games/pictures", 1, true, doc)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":2`) || !strings.Contains(w.Body.String(), `"unmatched":1`) {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	w = callRaw(t, f.h, "GET", "/games/pictures", 3, false, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Server.Default.Picture != "case" || got.Server.Consoles["ps1"].Picture != "fitted" || got.Games["7"].Case != "generic-box" || len(got.Games) != 1 {
		t.Fatalf("after put (a game someone can't see isn't listed): %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"x-mine":1`) {
		t.Errorf("an unknown field was lost: %s", w.Body)
	}
	for name, bad := range map[string][]byte{"not json": []byte("nope"), "bad value": []byte(`{"wittyplay":1,"default":{"picture":"stretched"}}`), "newer": []byte(`{"wittyplay":9}`)} {
		if w := callRaw(t, f.h, "PUT", "/games/pictures", 1, true, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	big := []byte(`{"wittyplay":1,"x":"` + strings.Repeat("y", wittyplayfile.MaxSize) + `"}`)
	if w := callRaw(t, f.h, "POST", "/games/pictures/check", 3, false, big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("too big: %d", w.Code)
	}
	// Checking a file for someone's own choices matches only games they can see.
	w = callRaw(t, f.h, "POST", "/games/pictures/check", 3, false, doc)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"matches":[[7],[],[]]`) {
		t.Errorf("check: %d %s", w.Code, w.Body)
	}
	// One game, by an administrator; an empty look takes it away.
	if w := callRaw(t, f.h, "PUT", "/games/7/picture", 3, false, []byte(`{"picture":"filled"}`)); w.Code != http.StatusForbidden {
		t.Fatalf("viewer: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/7/picture", 1, true, []byte(`{"picture":"filled"}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"picture":"filled"`) {
		t.Fatalf("one game: %d %s", w.Code, w.Body)
	}
	// The download: matched by file name, title and console.
	w = callRaw(t, f.h, "GET", "/games/pictures/file", 1, true, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != wittyplayfile.MediaType || !strings.Contains(w.Body.String(), `"file": "Made Up Quest (USA).sfc"`) {
		t.Errorf("file: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "PUT", "/games/7/picture", 1, true, []byte(`{}`)); w.Code != 200 {
		t.Fatalf("empty: %d", w.Code)
	}
	if l, _ := f.a.gamePictureFrom(f.ctx, 7, pictureByAdmin); !l.Empty() {
		t.Errorf("an empty look stayed: %+v", l)
	}
}

// A .wittyplay file beside a game is read, never written, when a scan describes it.
func TestSidecarLookIsRead(t *testing.T) {
	f := newGameFixture(t)
	p := f.game(t, 7, "Made Up Quest (USA).sfc")
	if err := os.WriteFile(strings.TrimSuffix(p, ".sfc")+".wittyplay", []byte(`{"wittyplay":1,"default":{"picture":"case","case":"generic-box"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.describe(t, nil, 7)
	if l, _ := f.a.gamePictureFrom(f.ctx, 7, pictureByFile); l.Picture != "case" {
		t.Errorf("look: %+v", l)
	}
}

// fakeWikimedia stands in for Wikimedia: console photos and logos.
type fakeWikimedia struct {
	pic   []byte
	none  bool
	asked atomic.Int32
}

func (f *fakeWikimedia) ConsolePhoto(ctx context.Context, console, name string) (gamelogos.LogoCandidate, error) {
	f.asked.Add(1)
	if f.none {
		return gamelogos.LogoCandidate{}, gamelogos.ErrNoPhoto
	}
	return gamelogos.LogoCandidate{SourceID: console + ".jpg", Credit: "Someone", License: "Public domain", PageURL: "https://commons.wikimedia.org/x"}, nil
}

func (f *fakeWikimedia) Fetch(ctx context.Context, c gamelogos.LogoCandidate) ([]byte, error) {
	return f.pic, nil
}

func TestConsolePhotosAndBrandLogos(t *testing.T) {
	src := &fakeWikimedia{pic: pngBytes(t)}
	was := photoSource
	photoSource = func() consolePhotoSource { return src }
	t.Cleanup(func() { photoSource = was })
	f := newGameFixture(t)
	// Nothing is asked before an administrator agreed.
	if w := callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil); w.Code != 404 || src.asked.Load() != 0 {
		t.Fatalf("before agreeing: %d, asked %d", w.Code, src.asked.Load())
	}
	w := callRaw(t, f.h, "GET", "/games/brand-logos", 3, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || strings.Contains(w.Body.String(), "agreedAt") {
		t.Fatalf("brand logos: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "PUT", "/games/brand-logos", 3, false, []byte(`{"enabled":true,"accepted":true}`)); w.Code != 403 {
		t.Fatalf("a viewer agreed: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/brand-logos", 1, true, []byte(`{"enabled":true}`)); w.Code != 400 {
		t.Fatalf("without accepting: %d", w.Code)
	}
	// The logo service is stopped from looking anything up in this test: no network.
	f.a.logos().Source = nil
	w = callRaw(t, f.h, "PUT", "/games/brand-logos", 1, true, []byte(`{"enabled":true,"accepted":true}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) || !strings.Contains(w.Body.String(), `"pending":`) {
		t.Fatalf("agreeing: %d %s", w.Code, w.Body)
	}
	if g, _ := f.a.logos().LogoFor(f.ctx, logoConsole, "Super Nintendo Entertainment System"); g == nil {
		t.Fatal("no row for a console's logo")
	}
	// Console photos are their own opt-in, off by default: agreeing to logos doesn't fetch them.
	w = callRaw(t, f.h, "GET", "/games/console-photos", 3, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) || !strings.Contains(w.Body.String(), "commons.wikimedia.org") || !strings.Contains(w.Body.String(), "Creative Commons") {
		t.Fatalf("console photos by default: %d %s", w.Code, w.Body)
	}
	if w := callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil); w.Code != 404 || src.asked.Load() != 0 {
		t.Fatalf("off by default: %d, asked %d", w.Code, src.asked.Load())
	}
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 3, false, []byte(`{"enabled":true}`)); w.Code != 403 {
		t.Fatalf("a viewer opted in: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{}`)); w.Code != 400 {
		t.Fatalf("no answer: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{"enabled":true}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("opting in: %d %s", w.Code, w.Body)
	}
	// Now a console's photo is fetched once, kept, and served by WittyPlay.
	w = callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"/api/modules/wittyplay/console-photos/snes/image?v=`) || !strings.Contains(w.Body.String(), `"credit":"Someone"`) {
		t.Fatalf("photo: %d %s", w.Code, w.Body)
	}
	callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil)
	if src.asked.Load() != 1 {
		t.Errorf("asked %d times", src.asked.Load())
	}
	if w := callRaw(t, f.h, "GET", "/console-photos/snes/image", 3, false, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("image: %d", w.Code)
	}
	if w := callRaw(t, f.h, "GET", "/console-photos/..%2fx", 3, false, nil); w.Code != 400 && w.Code != 404 {
		t.Errorf("a bad console: %d", w.Code)
	}
	// None found is remembered.
	src.none = true
	callRaw(t, f.h, "GET", "/console-photos/nes", 3, false, nil)
	callRaw(t, f.h, "GET", "/console-photos/nes", 3, false, nil)
	if src.asked.Load() != 2 {
		t.Errorf("a console with none was asked about again: %d", src.asked.Load())
	}
	// Removing them all turns them off.
	if w := callRaw(t, f.h, "DELETE", "/games/brand-logos", 1, true, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if f.a.brandLogosOn(f.ctx) {
		t.Error("still on")
	}
	// Turning console photos off deletes them, and nothing more is fetched.
	if w := callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil); w.Code != 200 {
		t.Errorf("logos off took console photos with them: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{"enabled":false}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("opting out: %d %s", w.Code, w.Body)
	}
	src.none = false
	before := src.asked.Load()
	for _, p := range []string{"/console-photos/snes", "/console-photos/snes/image", "/console-photos/gb"} {
		if w := callRaw(t, f.h, "GET", p, 3, false, nil); w.Code != 404 {
			t.Errorf("%s while off: %d", p, w.Code)
		}
	}
	if src.asked.Load() != before {
		t.Error("Wikimedia was asked while console photos were off")
	}
	if _, err := os.Stat(f.a.consolePhotoPath("snes")); !os.IsNotExist(err) {
		t.Errorf("the photo kept wasn't deleted: %v", err)
	}
}

const achTestKey = "achievements-test-key"

func TestItemAchievements(t *testing.T) {
	cart := []byte(strings.Repeat("made-up cartridge bytes ", 100))
	sum := md5.Sum(cart)
	hash := hex.EncodeToString(sum[:])
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Badge/4321_lock.png" {
			_, _ = w.Write(append([]byte("\x89PNG\r\n\x1a\n"), "badge"...))
			return
		}
		if r.URL.Query().Get("y") != achTestKey {
			http.Error(w, "{}", 401)
			return
		}
		switch r.URL.Path {
		case "/API/API_GetGameList.php":
			lists.Add(1)
			fmt.Fprintf(w, `[{"Title":"Test Cart","ID":777,"ConsoleID":7,"NumAchievements":1,"Points":25,"Hashes":[%q]}]`, hash)
		case "/API/API_GetGameExtended.php":
			_, _ = w.Write([]byte(`{"ID":777,"Title":"Test Cart","Achievements":{"1":{"ID":1,"Title":"Begin","Description":"Start the game","Points":25,"BadgeName":"4321","DisplayOrder":0}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	f := newGameFixture(t)
	cache := filepath.Join(t.TempDir(), "ra")
	keyed := false
	f.g.Built = func() []gamemeta.Source {
		key := ""
		if keyed {
			key = achTestKey
		}
		return []gamemeta.Source{&gamemeta.RetroAchievements{Key: key, HTTP: srv.Client(), Dir: cache, Base: srv.URL + "/API", MediaBase: srv.URL, Gap: time.Millisecond}}
	}
	romDir := t.TempDir()
	rom := filepath.Join(romDir, "Test Cart.nes")
	if err := os.WriteFile(rom, append([]byte("NES\x1a\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), cart...), 0o444); err != nil {
		t.Fatal(err)
	}
	f.host.addGame(7, 1, "nes", "Test Cart", rom)
	f.host.addGame(8, 1, "ps1", "Disc", filepath.Join(romDir, "Disc.cue"))
	get := func(id int64) (int, AchievementsAnswer, string) {
		t.Helper()
		w := callRaw(t, f.h, "GET", fmt.Sprintf("/items/%d/achievements", id), 3, false, nil)
		var a AchievementsAnswer
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return w.Code, a, w.Body.String()
	}
	if _, a, body := get(7); a.State != "nokey" || !strings.Contains(body, `"game":null`) {
		t.Errorf("no key: %s", body)
	}
	keyed = true
	f.host.secrets["retroAchievementsKey"] = achTestKey
	f.g.readKeys(f.ctx)
	code, a, body := get(7)
	if code != 200 || a.State != "ok" || a.Game == nil || a.Game.ID != 777 || len(a.Achievements) != 1 {
		t.Fatalf("%d %s", code, body)
	}
	if a.Achievements[0].Badge != "/api/modules/wittyplay/items/7/achievements/badge/4321" {
		t.Errorf("%+v", a.Achievements[0])
	}
	if strings.Contains(body, achTestKey) || strings.Contains(body, srv.URL) || strings.Contains(body, romDir) {
		t.Errorf("gives away a key, the service or a path: %s", body)
	}
	if entries, _ := os.ReadDir(romDir); len(entries) != 1 {
		t.Errorf("something was written beside the game: %v", entries)
	}
	get(7)
	if n := lists.Load(); n != 1 {
		t.Errorf("the console's list was fetched %d times", n)
	}
	if w := callRaw(t, f.h, "GET", "/items/7/achievements/badge/4321", 3, false, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("badge: %d", w.Code)
	}
	if w := callRaw(t, f.h, "GET", "/items/7/achievements/badge/9999", 3, false, nil); w.Code != 404 {
		t.Errorf("another game's badge: %d", w.Code)
	}
	if _, a, _ := get(8); a.State != "unsupported" {
		t.Errorf("a disc: %+v", a)
	}
	f.host.hidden[7] = true
	if code, _, _ := get(7); code != 404 {
		t.Errorf("a game someone can't see: %d", code)
	}
}
