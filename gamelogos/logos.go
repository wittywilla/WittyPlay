// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package gamelogos is WittyPlay's logo service (moved from the server's logo service, its game
// parts): consoles' logos, for WittyPlay's cartridges, cases and console marks, and game studios'.
// Logos are never bundled. A logo is downloaded only after an administrator agreed (WittyPlay
// Settings › Game pictures), from Wikimedia Commons, whose files say what may be done with them.
// Either way it is decoded and written again as a plain PNG, kept in WittyPlay's own folder, and
// served only by WittyPlay, so a browser or an app never talks to the source. Console photos for
// the Consoles tab come from the same place (console_photos.go).
package gamelogos

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"  // what an upload or a source may be
	_ "image/jpeg" // what an upload or a source may be
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // what an upload or a source may be
)

// The kinds of thing that can have a logo.
const (
	LogoConsole    = "console"
	LogoGameStudio = "gamestudio"
)

// What's known about a logo.
const (
	LogoPending = "pending" // not looked for yet
	LogoFound   = "found"   // there is a file
	LogoNone    = "none"    // looked, and no source has one
	LogoText    = "text"    // someone chose the name alone
)

// ErrNotFound: no such logo, or none to copy.
var ErrNotFound = errors.New("not found")

// Logo is one row of the service. Has says there's a picture to show; anything else
// is shown as Name.
type Logo struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Has     bool   `json:"has"`
	Version int    `json:"version"`
	// Opaque: the picture has a background of its own that couldn't be taken off,
	// so all three variants are the same picture, to be shown on its own plate.
	Opaque     bool   `json:"opaque"`
	Source     string `json:"source,omitempty"`
	SourceURL  string `json:"sourceUrl,omitempty"`
	License    string `json:"license,omitempty"`
	LicenseURL string `json:"licenseUrl,omitempty"`
	Credit     string `json:"credit,omitempty"`
	file       string
}

// LogoCandidate is a picture a source offers for something.
type LogoCandidate struct {
	Source     string `json:"source"`
	SourceID   string `json:"sourceId"`
	Name       string `json:"name"`
	Note       string `json:"note,omitempty"`
	ImageURL   string `json:"imageUrl"`
	PageURL    string `json:"pageUrl"`
	License    string `json:"license"`
	LicenseURL string `json:"licenseUrl,omitempty"`
	Credit     string `json:"credit,omitempty"`
	Exact      bool   `json:"exact"`
}

// LogoSource finds logos online.
type LogoSource interface {
	Find(ctx context.Context, kind, name string) ([]LogoCandidate, error)
	Fetch(ctx context.Context, c LogoCandidate) ([]byte, error)
}

// Logos runs the service.
type Logos struct {
	DB  *sql.DB
	Dir string // where the pictures are kept
	// Source is where downloads come from. Nil means nowhere.
	Source LogoSource
	Log    *slog.Logger
	// Allowed says whether an administrator has agreed to downloads (WittyPlay's brand logos).
	Allowed func(ctx context.Context) bool
	// Report tells the server's activity panel how the looking goes; may be nil.
	Report func(done int, err error)
	// Gap between lookups, so a first run asks gently.
	Gap time.Duration

	mu      sync.Mutex
	running bool
}

// ValidLogoKind reports whether kind is one the service knows.
func ValidLogoKind(kind string) bool {
	switch kind {
	case LogoConsole, LogoGameStudio:
		return true
	}
	return false
}

var logoNoise = regexp.MustCompile(`[^\p{L}\p{N}]+`)
var logoSuffix = regexp.MustCompile(`\s+(inc|llc|ltd|limited|corp|corporation|co|gmbh|sa|plc)$`)

// BrandKey is how two spellings of a name are told to be the same.
func BrandKey(name string) string {
	k := strings.TrimSpace(logoNoise.ReplaceAllString(strings.ToLower(name), " "))
	return logoSuffix.ReplaceAllString(k, "")
}

