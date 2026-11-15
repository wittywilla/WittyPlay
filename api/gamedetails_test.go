// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/gamemeta"
)

// gameHost is a server's host API as finding out about games uses it: items (with their files),
// SetItem, PutImage, secrets and tasks. It keeps what it was told.
type gameHost struct {
	mu       sync.Mutex
	items    map[int64]service.Item
	full     map[int64]map[string]any
	hidden   map[int64]bool // not for people: only WittyPlay itself sees them
	secrets  map[string]string
	patches  []wittymodule.ItemFields
	images   []string // roles
	tasks    atomic.Int32
	titlesMu sync.Mutex
	titles   map[string]string // each task's title, by its name
}

var taskNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newGameHost(t *testing.T) (*gameHost, *host.Client) {
	t.Helper()
	g := &gameHost{items: map[int64]service.Item{}, full: map[int64]map[string]any{}, hidden: map[int64]bool{}, secrets: map[string]string{}, titles: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		person := r.Header.Get(host.HeaderOnUser) != ""
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case r.Method == "GET" && p == "/items":
			q := r.URL.Query()
			want := map[int64]bool{}
			for _, s := range strings.Split(q.Get("ids"), ",") {
				if id, err := strconv.ParseInt(s, 10, 64); err == nil {
					want[id] = true
				}
			}
			out := []any{}
			for id := int64(1); id < 1000; id++ {
				it, ok := g.items[id]
				if !ok || (person && g.hidden[id]) || (len(want) > 0 && !want[id]) {
					continue
				}
				if lib := q.Get("library"); lib != "" && lib != strconv.FormatInt(it.Library, 10) {
					continue
				}
				if c := q.Get("facet.console"); c != "" && it.Facets["console"] != c {
					continue
				}
				if s := q.Get("search"); s != "" && !strings.Contains(strings.ToLower(it.Title), strings.ToLower(s)) {
					continue
				}
				if q.Get("full") == "1" {
					f := map[string]any{"id": id, "title": it.Title}
					for k, v := range g.full[id] {
						f[k] = v
					}
					out = append(out, f)
				} else {
					it.Files = nil
					out = append(out, it)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
		case r.Method == "GET" && strings.HasPrefix(p, "/items/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/items/"), 10, 64)
			it, ok := g.items[id]
			if !ok || (person && g.hidden[id]) {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"error":"no such item"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(it)
		case r.Method == "PATCH" && strings.HasPrefix(p, "/items/"):
			var f wittymodule.ItemFields
			_ = json.NewDecoder(r.Body).Decode(&f)
			g.patches = append(g.patches, f)
			w.WriteHeader(204)
		case r.Method == "POST" && p == "/images":
			b, _ := io.ReadAll(r.Body)
			if len(b) == 0 {
				w.WriteHeader(400)
				return
			}
			role := r.URL.Query().Get("role")
			g.images = append(g.images, role)
			_ = json.NewEncoder(w).Encode(map[string]string{"url": fmt.Sprintf("/api/images/item-%s-%s-%d", r.URL.Query().Get("item"), role, len(g.images))})
		case r.Method == "GET" && strings.HasPrefix(p, "/secrets/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"value": g.secrets[strings.TrimPrefix(p, "/secrets/")]})
		case r.Method == "POST" && strings.HasPrefix(p, "/tasks/"):
			// As the server: a task's name is letters, digits, - _ and . only.
			name := strings.TrimPrefix(p, "/tasks/")
			if !taskNameRe.MatchString(name) {
				w.WriteHeader(404)
				return
			}
			var in struct {
				Title string `json:"title"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.titlesMu.Lock()
			g.titles[name] = in.Title
			g.titlesMu.Unlock()
			g.tasks.Add(1)
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return g, host.NewAt(srv.URL, "t", nil)
}

// last is what SetItem was last told about an item, all its calls laid over each other.
func (g *gameHost) fields(id int64) wittymodule.ItemFields {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := wittymodule.ItemFields{ID: id}
	for _, f := range g.patches {
		if f.ID != id {
			continue
		}
		out = overlayFields(out, f)
		if f.Provider != "" {
			out.Provider, out.ProviderID = f.Provider, f.ProviderID
		}
		if f.Titles != nil {
			out.Titles = f.Titles
		}
		out.Clear = f.Clear
	}
	return out
}

func (g *gameHost) addGame(id, library int64, console, title string, files ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	it := service.Item{ID: id, Library: library, Kind: "game", Title: title, Facets: map[string]string{"console": console}}
	for _, f := range files {
		it.Files = append(it.Files, service.File{Path: f})
	}
	g.items[id] = it
}

// fakeSource answers from a table, and counts how often it's asked.
type fakeSource struct {
	id     string
	answer func(q gamemeta.Query) (*gamemeta.Info, error)
	asked  atomic.Int32
	mu     sync.Mutex
	titles []string
}

func (f *fakeSource) About() gamemeta.About {
	return gamemeta.About{ID: f.id, Name: "Fake " + f.id, Site: "https://example.test/" + f.id, Credit: "From " + f.id, License: "Test"}
}
func (f *fakeSource) Lookup(ctx context.Context, q gamemeta.Query) (*gamemeta.Info, error) {
	f.asked.Add(1)
	f.mu.Lock()
	f.titles = append(f.titles, q.Title)
	f.mu.Unlock()
	return f.answer(q)
}
func (f *fakeSource) Search(ctx context.Context, console, title string) ([]gamemeta.Candidate, error) {
	return []gamemeta.Candidate{{Source: f.id, ID: "42", Title: title, Score: 100}}, nil
}
func (f *fakeSource) Fetch(ctx context.Context, console, id string) (*gamemeta.Info, error) {
	in, err := f.answer(gamemeta.Query{Console: console, Title: id})
	if err != nil {
		return nil, err
	}
	in.ID = id
	return in, nil
}
func (f *fakeSource) Test(ctx context.Context) (string, error) { return "fine", nil }

// gameFixture is WittyPlay with a fake server and fake sources, its worker run by hand.
type gameFixture struct {
	a    *App
	h    http.Handler
	host *gameHost
	g    *gameMeta
	dir  string
	ctx  context.Context
}

func newGameFixture(t *testing.T, sources ...gamemeta.Source) *gameFixture {
	t.Helper()
	gh, hc := newGameHost(t)
	a, h := testApp(t, hc)
	g := a.gameWorker()
	g.Built = func() []gamemeta.Source { return sources }
	g.Pictures = &http.Client{Transport: roundTripErr{}}
	g.started = true // run by hand
	t.Cleanup(func() { gameMetasMu.Lock(); delete(gameMetas, a); gameMetasMu.Unlock() })
	return &gameFixture{a: a, h: h, host: gh, g: g, dir: t.TempDir(), ctx: context.Background()}
}

type roundTripErr struct{}

func (roundTripErr) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network here")
}

// game puts a game file in the library folder and on the fake server.
func (f *gameFixture) game(t *testing.T, id int64, name string) string {
	t.Helper()
	p := filepath.Join(f.dir, name)
	if err := os.WriteFile(p, []byte("made-up cartridge "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	title, _ := gamemeta.CleanName(gameBaseName(name))
	f.host.addGame(id, 1, "snes", title, p)
	return p
}

// describe asks WittyPlay about games as a scan does.
func (f *gameFixture) describe(t *testing.T, edited []string, ids ...int64) wittymodule.ScanDescribed {
	t.Helper()
	args := wittymodule.ScanDescribeArgs{Library: wittymodule.ScanLibrary{ID: 1, Kind: "games"}}
	for _, id := range ids {
		it := f.host.items[id]
		var files []string
		for _, x := range it.Files {
			files = append(files, x.Path)
		}
		args.Items = append(args.Items, wittymodule.ScanDescribeItem{ID: id, Kind: "game", Title: it.Title, Files: files, Facets: it.Facets, Edited: edited})
	}
	out, err := describeGames(f.ctx, f.a, args)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *gameFixture) work(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	defer cancel()
	f.g.work(ctx)
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	m.SetNRGBA(1, 1, color.NRGBA{200, 10, 10, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A scan's describe answers at once with what's beside the game and in the local database, and
// the worker then asks online and sets what it found through SetItem, leaving the owner's words.
func TestDescribeAnswersLocallyAndTheWorkerApplies(t *testing.T) {
	local := &fakeSource{id: "libretro", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "lr1", Title: "Mega Game: The Quest (USA)", Method: gamemeta.MethodChecksum, Confidence: 100, Released: "1994", Players: 2, Franchise: "Mega"}, nil
	}}
	esrb, _ := gamemeta.NewAge("ESRB", "T")
	online := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "9", Title: "Other Name", Method: gamemeta.MethodName, Confidence: 92, Developer: "Maker", Description: "Their words",
			Genres: []string{"Action"}, Ages: []gamemeta.AgeRating{esrb}, Scores: []gamemeta.Score{{Source: "A Magazine", Kind: "critic", Value: 90}}, Native: "メガゲーム"}, nil
	}}
	f := newGameFixture(t, local, online)
	p := f.game(t, 7, "Mega Game (USA).sfc")
	base := strings.TrimSuffix(p, ".sfc")
	if err := os.WriteFile(base+".nfo", []byte(`<?xml version="1.0"?><movie><plot>The owner's words</plot></movie>http://example.test/x`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+"-cover.png", pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	out := f.describe(t, []string{"genres"}, 7)
	if len(out.Items) != 1 {
		t.Fatalf("%+v", out)
	}
	got := out.Items[0]
	if got.ID != 7 || got.Overview != "The owner's words" || !strings.Contains(got.Poster, "item-7-poster") || got.Title != "Mega Game: The Quest" || got.Year != 1994 || got.Provider != "game:libretro" {
		t.Fatalf("described: %+v", got)
	}
	if online.asked.Load() != 0 {
		t.Fatal("describing asked an online source")
	}
	f.work(t)
	if online.asked.Load() != 1 {
		t.Fatalf("online asked %d times", online.asked.Load())
	}
	set := f.host.fields(7)
	if set.Studio != "Maker" || set.ContentRating != "ESRB T" || set.Rating == nil || *set.Rating != 9 || set.Provider != "game:libretro" {
		t.Errorf("set: %+v", set)
	}
	if set.Overview != "" || len(set.Genres) != 0 {
		t.Errorf("the owner's overview and a typed field were changed: %+v", set)
	}
	if set.Titles == nil || set.Titles.Native != "メガゲーム" {
		t.Errorf("other names: %+v", set.Titles)
	}
	// The name a checksum found is what later sources are asked about.
	if online.titles[len(online.titles)-1] != "Mega Game: The Quest" {
		t.Errorf("asked about %v", online.titles)
	}
	// What's known beside the item.
	w := call(t, f.h, "GET", "/games/7/metadata", 3, nil)
	var d gameDetails
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w.Code != 200 || d.Status != "done" || d.MatchSource != "libretro" || d.Players != 2 || d.Franchise != "Mega" || len(d.Scores) != 1 || len(d.Credits) != 2 || d.CRCs != "" {
		t.Errorf("metadata: %d %s", w.Code, w.Body)
	}
	if w := callAdmin(t, f.h, "GET", "/games/7/metadata", nil); !strings.Contains(w.Body.String(), `"crcs":"`) {
		t.Errorf("an administrator sees the checksums: %s", w.Body)
	}
	// Facets: the server's items, WittyPlay's players and series.
	f.host.full[7] = map[string]any{"genres": []string{"Action"}, "year": 1994, "rating": 9.0, "sortTitle": "Mega Game"}
	w = call(t, f.h, "GET", "/games/facets?library=1", 3, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"players":[{"value":"2","count":1}]`) || !strings.Contains(w.Body.String(), `"series":[{"value":"Mega","count":1}]`) ||
		!strings.Contains(w.Body.String(), `"decades":[{"value":"1990","count":1}]`) || !strings.Contains(w.Body.String(), `"rated":1`) {
		t.Errorf("facets: %s", w.Body)
	}
	// A game the server no longer has is forgotten.
	f.host.mu.Lock()
	delete(f.host.items, 7)
	f.host.mu.Unlock()
	f.a.pruneGames(f.ctx)
	var n int
	_ = f.a.DB.QueryRow(`SELECT COUNT(*) FROM wittyplay_game_details`).Scan(&n)
	if n != 0 {
		t.Errorf("%d rows stayed", n)
	}
}

func callAdmin(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	r.Header.Set(service.HeaderUser, "1")
	r.Header.Set(service.HeaderAdmin, "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRateLimitedSourceWaits(t *testing.T) {
	limited := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return nil, &gamemeta.RateLimited{Until: time.Now().Add(time.Hour), Why: "too fast"}
	}}
	fine := &fakeSource{id: "rawg", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "1", Method: gamemeta.MethodName, Confidence: 92, Developer: "Maker"}, nil
	}}
	f := newGameFixture(t, limited, fine)
	var ids []int64
	for i := 0; i < 5; i++ {
		id := int64(10 + i)
		f.game(t, id, fmt.Sprintf("Game %d.sfc", i))
		ids = append(ids, id)
	}
	f.describe(t, nil, ids...)
	f.work(t)
	if limited.asked.Load() != 1 {
		t.Errorf("a source that said stop was asked %d times", limited.asked.Load())
	}
	if fine.asked.Load() != 5 {
		t.Errorf("the other source was asked %d times", fine.asked.Load())
	}
	if got := f.host.fields(ids[4]); got.Studio != "Maker" {
		t.Errorf("%+v", got)
	}
	st := f.g.status(f.ctx)
	if st.Pending != 5 || len(st.Paused) != 1 || st.Paused[0].Source != "igdb" {
		t.Errorf("%+v", st)
	}
	for i := 0; i < 5; i++ {
		f.work(t)
	}
	if limited.asked.Load() != 1 {
		t.Errorf("asked %d times", limited.asked.Load())
	}
	var tries int
	_ = f.a.DB.QueryRow(`SELECT tries FROM wittyplay_game_details WHERE item_id = ?`, ids[0]).Scan(&tries)
	if tries != 0 {
		t.Errorf("waiting counted as %d failures", tries)
	}
	if f.host.tasks.Load() == 0 {
		t.Error("the work never showed with the server's tasks")
	}
}

// A source that fails is tried again later, a limited number of times.
func TestFailingSourceBacksOff(t *testing.T) {
	src := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) { return nil, errors.New("it fell over") }}
	f := newGameFixture(t, src)
	f.game(t, 7, "Game.sfc")
	f.describe(t, nil, 7)
	f.work(t)
	f.work(t)
	if src.asked.Load() != 1 {
		t.Errorf("asked %d times right away", src.asked.Load())
	}
	var next int64
	_ = f.a.DB.QueryRow(`SELECT next_try FROM wittyplay_game_details WHERE item_id = 7`).Scan(&next)
	if next <= time.Now().Unix() {
		t.Errorf("next try %d", next)
	}
	for i := 0; i < 12; i++ {
		_, _ = f.a.DB.Exec(`UPDATE wittyplay_game_details SET next_try = 0 WHERE status = 'pending'`)
		f.work(t)
	}
	var status string
	_ = f.a.DB.QueryRow(`SELECT status FROM wittyplay_game_details WHERE item_id = 7`).Scan(&status)
	if status != "done" {
		t.Errorf("status %q: it should give up in the end", status)
	}
}

// Pictures are fetched by WittyPlay, given to the server with PutImage, and set on the item.
func TestPicturesArePutOnTheServer(t *testing.T) {
	pic := pngBytes(t)
	var fetched atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone.png" {
			w.WriteHeader(404)
			return
		}
		fetched.Add(1)
		_, _ = w.Write(pic)
	}))
	t.Cleanup(srv.Close)
	src := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "1", Method: gamemeta.MethodChecksum, Confidence: 100, Poster: srv.URL + "/gone.png", MorePosters: []string{srv.URL + "/box.png"},
			Backdrop: srv.URL + "/wide.png", Art: []gamemeta.ArtRef{{Ref: srv.URL + "/shot.png", Kind: "screenshot"}}}, nil
	}}
	f := newGameFixture(t, src)
	f.g.Pictures = srv.Client()
	f.game(t, 7, "Game.sfc")
	f.describe(t, nil, 7)
	f.work(t)
	set := f.host.fields(7)
	if !strings.Contains(set.Poster, "item-7-poster") || !strings.Contains(set.Backdrop, "item-7-backdrop") {
		t.Fatalf("pictures: %+v", set)
	}
	// Fetched once: asking again changes nothing.
	n := fetched.Load()
	_, _ = f.a.DB.Exec(`UPDATE wittyplay_game_details SET art_wanted = 1`)
	f.work(t)
	if fetched.Load() != n {
		t.Errorf("fetched again: %d then %d", n, fetched.Load())
	}
	// A screenshot comes through WittyPlay, the first time anyone looks.
	w := call(t, f.h, "GET", "/games/7/art/0", 3, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Errorf("art: %d %s", w.Code, w.Header())
	}
	if w := call(t, f.h, "GET", "/games/7/art/5", 3, nil); w.Code != 404 {
		t.Errorf("no such art: %d", w.Code)
	}
}

// Sources, keys (the server's secrets: whether they're set, never what they are), matches.
func TestGameSourcesAndMatches(t *testing.T) {
	src := &fakeSource{id: "igdb", answer: func(q gamemeta.Query) (*gamemeta.Info, error) {
		return &gamemeta.Info{ID: "1", Title: "Found Game", Method: gamemeta.MethodName, Confidence: 90, Developer: "Maker"}, nil
	}}
	f := newGameFixture(t, src)
	f.host.secrets["rawgKey"] = "a-secret-key"
	f.game(t, 7, "Game.sfc")
	if w := call(t, f.h, "GET", "/gamemeta/sources", 3, nil); w.Code != 403 {
		t.Errorf("not an administrator: %d", w.Code)
	}
	w := callAdmin(t, f.h, "GET", "/gamemeta/sources", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "a-secret-key") || !strings.Contains(w.Body.String(), `"id":"igdb"`) {
		t.Fatalf("sources: %d %s", w.Code, w.Body)
	}
	w = callAdmin(t, f.h, "PUT", "/gamemeta/sources", map[string]any{"sources": []map[string]any{{"id": "igdb", "on": false}}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"igdb"`) {
		t.Fatalf("put sources: %d %s", w.Code, w.Body)
	}
	if order := f.a.savedSourceOrder(f.ctx); order[0].ID != "igdb" || order[0].On {
		t.Errorf("order: %+v", order)
	}
	if w := callAdmin(t, f.h, "POST", "/gamemeta/sources/igdb/test", map[string]any{}); !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("test: %s", w.Body)
	}
	if w := callAdmin(t, f.h, "POST", "/gamemeta/sources/nosuch/test", map[string]any{}); w.Code != 404 {
		t.Errorf("no such source: %d", w.Code)
	}
	w = callAdmin(t, f.h, "GET", "/gamemeta/status", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"keysSet":true`) {
		t.Errorf("status: %s", w.Body)
	}
	// Choosing a match by hand, with the source switched on again.
	_ = f.g.setSourceOrder(f.ctx, nil)
	w = callAdmin(t, f.h, "PUT", "/games/7/metadata/match", map[string]any{"source": "igdb", "id": "42"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"locked":true`) || !strings.Contains(w.Body.String(), `"matchMethod":"manual"`) {
		t.Fatalf("match: %d %s", w.Code, w.Body)
	}
	if set := f.host.fields(7); set.Title != "Found Game" || set.Provider != "game:igdb" || set.ProviderID != "42" {
		t.Errorf("chosen: %+v", set)
	}
	// And forgetting it: what sources filled in is cleared.
	if w := callAdmin(t, f.h, "DELETE", "/games/7/metadata/match", nil); w.Code != 200 {
		t.Fatalf("unmatch: %d %s", w.Code, w.Body)
	}
	f.host.mu.Lock()
	last := f.host.patches[len(f.host.patches)-1]
	f.host.mu.Unlock()
	if last.Title != "Game" || !containsStr(last.Clear, "studio") || !containsStr(last.Clear, "provider") {
		t.Errorf("cleared: %+v", last)
	}
	if w := callAdmin(t, f.h, "GET", "/games/7/metadata/search?q=Found", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"results":[`) {
		t.Errorf("search: %d %s", w.Code, w.Body)
	}
	if w := callAdmin(t, f.h, "POST", "/games/7/metadata/refresh", nil); w.Code != 202 {
		t.Errorf("refresh: %d %s", w.Code, w.Body)
	}
	if w := callAdmin(t, f.h, "POST", "/gamemeta/refetch", nil); w.Code != 202 {
		t.Errorf("refetch: %d", w.Code)
	}
	// A game someone can't see isn't theirs to ask about.
	f.host.hidden[7] = true
	if w := call(t, f.h, "GET", "/games/7/metadata", 3, nil); w.Code != 404 {
		t.Errorf("hidden: %d", w.Code)
	}
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// A key given to the server sends games to be asked again; an achievements key doesn't.
func TestNewKeysRequeue(t *testing.T) {
	f := newGameFixture(t)
	f.game(t, 7, "Game.sfc")
	f.describe(t, nil, 7)
	_, _ = f.a.DB.Exec(`UPDATE wittyplay_game_details SET status = 'done'`)
	f.g.readKeys(f.ctx)
	f.host.secrets["retroAchievementsKey"] = "ra"
	f.g.readKeys(f.ctx)
	var status string
	_ = f.a.DB.QueryRow(`SELECT status FROM wittyplay_game_details WHERE item_id = 7`).Scan(&status)
	if status != "done" {
		t.Errorf("an achievements key asked again: %s", status)
	}
	f.host.secrets["igdbClientId"] = "id"
	f.g.readKeys(f.ctx)
	_ = f.a.DB.QueryRow(`SELECT status FROM wittyplay_game_details WHERE item_id = 7`).Scan(&status)
	if status != "pending" {
		t.Errorf("a new key didn't ask again: %s", status)
	}
}

func TestGameNFO(t *testing.T) {
	p := filepath.Join(t.TempDir(), "g.nfo")
	_ = os.WriteFile(p, []byte(nfoMarker+"\n<movie><title> T </title><genre>A</genre><genre>A</genre><premiered>1991-02-03</premiered><ratings><rating><value>7.5</value></rating></ratings></movie>"), 0o644)
	m, ok := readGameNFO(p)
	if !ok || m.Title != "T" || len(m.Genres) != 1 || m.year() != 1991 || m.rating() == nil || *m.rating() != 7.5 || !m.writtenHere {
		t.Errorf("%+v %v", m, ok)
	}
}
