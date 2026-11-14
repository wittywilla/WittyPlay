// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/gameid/fixture"
)

// gameTree writes a small games library: a disc kept as a sheet and its tracks, a game on two
// discs with a playlist, another on two discs without one, a zip, and one game in two regions.
func gameTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, b []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cue := func(bins ...string) []byte {
		var b strings.Builder
		for i, bin := range bins {
			b.WriteString("FILE \"" + bin + "\" BINARY\n  TRACK 0" + strconv.Itoa(i+1) + " MODE2/2352\n    INDEX 01 00:00:00\n")
		}
		return []byte(b.String())
	}
	write("PS1/Cue Game (USA)/Cue Game (USA) (Track 1).bin", fixture.Raw(fixture.PS1("SLUS_111.11")))
	write("PS1/Cue Game (USA)/Cue Game (USA) (Track 2).bin", make([]byte, 4096))
	write("PS1/Cue Game (USA)/Cue Game (USA).cue", cue("Cue Game (USA) (Track 1).bin", "Cue Game (USA) (Track 2).bin"))
	write("PS1/Big Game/Big Game (USA) (Disc 1).bin", fixture.Raw(fixture.PS1("SLUS_222.21")))
	write("PS1/Big Game/Big Game (USA) (Disc 1).cue", cue("Big Game (USA) (Disc 1).bin"))
	write("PS1/Big Game/Big Game (USA) (Disc 2).bin", fixture.Raw(fixture.PS1("SLUS_222.22")))
	write("PS1/Big Game/Big Game (USA) (Disc 2).cue", cue("Big Game (USA) (Disc 2).bin"))
	write("PS1/Big Game/Big Game (USA).m3u", []byte("Big Game (USA) (Disc 1).cue\nBig Game (USA) (Disc 2).cue\n"))
	write("PS1/Two Discs (Europe) (Disc 1).bin", fixture.Raw(fixture.PS1("SLES_333.31")))
	write("PS1/Two Discs (Europe) (Disc 1).cue", cue("Two Discs (Europe) (Disc 1).bin"))
	write("PS1/Two Discs (Europe) (Disc 2).bin", fixture.Raw(fixture.PS1("SLES_333.32")))
	write("PS1/Two Discs (Europe) (Disc 2).cue", cue("Two Discs (Europe) (Disc 2).bin"))
	nes := func(name string) []byte {
		b := fixture.NES()
		copy(b[len(b)-64:], name) // different games, different checksums
		return b
	}
	write("NES/Mega Game (USA).nes", nes("usa"))
	write("NES/Mega Game (Europe).nes", nes("europe"))
	write("NES/readme.txt", []byte("not a game"))
	write("NES/.hidden/Skip (USA).nes", nes("skip"))
	zp := filepath.Join(root, "NES", "Zipped Game (USA).zip")
	f, err := os.Create(zp)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	e, _ := zw.Create("Zipped Game (USA).nes")
	_, _ = e.Write(nes("zipped"))
	_ = zw.Close()
	_ = f.Close()
	return root
}

// walkLike lists a folder as the server's walk sends it.
func walkLike(root string) []wittymodule.ScanFile {
	var out []wittymodule.ScanFile
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p != root && (strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "$")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		st, _ := d.Info()
		out = append(out, wittymodule.ScanFile{Path: p, Root: root, Size: st.Size(), MTime: st.ModTime().Unix()})
		return nil
	})
	return out
}

