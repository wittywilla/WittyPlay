// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamelogos

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"database/sql"

	_ "modernc.org/sqlite"
)

func logoDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+t.TempDir()+"/logos.db")
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE wittyplay_logos (
  id INTEGER PRIMARY KEY, kind TEXT NOT NULL, key TEXT NOT NULL, name TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending', source TEXT NOT NULL DEFAULT '', source_id TEXT NOT NULL DEFAULT '',
  source_url TEXT NOT NULL DEFAULT '', license TEXT NOT NULL DEFAULT '', license_url TEXT NOT NULL DEFAULT '',
  credit TEXT NOT NULL DEFAULT '', file TEXT NOT NULL DEFAULT '', opaque INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL DEFAULT 0, tried_at INTEGER NOT NULL DEFAULT 0, UNIQUE(kind, key))`); err != nil {
		t.Fatal(err)
	}
	return d
}

// logoService is a logo service whose background work, which LookupLogos starts, has ended
// before its store closes: on Windows a database still open can't be deleted with the test's
// folder.
func logoService(t *testing.T, l *Logos) *Logos {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(2 * time.Minute)
		for {
			l.mu.Lock()
			running := l.running
			l.mu.Unlock()
			if !running {
				return
			}
			if time.Now().After(deadline) {
				t.Error("the logo service was still running when the test ended")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return l
}

func pngOf(t *testing.T, m image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A logo on a clear background keeps its shape, painted one colour.
func TestLogoVariantsFromTransparency(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 5; y < 15; y++ {
		for x := 10; x < 30; x++ {
			m.SetNRGBA(x, y, color.NRGBA{200, 20, 20, 255})
		}
	}
	set, err := makeLogoVariants(pngOf(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if set.opaque {
		t.Fatal("a transparent picture was treated as opaque")
	}
	w, err := png.Decode(bytes.NewReader(set.white))
	if err != nil {
		t.Fatal(err)
	}
	if b := w.Bounds(); b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("the empty margin wasn't trimmed: %v", b)
	}
	if r, g, bl, a := w.At(5, 5).RGBA(); a == 0 || r>>8 != 255 || g>>8 != 255 || bl>>8 != 255 {
		t.Errorf("the white version isn't white: %d %d %d %d", r, g, bl, a)
	}
	d, _ := png.Decode(bytes.NewReader(set.dark))
	if r, _, _, a := d.At(5, 5).RGBA(); a == 0 || r>>8 > 40 {
		t.Errorf("the dark version isn't dark: %d %d", r, a)
	}
}

// Black letters on a white rectangle are cut out of the rectangle.
func TestLogoVariantsFromPlainBackground(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.NRGBA{255, 255, 255, 255}
			if x >= 10 && x < 30 && y >= 5 && y < 15 {
				c = color.NRGBA{0, 0, 0, 255}
			}
			m.SetNRGBA(x, y, c)
		}
	}
	set, err := makeLogoVariants(pngOf(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if set.opaque {
		t.Fatal("a plain background should come off")
	}
	w, _ := png.Decode(bytes.NewReader(set.white))
	if b := w.Bounds(); b.Dx() != 20 || b.Dy() != 10 {
		t.Errorf("wanted only the letters, got %v", b)
	}
}

// A photo has no background to take off: it stays whole.
func TestLogoVariantsOpaquePhoto(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			m.SetNRGBA(x, y, color.NRGBA{uint8(x * 6), uint8(y * 6), uint8(x * y % 255), 255})
		}
	}
	set, err := makeLogoVariants(pngOf(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if !set.opaque || !bytes.Equal(set.white, set.original) {
		t.Fatal("a picture that can't be cut out should stay as it is")
	}
}

// Whatever comes in, only pixels come out; anything that isn't a picture is refused.
func TestLogoRefusesWhatIsNotAPicture(t *testing.T) {
	for name, b := range map[string][]byte{
		"svg":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":  []byte(`<html><script>alert(1)</script></html>`),
		"empty": {},
		"huge":  bytes.Repeat([]byte{0}, maxLogoBytes+1),
	} {
		if _, err := makeLogoVariants(b); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// Trailing data on a real picture doesn't survive being written again.
	m := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	m.SetNRGBA(3, 3, color.NRGBA{9, 9, 9, 255})
	evil := append(pngOf(t, m), []byte("<script>alert(1)</script>")...)
	set, err := makeLogoVariants(evil)
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range [][]byte{set.original, set.white, set.dark} {
		if bytes.Contains(out, []byte("script")) {
			t.Fatal("trailing data survived")
		}
	}
}

func TestFreeLogoLicense(t *testing.T) {
	for l, want := range map[string]bool{
		"Public domain": true, "PD-textlogo": true, "CC0": true, "CC BY 4.0": true, "CC BY-SA 3.0": true, "CC-BY-SA-4.0": true,
		"CC BY-NC 4.0": false, "CC BY-ND 4.0": false, "CC BY-NC-SA 4.0": false, "Fair use": false, "Non-free logo": false, "": false, "All rights reserved": false,
	} {
		if FreeLogoLicense(l) != want {
			t.Errorf("licence %q: want %v", l, want)
		}
	}
}

func TestBrandKeyTreatsSpellingsAlike(t *testing.T) {
	if a, b := BrandKey("Warner Bros."), BrandKey("warner  bros"); a != b {
		t.Errorf("%q and %q", a, b)
	}
	if a, b := BrandKey("Acme, Inc."), BrandKey("ACME"); a != b {
		t.Errorf("%q and %q", a, b)
	}
	if BrandKey("  --  ") != "" {
		t.Error("punctuation alone isn't a name")
	}
}

// fakeWikimedia answers as Wikidata and Commons do.
func fakeWikimedia(t *testing.T, license string) (*WikimediaLogos, *int) {
	t.Helper()
	calls := 0
	m := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 5; y < 15; y++ {
		for x := 10; x < 30; x++ {
			m.SetNRGBA(x, y, color.NRGBA{10, 10, 10, 255})
		}
	}
	pic := pngOf(t, m)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/pic.png":
			w.Write(pic)
		case q.Get("action") == "wbsearchentities":
			json.NewEncoder(w).Encode(map[string]any{"search": []map[string]string{{"id": "Q1"}, {"id": "Q2"}}})
		case q.Get("action") == "wbgetentities":
			json.NewEncoder(w).Encode(map[string]any{"entities": map[string]any{
				"Q1": map[string]any{"labels": map[string]any{"en": map[string]string{"value": "Acme Studio"}}, "descriptions": map[string]any{"en": map[string]string{"value": "home video game console"}},
					"claims": map[string]any{"P154": []map[string]any{{"rank": "normal", "mainsnak": map[string]any{"datavalue": map[string]any{"value": "Acme logo.svg"}}}}}},
				// A film with the same name: the right word is missing from what it is.
				"Q2": map[string]any{"labels": map[string]any{"en": map[string]string{"value": "Acme Studio"}}, "descriptions": map[string]any{"en": map[string]string{"value": "1999 album"}},
					"claims": map[string]any{"P154": []map[string]any{{"rank": "normal", "mainsnak": map[string]any{"datavalue": map[string]any{"value": "Album cover.png"}}}}}},
			}})
		case q.Get("action") == "query":
			json.NewEncoder(w).Encode(map[string]any{"query": map[string]any{"pages": map[string]any{"1": map[string]any{"title": "File:Acme logo.svg", "imageinfo": []map[string]any{{
				"thumburl": "https://upload.wikimedia.org/acme.png", "descriptionurl": "https://commons.wikimedia.org/wiki/File:Acme_logo.svg",
				"extmetadata": map[string]any{"LicenseShortName": map[string]string{"value": license}, "Artist": map[string]string{"value": "<a href='x'>Some Author</a>"}},
			}}}}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return &WikimediaLogos{Client: &http.Client{Transport: rewriteTo{srv.URL}}, WikidataAPI: srv.URL + "/wd", CommonsAPI: srv.URL + "/c"}, &calls
}

// rewriteTo sends a picture's address to the fake server.
type rewriteTo struct{ base string }

func (rt rewriteTo) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Host, "wikimedia") {
		r2 := r.Clone(r.Context())
		u, _ := r.URL.Parse(rt.base + "/pic.png")
		r2.URL = u
		r2.Host = ""
		return http.DefaultTransport.RoundTrip(r2)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestBestLogoFile(t *testing.T) {
	claim := func(rank, file, language string) logoClaim {
		var c logoClaim
		c.Rank = rank
		c.Mainsnak.Datavalue.Value = json.RawMessage(strconv.Quote(file))
		if language != "" {
			if err := json.Unmarshal([]byte(`{"P407":[{"datavalue":{"value":{"id":"`+language+`"}}}]}`), &c.Qualifiers); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	const japanese = "Q5287"
	for _, c := range []struct {
		name   string
		claims []logoClaim
		want   string
	}{
		// As Wikidata has them for the NES: the Japanese console's logo is listed last.
		{"English over another language", []logoClaim{claim("normal", "NES logo.svg", wdEnglish), claim("normal", "Family Computer logo.svg", japanese)}, "NES logo.svg"},
		{"no language over another language", []logoClaim{claim("normal", "Plain.svg", ""), claim("normal", "Super Famicom.svg", japanese)}, "Plain.svg"},
		{"preferred over everything", []logoClaim{claim("preferred", "SNES logo.svg", ""), claim("normal", "English.svg", wdEnglish)}, "SNES logo.svg"},
		{"the last among equals", []logoClaim{claim("normal", "Old.svg", ""), claim("normal", "New.svg", "")}, "New.svg"},
		{"another language beats nothing", []logoClaim{claim("normal", "Only.svg", japanese)}, "Only.svg"},
		{"deprecated is never used", []logoClaim{claim("deprecated", "Wrong.svg", "")}, ""},
	} {
		if got := bestLogoFile(c.claims); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// An entity's claims come all together, and other properties' qualifiers hold strings. One of
// those must not stop its logos being read.
func TestLogoClaimsReadBesideOtherProperties(t *testing.T) {
	var claims map[string][]logoClaim
	err := json.Unmarshal([]byte(`{
		"P3984": [{"rank":"normal","mainsnak":{"datavalue":{"value":"a name"}},"qualifiers":{"P1810":[{"datavalue":{"value":"named as"}}]}}],
		"P625": [{"rank":"normal","mainsnak":{"datavalue":{"value":{"latitude":1}}},"qualifiers":{"P585":[{"datavalue":{"value":{"time":"+2020"}}}]}}],
		"P154": [
			{"rank":"normal","mainsnak":{"datavalue":{"value":"NES logo.svg"}},"qualifiers":{"P407":[{"datavalue":{"value":{"id":"Q1860"}}}],"P1810":[{"datavalue":{"value":"NES"}}]}},
			{"rank":"normal","mainsnak":{"datavalue":{"value":"Family Computer logo.svg"}},"qualifiers":{"P407":[{"datavalue":{"value":{"id":"Q5287"}}}]}}
		]}`), &claims)
	if err != nil {
		t.Fatal(err)
	}
	if got := bestLogoFile(claims[wdLogoImage]); got != "NES logo.svg" {
		t.Errorf("got %q", got)
	}
}

func TestLogoServiceAsksNobodyWithoutConsent(t *testing.T) {
	src, calls := fakeWikimedia(t, "PD-textlogo")
	l := logoService(t, &Logos{DB: logoDB(t), Dir: t.TempDir(), Source: src, Gap: 1, Allowed: func(context.Context) bool { return false }})
	ctx := context.Background()
	if _, err := l.EnsureLogo(ctx, LogoConsole, "Acme Studio"); err != nil {
		t.Fatal(err)
	}
	l.work(ctx)
	if *calls != 0 {
		t.Fatalf("asked Wikimedia %d times without the administrator agreeing", *calls)
	}
}

func TestLogoServiceDownloadsAConsoleLogoAfterConsent(t *testing.T) {
	src, _ := fakeWikimedia(t, "PD-textlogo")
	dir := t.TempDir()
	l := logoService(t, &Logos{DB: logoDB(t), Dir: dir, Source: src, Gap: 1, Allowed: func(context.Context) bool { return true }})
	ctx := context.Background()
	if _, err := l.EnsureLogo(ctx, LogoConsole, "Acme Studio"); err != nil {
		t.Fatal(err)
	}
	studio, _ := l.EnsureLogo(ctx, LogoGameStudio, "Acme Games")
	l.work(ctx)
	g, err := l.LogoFor(ctx, LogoConsole, "acme studio")
	if err != nil || g == nil || !g.Has || g.Source != "wikimedia" || g.License != "PD-textlogo" || g.Credit != "Some Author" {
		t.Fatalf("logo: %+v %v", g, err)
	}
	for _, v := range []string{"original", "white", "dark"} {
		if _, err := os.Stat(l.ImagePath(g, v)); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
	// A game studio's is only looked for when an administrator chooses one.
	if s, _ := l.LogoByID(ctx, studio.ID); s.Status != LogoPending {
		t.Errorf("a game studio's logo was looked for by itself: %+v", s)
	}
	if n, err := l.ForgetDownloadedKind(ctx, LogoConsole); err != nil || n != 1 {
		t.Fatalf("forget: %d %v", n, err)
	}
	if _, err := os.Stat(l.ImagePath(g, "original")); err == nil {
		t.Error("the file stayed")
	}
}