// cleanBrandName trims and bounds a name that came from a source or a person.
func cleanBrandName(name string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name))
	if rs := []rune(name); len(rs) > 120 {
		name = string(rs[:120])
	}
	return name
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

const logoCols = `id, kind, name, status, source, source_url, license, license_url, credit, file, opaque, version`

func scanLogo(sc interface{ Scan(...any) error }) (*Logo, error) {
	var g Logo
	var opaque int
	if err := sc.Scan(&g.ID, &g.Kind, &g.Name, &g.Status, &g.Source, &g.SourceURL, &g.License, &g.LicenseURL, &g.Credit, &g.file, &opaque, &g.Version); err != nil {
		return nil, err
	}
	g.Opaque = opaque != 0
	g.Has = g.Status == LogoFound && g.file != ""
	return &g, nil
}

// LogoByID reads one row.
func (l *Logos) LogoByID(ctx context.Context, id int64) (*Logo, error) {
	g, err := scanLogo(l.DB.QueryRowContext(ctx, `SELECT `+logoCols+` FROM wittyplay_logos WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return g, err
}

// LogoFor reads the row for a name, or nil if nobody has asked about it.
func (l *Logos) LogoFor(ctx context.Context, kind, name string) (*Logo, error) {
	g, err := scanLogo(l.DB.QueryRowContext(ctx, `SELECT `+logoCols+` FROM wittyplay_logos WHERE kind = ? AND key = ?`, kind, BrandKey(name)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

// EnsureLogo makes sure a name has a row, so the service looks for it. This is
// what other parts of the server call when they find a console or a game studio.
// It does nothing for an empty or unknown name, and contacts nobody by itself.
func (l *Logos) EnsureLogo(ctx context.Context, kind, name string) (*Logo, error) {
	name = cleanBrandName(name)
	key := BrandKey(name)
	if !ValidLogoKind(kind) || key == "" {
		return nil, errors.New("not a name")
	}
	if _, err := l.DB.ExecContext(ctx, `INSERT INTO wittyplay_logos(kind, key, name) VALUES(?,?,?) ON CONFLICT(kind, key) DO NOTHING`, kind, key, name); err != nil {
		return nil, err
	}
	return l.LogoFor(ctx, kind, name)
}

// ---------------------------------------------------------------------------
// Looking for logos
// ---------------------------------------------------------------------------

// Kick starts looking for what's pending, if the owner agreed. It returns at once.
func (l *Logos) Kick() {
	if l == nil || l.Source == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return
	}
	l.running = true
	go func() {
		defer func() {
			if p := recover(); p != nil {
				l.log().Error("logo service", "err", fmt.Sprint(p))
			}
			l.mu.Lock()
			l.running = false
			l.mu.Unlock()
		}()
		l.work(context.Background())
	}()
}

func (l *Logos) work(ctx context.Context) {
	gap := l.Gap
	if gap == 0 {
		gap = time.Second
	}
	done, started := 0, false // reported only once there's something to do: this wakes after every scan
	report := func(err error) {
		if l.Report != nil && started {
			l.Report(done, err)
		}
	}
	for {
		// Consoles' logos are looked for by themselves; a game studio's only when an
		// administrator chooses one.
		if l.Allowed == nil || !l.Allowed(ctx) {
			report(nil)
			return
		}
		g, err := scanLogo(l.DB.QueryRowContext(ctx, `SELECT `+logoCols+` FROM wittyplay_logos WHERE status = ? AND kind = ? ORDER BY id LIMIT 1`, LogoPending, LogoConsole))
		if err != nil {
			report(nil)
			return
		}
		started = true
		if err := l.lookFor(ctx, g); err != nil {
			l.log().Warn("could not look for a logo", "kind", g.Kind, "name", g.Name, "err", err)
			// The source may be away. Stop here: the next scan or lookup starts again.
			_, _ = l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET tried_at = ? WHERE id = ?`, time.Now().Unix(), g.ID)
			report(err)
			return
		}
		done++
		report(nil)
		time.Sleep(gap)
	}
}