// identify runs a whole scan through the scan hooks, the files in batches of three.
func identify(t *testing.T, hooks *service.ScanHooks, scan string, lib wittymodule.ScanLibrary) wittymodule.ScanIdentified {
	t.Helper()
	ctx := context.Background()
	files := walkLike(lib.Paths[0])
	total := len(files)
	for len(files) > 0 {
		n := min(3, len(files))
		got, err := hooks.Identify(ctx, wittymodule.ScanIdentifyArgs{Scan: scan, Library: lib, Files: files[:n], Total: total})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 0 {
			t.Fatal("a batch that isn't the last answers nothing")
		}
		files = files[n:]
	}
	out, err := hooks.Identify(ctx, wittymodule.ScanIdentifyArgs{Scan: scan, Library: lib, Total: total, Done: true})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gamesHost is a server's host API with one games library, its items as the last scan left them.
type gamesHost struct {
	lib     wittymodule.ScanLibrary
	items   map[int64]service.Item
	rescans atomic.Int32
	// scan is the library's scan as the host's libraries say it ({mode, since}), when set.
	scan atomic.Pointer[map[string]any]
	// reports are the progress reports of the library's scan, "done/total".
	reportsMu sync.Mutex
	reports   []string
}

func (g *gamesHost) client(t *testing.T) *host.Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		switch {
		case r.Method == "GET" && p == "/libraries":
			l := map[string]any{"id": g.lib.ID, "name": "Games", "kind": "games", "console": g.lib.Facet, "paths": g.lib.Paths}
			if sc := g.scan.Load(); sc != nil {
				l["scan"] = *sc
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"libraries": []any{l}})
		case r.Method == "POST" && p == "/tasks/scan-"+strconv.FormatInt(g.lib.ID, 10):
			var in struct{ Done, Total int }
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.reportsMu.Lock()
			g.reports = append(g.reports, strconv.Itoa(in.Done)+"/"+strconv.Itoa(in.Total))
			g.reportsMu.Unlock()
			w.WriteHeader(204)
		case r.Method == "POST" && p == "/libraries/"+strconv.FormatInt(g.lib.ID, 10)+"/scan":
			g.rescans.Add(1)
			w.WriteHeader(204)
		case r.Method == "GET" && p == "/items":
			var out []service.Item
			for _, s := range strings.Split(r.URL.Query().Get("ids"), ",") {
				id, _ := strconv.ParseInt(s, 10, 64)
				if it, ok := g.items[id]; ok {
					it.Files = nil
					out = append(out, it)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": out})
		case r.Method == "GET" && strings.HasPrefix(p, "/items/"):
			id, _ := strconv.ParseInt(strings.TrimPrefix(p, "/items/"), 10, 64)
			if it, ok := g.items[id]; ok {
				_ = json.NewEncoder(w).Encode(it)
				return
			}
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no such item"}`))
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":"no"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

// What a scan of a small library finds, through the scan hooks: each game once, with its discs
// in order, its console, and its other version under it; then the routes that put it right.
func TestIdentifyThroughTheScanHooks(t *testing.T) {
	root := gameTree(t)
	lib := wittymodule.ScanLibrary{ID: 1, Kind: "games", Facet: "auto", Paths: []string{root}}
	gh := &gamesHost{lib: lib, items: map[int64]service.Item{}}
	_, h := testApp(t, gh.client(t))
	hooks := ScanHooks()

	choices, err := hooks.Choices(context.Background(), wittymodule.ScanChoicesArgs{LibraryKind: "games"})
	if err != nil || choices.Facet != "console" || len(choices.Values) < 20 {
		t.Fatalf("choices: %+v %v", choices, err)
	}
	for _, c := range choices.Values {
		if c.ID == "wittytest" || c.ID == wittymodule.ScanAuto {
			t.Fatalf("%s isn't a choice outside testing", c.ID)
		}
	}

	out := identify(t, hooks, "first", lib)
	byRel := map[string]wittymodule.ScanItem{}
	var rels []string
	for _, it := range out.Items {
		rel, _ := filepath.Rel(root, it.Files[0])
		rel = filepath.ToSlash(rel)
		byRel[rel] = it
		rels = append(rels, rel+"="+it.Facets["console"])
	}
	sort.Strings(rels)
	want := []string{
		"NES/Mega Game (Europe).nes=nes", "NES/Mega Game (USA).nes=nes", "NES/Zipped Game (USA).zip=nes",
		"PS1/Big Game/Big Game (USA).m3u=ps1", "PS1/Cue Game (USA)/Cue Game (USA).cue=ps1", "PS1/Two Discs (Europe) (Disc 1).cue=ps1",
	}
	if strings.Join(rels, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", rels, want)
	}
	if it := byRel["PS1/Cue Game (USA)/Cue Game (USA).cue"]; it.Title != "Cue Game" || it.Tagline != "USA" || it.Kind != "game" || it.Container != "cue" || len(it.Files) != 1 {
		t.Errorf("a sheet and its tracks: %+v", it)
	}
	if it := byRel["PS1/Two Discs (Europe) (Disc 1).cue"]; len(it.Files) != 2 || !strings.HasSuffix(it.Files[1], "(Disc 2).cue") || it.Title != "Two Discs" {
		t.Errorf("a game on two discs: %+v", it)
	}
	if it := byRel["NES/Zipped Game (USA).zip"]; it.Title != "Zipped Game" || it.Tagline != "USA" || it.Container != "zip" {
		t.Errorf("a zip: %+v", it)
	}
	usa, eur := byRel["NES/Mega Game (USA).nes"], byRel["NES/Mega Game (Europe).nes"]
	if usa.VersionOf != "" || eur.VersionOf != usa.Files[0] || eur.VersionLabel != "Europe" || usa.SortTitle != "Mega Game" {
		t.Errorf("versions: %+v %+v", usa, eur)
	}
	if !strings.HasPrefix(out.Result, "Found 5 games in ") {
		t.Errorf("result: %q", out.Result)
	}

	// The server lists what was answered, with ids, and asks about the new ones.
	var ids []wittymodule.ScanDescribeItem
	for i, it := range out.Items {
		id := int64(100 + i)
		files := make([]service.File, len(it.Files))
		for j, f := range it.Files {
			files[j] = service.File{Path: f}
		}
		gh.items[id] = service.Item{ID: id, Library: lib.ID, Kind: "game", Title: it.Title, Facets: it.Facets, Files: files}
		ids = append(ids, wittymodule.ScanDescribeItem{ID: id, Kind: "game", Title: it.Title, Files: it.Files, Facets: it.Facets})
	}
	if _, err := hooks.Describe(context.Background(), wittymodule.ScanDescribeArgs{Library: lib, Items: ids}); err != nil {
		t.Fatal(err)
	}
	idOf := func(rel string) int64 {
		for id, it := range gh.items {
			if it.Files[0].Path == filepath.Join(root, filepath.FromSlash(rel)) {
				return id
			}
		}
		t.Fatalf("no item for %s", rel)
		return 0
	}
	const person = 2
	asAdmin := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(func() string { b, _ := json.Marshal(body); return string(b) }()))
		r.Header.Set(service.HeaderUser, "1")
		r.Header.Set(service.HeaderAdmin, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// The versions of a game, from either of them.
	var vs struct {
		Versions []gameVersion `json:"versions"`
		Discs    []struct {
			Disc int    `json:"disc"`
			Name string `json:"name"`
		} `json:"discs"`
	}
	w := call(t, h, "GET", "/games/"+strconv.FormatInt(idOf("NES/Mega Game (Europe).nes"), 10)+"/versions", person, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &vs) != nil || len(vs.Versions) != 2 || vs.Versions[0].ID != idOf("NES/Mega Game (USA).nes") || !vs.Versions[1].Current || vs.Versions[1].Label != "Europe" {
		t.Fatalf("versions: %d %s", w.Code, w.Body)
	}
	w = call(t, h, "GET", "/games/"+strconv.FormatInt(idOf("PS1/Two Discs (Europe) (Disc 1).cue"), 10)+"/versions", person, nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &vs) != nil || len(vs.Discs) != 2 || vs.Discs[1].Disc != 2 {
		t.Fatalf("discs: %d %s", w.Code, w.Body)
	}
	// Only an administrator puts a library right.
	if w := call(t, h, "GET", "/libraries/1/games/review", person, nil); w.Code != 403 {
		t.Fatalf("a person who isn't an administrator: %d", w.Code)
	}
	var review struct {
		Items []struct {
			ID   int64  `json:"id"`
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"items"`
		Counts map[string]int `json:"counts"`
	}
	if w := asAdmin("GET", "/libraries/1/games/review", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil {
		t.Fatalf("review: %d %s", w.Code, w.Body)
	}
	if w := asAdmin("GET", "/libraries/2/games/review", nil); w.Code != 404 {
		t.Fatalf("a library that isn't there: %d", w.Code)
	}
	// Splitting a version off is kept, and the server is asked to scan.
	if w := asAdmin("POST", "/games/"+strconv.FormatInt(idOf("NES/Mega Game (Europe).nes"), 10)+"/split", nil); w.Code != 200 || gh.rescans.Load() != 1 {
		t.Fatalf("split: %d %s, %d scans", w.Code, w.Body, gh.rescans.Load())
	}
	// Not a game: it goes to the list of what's left out.
	if w := asAdmin("POST", "/games/"+strconv.FormatInt(idOf("NES/Zipped Game (USA).zip"), 10)+"/not-a-game", nil); w.Code != 200 {
		t.Fatalf("not a game: %d %s", w.Code, w.Body)
	}
	if w := asAdmin("PUT", "/games/"+strconv.FormatInt(idOf("NES/Mega Game (USA).nes"), 10)+"/console", map[string]string{"console": "nope"}); w.Code != 400 {
		t.Fatalf("no such console: %d", w.Code)
	}
	again := identify(t, hooks, "second", lib)
	for _, it := range again.Items {
		if it.VersionOf != "" {
			t.Errorf("a split came back: %+v", it)
		}
		if strings.HasSuffix(it.Files[0], ".zip") {
			t.Errorf("not a game, and listed: %+v", it)
		}
	}
	if w := asAdmin("GET", "/libraries/1/games/review?kind=ignored", nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &review) != nil || len(review.Items) != 1 || review.Items[0].Path != "NES/Zipped Game (USA).zip" {
		t.Fatalf("ignored: %d %s", w.Code, w.Body)
	}
	// Brought back from the list, it's a game again.
	if w := asAdmin("POST", "/libraries/1/games/review/apply", map[string]any{"ids": []int64{review.Items[0].ID}, "action": "forget"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":1`) {
		t.Fatalf("apply: %d %s", w.Code, w.Body)
	}
	if n := len(identify(t, hooks, "third", lib).Items); n != 6 {
		t.Fatalf("after forgetting: %d items", n)
	}
	// The import page: how it's going, in one answer.
	if w := asAdmin("GET", "/libraries/1/games/import", nil); w.Code == 404 || w.Code == 403 {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
}

// A last call for a scan WittyPlay never heard the files of (it restarted) is refused, rather
// than answered with a library holding nothing.
func TestIdentifyRefusesAScanItLost(t *testing.T) {
	root := gameTree(t)
	lib := wittymodule.ScanLibrary{ID: 1, Kind: "games", Facet: "auto", Paths: []string{root}}
	testApp(t, (&gamesHost{lib: lib}).client(t))
	hooks := ScanHooks()
	if _, err := hooks.Identify(context.Background(), wittymodule.ScanIdentifyArgs{Scan: "lost", Library: lib, Total: 12, Done: true}); err == nil {
		t.Fatal("answered a scan it never saw the files of")
	}
	// An empty library is an empty library.
	if out, err := hooks.Identify(context.Background(), wittymodule.ScanIdentifyArgs{Scan: "empty", Library: wittymodule.ScanLibrary{ID: 2, Kind: "games", Facet: "snes", Paths: []string{t.TempDir()}}, Done: true}); err != nil || len(out.Items) != 0 {
		t.Fatalf("empty: %+v %v", out, err)
	}
	if _, err := hooks.Identify(context.Background(), wittymodule.ScanIdentifyArgs{Scan: "x", Library: wittymodule.ScanLibrary{ID: 3, Kind: "movies"}, Done: true}); err == nil {
		t.Fatal("a library of films isn't WittyPlay's to scan")
	}
}