func (l *Logos) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.Default().With("cat", "games")
}

func (l *Logos) lookFor(ctx context.Context, g *Logo) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cands, err := l.Source.Find(ctx, g.Kind, g.Name)
	if err != nil {
		return err
	}
	for _, c := range cands {
		if !c.Exact {
			continue // close isn't good enough to put on a title by itself: Change offers these
		}
		if err := l.use(ctx, g, c); err != nil {
			l.log().Debug("a logo couldn't be used", "name", g.Name, "file", c.SourceID, "err", err)
			continue
		}
		return nil
	}
	_, err = l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET status = ?, tried_at = ? WHERE id = ?`, LogoNone, time.Now().Unix(), g.ID)
	return err
}

// Candidates lists what a source has for a logo, for someone choosing by hand.
func (l *Logos) Candidates(ctx context.Context, g *Logo, query string) ([]LogoCandidate, error) {
	if !l.SearchAllowed(ctx, g.Kind) {
		return nil, ErrLogosNotAgreed
	}
	if l.Source == nil {
		return []LogoCandidate{}, nil
	}
	if query = cleanBrandName(query); query == "" {
		query = g.Name
	}
	c, err := l.Source.Find(ctx, g.Kind, query)
	if c == nil {
		c = []LogoCandidate{}
	}
	return c, err
}

// BrandLogoKinds are the kinds WittyPlay's agreement (ConsoleLogosOn) covers: a console's logo, which
// it downloads by itself, and a game studio's, which an administrator may search for and choose.
var BrandLogoKinds = []string{LogoConsole, LogoGameStudio}

// SearchAllowed says an administrator may search a source for this kind of logo: under the app-wide
// agreement, or WittyPlay's for its brand kinds.
func (l *Logos) SearchAllowed(ctx context.Context, kind string) bool {
	return l.Allowed != nil && l.Allowed(ctx) && (kind == LogoConsole || kind == LogoGameStudio)
}

// Downloaded lists the logos of one kind already downloaded here (not uploaded), to reuse for
// another name without asking a source again: a console's logo for its other name, say.
func (l *Logos) Downloaded(ctx context.Context, kind string, except int64) ([]Logo, error) {
	rows, err := l.DB.QueryContext(ctx, `SELECT `+logoCols+` FROM wittyplay_logos WHERE kind = ? AND status = ? AND source NOT IN ('you', '') AND id <> ? ORDER BY name COLLATE NOCASE LIMIT 200`, kind, LogoFound, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Logo{}
	for rows.Next() {
		g, err := scanLogo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// CopyFrom makes another downloaded logo of the same kind this one too, with its source, licence and
// credit, so the credits still say where it came from.
func (l *Logos) CopyFrom(ctx context.Context, g *Logo, fromID int64) (*Logo, error) {
	if !l.SearchAllowed(ctx, g.Kind) {
		return nil, ErrLogosNotAgreed
	}
	from, err := l.LogoByID(ctx, fromID)
	if err != nil || from == nil || from.ID == g.ID || from.Kind != g.Kind || !from.Has || from.Source == "you" || from.Source == "" {
		return nil, ErrNotFound
	}
	b, err := os.ReadFile(l.pathFor(from.file, "original"))
	if err != nil {
		return nil, ErrNotFound
	}
	c := LogoCandidate{Source: from.Source, SourceID: "copy:" + strconv.FormatInt(from.ID, 10), PageURL: from.SourceURL, License: from.License, LicenseURL: from.LicenseURL, Credit: from.Credit}
	if err := l.store(ctx, g, b, c); err != nil {
		return nil, err
	}
	return l.LogoByID(ctx, g.ID)
}

// ErrLogosNotAgreed: downloading logos needs the owner's agreement first.
var ErrLogosNotAgreed = errors.New("downloading logos isn't turned on. Turn it on in WittyPlay's settings, Game pictures, after reading what it means")

// Pick downloads a candidate and makes it this logo. The candidate comes from
// the browser, so only a fresh answer from the source is believed: a page must not
// be able to make the server fetch an address of its choosing, or claim a licence.
func (l *Logos) Pick(ctx context.Context, g *Logo, query, sourceID string) (*Logo, error) {
	cands, err := l.Candidates(ctx, g, query)
	if err != nil {
		return nil, err
	}
	for _, c := range cands {
		if c.SourceID == sourceID {
			if err := l.use(ctx, g, c); err != nil {
				return nil, err
			}
			return l.LogoByID(ctx, g.ID)
		}
	}
	return nil, ErrNotFound
}

func (l *Logos) use(ctx context.Context, g *Logo, c LogoCandidate) error {
	b, err := l.Source.Fetch(ctx, c)
	if err != nil {
		return err
	}
	return l.store(ctx, g, b, c)
}

// Upload makes a picture from the owner this logo.
func (l *Logos) Upload(ctx context.Context, g *Logo, b []byte) (*Logo, error) {
	if err := l.store(ctx, g, b, LogoCandidate{Source: "you", Credit: "Uploaded by this server's owner"}); err != nil {
		return nil, err
	}
	return l.LogoByID(ctx, g.ID)
}

// SetLogoStatus handles "use the name alone" and "look again".
func (l *Logos) SetLogoStatus(ctx context.Context, g *Logo, status string) (*Logo, error) {
	if status != LogoText && status != LogoPending {
		return nil, errors.New("status must be text or pending")
	}
	l.removeFiles(g.file)
	if _, err := l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET status = ?, file = '', source = '', source_id = '', source_url = '', license = '', license_url = '', credit = '', opaque = 0, tried_at = 0, version = version + 1 WHERE id = ?`, status, g.ID); err != nil {
		return nil, err
	}
	if status == LogoPending {
		l.Kick()
	}
	return l.LogoByID(ctx, g.ID)
}

func (l *Logos) store(ctx context.Context, g *Logo, b []byte, c LogoCandidate) error {
	set, err := makeLogoVariants(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return err
	}
	version := g.Version + 1
	file := fmt.Sprintf("%d-%d", g.ID, version) // the name is ours alone: nothing from a source is in it
	for v, data := range map[string][]byte{"original": set.original, "white": set.white, "dark": set.dark} {
		tmp, err := os.CreateTemp(l.Dir, "tmp-*")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(data)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), l.pathFor(file, v))
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return werr
		}
	}
	old := g.file
	opaque := 0
	if set.opaque {
		opaque = 1
	}
	if _, err := l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET status = ?, source = ?, source_id = ?, source_url = ?, license = ?, license_url = ?, credit = ?, file = ?, opaque = ?, version = ?, tried_at = ? WHERE id = ?`,
		LogoFound, c.Source, c.SourceID, c.PageURL, c.License, c.LicenseURL, c.Credit, file, opaque, version, time.Now().Unix(), g.ID); err != nil {
		return err
	}
	l.removeFiles(old)
	g.Version, g.file = version, file
	return nil
}

func (l *Logos) pathFor(file, variant string) string {
	return filepath.Join(l.Dir, file+"-"+variant+".png")
}

func (l *Logos) removeFiles(file string) {
	if file == "" {
		return
	}
	for _, v := range []string{"original", "white", "dark"} {
		_ = os.Remove(l.pathFor(file, v))
	}
}

// ImagePath is where a variant of a logo is, or "".
func (l *Logos) ImagePath(g *Logo, variant string) string {
	if !g.Has {
		return ""
	}
	switch variant {
	case "white", "dark", "original":
	default:
		variant = "original"
	}
	return l.pathFor(g.file, variant)
}

// ---------------------------------------------------------------------------
// Making a picture safe, and white and dark versions of it
// ---------------------------------------------------------------------------

const (
	maxLogoBytes  = 8 << 20
	maxLogoPixels = 25_000_000
	logoWidth     = 640
)

type logoVariants struct {
	original, white, dark []byte
	opaque                bool
}

// makeLogoVariants reads a picture from anywhere and writes it again from its pixels
// as PNG, so nothing but pixels survives: no metadata, no trailing data, no script.
func makeLogoVariants(b []byte) (set *logoVariants, err error) {
	defer func() {
		if p := recover(); p != nil { // image decoders see whatever was downloaded
			set, err = nil, fmt.Errorf("could not read this picture (%v)", p)
		}
	}()
	if len(b) > maxLogoBytes {
		return nil, errors.New("the picture is too big: the limit is 8 MB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("use a PNG, JPEG, WebP or GIF picture")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxLogoPixels {
		return nil, errors.New("the picture is too big")
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("this picture couldn't be read")
	}
	full := toNRGBA(src)
	matte, opaque := logoMatte(full)
	if !opaque {
		// Trim the empty margin, so logos line up on a card by what can be seen.
		if box := alphaBounds(matte); !box.Empty() {
			full = cropNRGBA(full, box)
			matte = cropAlpha(matte, box)
		}
	}
	full, matte = fitLogo(full, matte)
	white, dark := full, full
	if !opaque {
		white = paintMatte(matte, color.NRGBA{255, 255, 255, 255})
		dark = paintMatte(matte, color.NRGBA{16, 18, 24, 255})
	}
	out := &logoVariants{opaque: opaque}
	if out.original, err = encodePNG(full); err != nil {
		return nil, err
	}
	if out.white, err = encodePNG(white); err != nil {
		return nil, err
	}
	if out.dark, err = encodePNG(dark); err != nil {
		return nil, err
	}
	return out, nil
}

func encodePNG(m image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, m); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func toNRGBA(src image.Image) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetNRGBA(x, y, color.NRGBAModel.Convert(src.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA))
		}
	}
	return out
}

func lum(c color.NRGBA) float64 {
	return (0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)) / 255
}

// logoMatte works out which pixels are the logo, for painting it in one colour.
//
//   - A transparent background goes by its transparency. If what's left is in two
//     clearly different tones (a dark shield with light letters), the larger tone
//     becomes the logo and the smaller is cut out of it, so the letters stay readable
//     once everything is one colour.
//   - A plain background (all four corners alike) goes by how far each pixel is from it.
//   - Anything else, such as a photo, can't be cut out: opaque is true and the
//     picture stays as it is, to sit on a plate of its own.
func logoMatte(m *image.NRGBA) (matte *image.Alpha, opaque bool) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	matte = image.NewAlpha(b)
	see := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if m.NRGBAAt(x, y).A < 250 {
				see++
			}
		}
	}
	if see*50 >= w*h { // at least 2% can be seen through
		var dark, light int
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if c := m.NRGBAAt(x, y); c.A > 128 {
					if lum(c) < 0.5 {
						dark++
					} else {
						light++
					}
				}
			}
		}
		solid := dark + light
		twoTone := solid > 0 && min(dark, light)*100 >= solid*8
		major, minor := 0.0, 1.0
		if light > dark {
			major, minor = 1, 0
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				c := m.NRGBAAt(x, y)
				a := float64(c.A)
				if twoTone {
					d := (lum(c) - minor) / (major - minor)
					a *= clamp01(d*1.6 - 0.1)
				}
				matte.SetAlpha(x, y, color.Alpha{uint8(a)})
			}
		}
		return matte, false
	}
	corners := []color.NRGBA{m.NRGBAAt(0, 0), m.NRGBAAt(w-1, 0), m.NRGBAAt(0, h-1), m.NRGBAAt(w-1, h-1)}
	bg := corners[0]
	for _, c := range corners[1:] {
		if colorDist(bg, c) > 0.08 {
			return matte, true
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			matte.SetAlpha(x, y, color.Alpha{uint8(clamp01((colorDist(m.NRGBAAt(x, y), bg)-0.06)*3) * 255)})
		}
	}
	return matte, false
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func colorDist(a, b color.NRGBA) float64 {
	dr, dg, db := float64(a.R)-float64(b.R), float64(a.G)-float64(b.G), float64(a.B)-float64(b.B)
	return (abs64(dr) + abs64(dg) + abs64(db)) / (3 * 255)
}

func abs64(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func alphaBounds(m *image.Alpha) image.Rectangle {
	b := m.Bounds()
	box := image.Rectangle{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if m.AlphaAt(x, y).A > 8 {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return box
}

func cropNRGBA(m *image.NRGBA, box image.Rectangle) *image.NRGBA {
	out := image.NewNRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	xdraw.Draw(out, out.Bounds(), m, box.Min, xdraw.Src)
	return out
}

func cropAlpha(m *image.Alpha, box image.Rectangle) *image.Alpha {
	out := image.NewAlpha(image.Rect(0, 0, box.Dx(), box.Dy()))
	xdraw.Draw(out, out.Bounds(), m, box.Min, xdraw.Src)
	return out
}

func fitLogo(m *image.NRGBA, matte *image.Alpha) (*image.NRGBA, *image.Alpha) {
	b := m.Bounds()
	if b.Dx() <= logoWidth {
		return m, matte
	}
	h := max(1, b.Dy()*logoWidth/b.Dx())
	r := image.Rect(0, 0, logoWidth, h)
	small := image.NewNRGBA(r)
	xdraw.CatmullRom.Scale(small, r, m, b, xdraw.Over, nil)
	sm := image.NewAlpha(r)
	xdraw.CatmullRom.Scale(sm, r, matte, b, xdraw.Over, nil)
	return small, sm
}

func paintMatte(m *image.Alpha, c color.NRGBA) *image.NRGBA {
	b := m.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, m.AlphaAt(b.Min.X+x, b.Min.Y+y).A})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Brands: the studios and networks of titles
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Agreeing, and taking it back
// ---------------------------------------------------------------------------

// Agreed is told an administrator agreed to downloads: a row for each console name given, and
// consoles given up on before are looked for again.
func (l *Logos) Agreed(ctx context.Context, consoleNames []string) error {
	for _, n := range consoleNames {
		if strings.TrimSpace(n) == "" {
			continue
		}
		if _, err := l.EnsureLogo(ctx, LogoConsole, n); err != nil {
			return err
		}
	}
	if _, err := l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET status = ?, tried_at = 0 WHERE status = ? AND kind = ?`, LogoPending, LogoNone, LogoConsole); err != nil {
		return err
	}
	l.Kick()
	return nil
}

// ForgetDownloadedKind drops every logo of a kind that was downloaded ("" for every kind), files
// and all, and says how many. Each is looked for again, as on a new server. A logo an
// administrator uploaded is theirs, and stays, as does their choice of the name alone.
func (l *Logos) ForgetDownloadedKind(ctx context.Context, kind string) (int, error) {
	only, args := "", []any{LogoFound}
	if kind != "" {
		only, args = " AND kind = ?", append(args, kind)
	}
	rows, err := l.DB.QueryContext(ctx, `SELECT id, file FROM wittyplay_logos WHERE status = ? AND source <> 'you'`+only, args...)
	if err != nil {
		return 0, err
	}
	var gone []Logo
	for rows.Next() {
		var g Logo
		if err := rows.Scan(&g.ID, &g.file); err != nil {
			rows.Close()
			return 0, err
		}
		gone = append(gone, g)
	}
	rows.Close()
	for _, g := range gone {
		l.removeFiles(g.file)
	}
	_, err = l.DB.ExecContext(ctx, `UPDATE wittyplay_logos SET status = ?, file = '', source = '', source_id = '', source_url = '', license = '', license_url = '', credit = '', opaque = 0, version = version + 1
		WHERE status = ? AND source <> 'you'`+only, append([]any{LogoPending}, args...)...)
	return len(gone), err
}
