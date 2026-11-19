// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // pictures are checked by decoding their header
	_ "image/png"
	"io"
	"regexp"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

// A game's own .wittyplay is a ZIP archive: manifest.json (the same JSON as a bare file) and the
// pictures it names, under images/. Everything in it is checked against the limits below before
// any of it is used, and anything past one refuses the whole file: it is a sidecar anyone could
// have left. Nothing in it is ever run, fetched or followed.

// Archive limits.
const (
	MaxFile     = 64 << 20 // the whole archive
	MaxMembers  = 256
	MaxImage    = 16 << 20 // one picture, unpacked
	MaxPixels   = 8192     // a picture's width and height
	MaxOther    = 4 << 20  // a member this version doesn't use, unpacked
	MaxUnpacked = 128 << 20
	MaxRatio    = 100 // unpacked size over packed size, for anything over 1 MB
)

// ManifestName is the archive member that holds the manifest.
const ManifestName = "manifest.json"

// ArchiveMediaType is the media type of the archive form.
const ArchiveMediaType = "application/vnd.wittyplay+zip"

// IsArchive says whether data looks like a ZIP (rather than a bare JSON file).
func IsArchive(data []byte) bool { return bytes.HasPrefix(data, []byte("PK\x03\x04")) }

var memberRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9 ._-]{0,99}(/[A-Za-z0-9_][A-Za-z0-9 ._-]{0,99}){0,3}$`)

// ValidMemberName says whether name is a safe archive member name: letters, digits, dot, dash,
// underscore and space, in at most three folders, and nothing that climbs out.
func ValidMemberName(name string) bool {
	return memberRe.MatchString(name) && !strings.Contains(name, "..")
}

func isImageMember(name string) bool { return strings.HasPrefix(strings.ToLower(name), "images/") }

func readArchive(data []byte) ([]byte, map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, refuse("", "the archive can't be read: %v", err)
	}
	if len(zr.File) > MaxMembers {
		return nil, nil, refuse("", "the archive has more than %d members", MaxMembers)
	}
	var manifest []byte
	files := map[string][]byte{}
	seen := map[string]bool{}
	var total int64
	for _, f := range zr.File {
		name := f.Name
		if strings.HasSuffix(name, "/") && f.UncompressedSize64 == 0 {
			continue // a folder
		}
		if !ValidMemberName(name) {
			return nil, nil, refuse(name, "a member's name has to be a plain relative name (letters, digits, . _ - and space)")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, nil, refuse(name, "the archive holds this name twice")
		}
		seen[key] = true
		if f.Flags&0x1 != 0 {
			return nil, nil, refuse(name, "encrypted members are not allowed")
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, nil, refuse(name, "only stored and deflated members are allowed")
		}
		if !f.Mode().IsRegular() {
			return nil, nil, refuse(name, "only ordinary files are allowed (no links)")
		}
		limit := int64(MaxOther)
		switch {
		case key == ManifestName:
			limit = MaxSize
		case isImageMember(name):
			limit = MaxImage
		}
		if f.UncompressedSize64 > uint64(limit) {
			return nil, nil, refuse(name, "larger than %d KB unpacked", limit>>10)
		}
		if f.UncompressedSize64 > 1<<20 && f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > MaxRatio {
			return nil, nil, refuse(name, "packed too tightly to be ordinary content")
		}
		b, err := readMember(f, limit)
		if err != nil {
			return nil, nil, refuse(name, "%v", err)
		}
		if total += int64(len(b)); total > MaxUnpacked {
			return nil, nil, refuse("", "the archive unpacks to more than %d MB", MaxUnpacked>>20)
		}
		switch {
		case key == ManifestName:
			manifest = b
		case isImageMember(name):
			if err := checkImage(name, b); err != nil {
				return nil, nil, refuse(name, "%v", err)
			}
			files[name] = b
		default:
			files[name] = b // kept, never used
		}
	}
	if manifest == nil {
		return nil, nil, refuse("", "the archive has no %s", ManifestName)
	}
	return manifest, files, nil
}

// readMember unpacks one member, never more than limit bytes whatever its header says.
func readMember(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("can't be unpacked: %v", err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("larger than %d KB unpacked", limit>>10)
	}
	return b, nil
}

// ImageExts are the picture types a .wittyplay may hold, and what each really is.
var ImageExts = map[string]string{".jpg": "jpeg", ".jpeg": "jpeg", ".png": "png", ".webp": "webp"}

func extOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i < 0 || strings.ContainsRune(name[i:], '/') {
		return ""
	}
	return strings.ToLower(name[i:])
}

// CheckImage checks that b is a picture of the type its name says, of a sensible size.
func CheckImage(name string, b []byte) error { return checkImage(name, b) }

func checkImage(name string, b []byte) error {
	want, ok := ImageExts[extOf(name)]
	if !ok {
		return errors.New("pictures have to be .jpg, .png or .webp")
	}
	if len(b) > MaxImage {
		return fmt.Errorf("pictures can be %d MB at most", MaxImage>>20)
	}
	cfg, got, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return errors.New("not a picture that can be read")
	}
	if got != want {
		return fmt.Errorf("the name says %s but it is %s", want, got)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxPixels || cfg.Height > MaxPixels {
		return fmt.Errorf("pictures can be %d pixels across at most", MaxPixels)
	}
	return nil
}

// ImageType is a picture's file extension by what it is, "" when it is none this format holds.
func ImageType(b []byte) string {
	_, got, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return ""
	}
	return map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[got]
}

// ------------------------------------------------------------------ writing

// Tidy puts a file in the order it is written: languages in their usual form, ids on cheats that
// have none, and pictures nothing names any more dropped (other members are kept, unused).
func (d *Doc) Tidy() {
	if g := d.Game; g != nil {
		for i := range g.Cheats {
			if g.Cheats[i].ID == "" {
				g.Cheats[i].ID = fmt.Sprintf("cheat-%d", i+1)
			}
		}
		if len(g.Artwork) == 0 {
			g.Artwork = nil
		}
	}
	used := map[string]bool{}
	if g := d.Game; g != nil {
		for _, v := range g.Artwork {
			used[v] = true
		}
		for _, v := range g.Screenshots {
			used[v] = true
		}
	}
	for name := range d.Files {
		if isImageMember(name) && !used[name] {
			delete(d.Files, name)
		}
	}
	d.tidyLanguages()
}

// Stamp records who saved the file and with what, now.
func (d *Doc) Stamp(author, tool, toolVersion string, now time.Time) {
	if d.Authorship == nil {
		d.Authorship = &Authorship{}
	}
	a := d.Authorship
	ts := now.UTC().Format(time.RFC3339)
	if a.Created == "" {
		a.Created = ts
	}
	a.Updated = ts
	if author != "" {
		a.Author = author
	}
	if tool != "" {
		if a.Tool == nil {
			a.Tool = &Tool{}
		}
		a.Tool.Name, a.Tool.Version = tool, toolVersion
	}
}

// EncodeArchive checks the file and writes it as an archive: manifest.json first (stored, so it
// can be read without unpacking), then the pictures in name order. The same content gives the
// same bytes.
func (d *Doc) EncodeArchive() ([]byte, error) {
	d.Tidy()
	if err := d.Validate(); err != nil {
		return nil, err
	}
	man, err := d.Encode()
	if err != nil {
		return nil, err
	}
	if len(man) > MaxSize {
		return nil, refuse(ManifestName, "the manifest is larger than %d KB", MaxSize>>10)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	stamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if a := d.Authorship; a != nil {
		if t, err := time.Parse(time.RFC3339, a.Updated); err == nil {
			stamp = t
		}
	}
	add := func(name string, b []byte, method uint16) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method, Modified: stamp})
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	}
	if err := add(ManifestName, man, zip.Store); err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(d.Files) {
		if strings.EqualFold(name, ManifestName) || !ValidMemberName(name) {
			continue
		}
		method := zip.Deflate
		if isImageMember(name) {
			method = zip.Store // pictures are packed already
		}
		if err := add(name, d.Files[name], method); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxFile {
		return nil, refuse("", "the file would be larger than %d MB", MaxFile>>20)
	}
	return buf.Bytes(), nil
}

// NeedsArchive says the file holds pictures, so has to be written as an archive.
func (d *Doc) NeedsArchive() bool {
	for name := range d.Files {
		if isImageMember(name) {
			return true
		}
	}
	return false
}

// EncodeAny writes the file as an archive when it holds pictures, else as bare JSON.
func (d *Doc) EncodeAny() ([]byte, error) {
	d.Tidy()
	if d.NeedsArchive() {
		return d.EncodeArchive()
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return d.Encode()
}

// SetImage puts a picture in the archive for a role and names it in the artwork.
func (d *Doc) SetImage(role string, b []byte) (string, error) {
	if !oneOfList(role, ArtworkRoles) {
		return "", fmt.Errorf("%q is not an artwork role: use %s", role, strings.Join(ArtworkRoles, ", "))
	}
	ext := ImageType(b)
	if ext == "" {
		return "", errors.New("pictures have to be JPEG, PNG or WebP")
	}
	name := "images/" + role + ext
	if err := checkImage(name, b); err != nil {
		return "", err
	}
	g := d.ensureGame()
	if old := g.Artwork[role]; old != "" {
		delete(d.Files, old)
	}
	if d.Files == nil {
		d.Files = map[string][]byte{}
	}
	d.Files[name] = b
	if g.Artwork == nil {
		g.Artwork = Artwork{}
	}
	g.Artwork[role] = name
	return name, nil
}

// AddScreenshot adds a screenshot to the archive.
func (d *Doc) AddScreenshot(b []byte) (string, error) {
	ext := ImageType(b)
	if ext == "" {
		return "", errors.New("pictures have to be JPEG, PNG or WebP")
	}
	g := d.ensureGame()
	if len(g.Screenshots) >= MaxScreenshots {
		return "", fmt.Errorf("at most %d screenshots", MaxScreenshots)
	}
	var name string
	for n := len(g.Screenshots) + 1; ; n++ {
		name = fmt.Sprintf("images/screenshots/%02d%s", n, ext)
		if _, taken := d.Files[name]; !taken {
			break
		}
	}
	if err := checkImage(name, b); err != nil {
		return "", err
	}
	if d.Files == nil {
		d.Files = map[string][]byte{}
	}
	d.Files[name] = b
	g.Screenshots = append(g.Screenshots, name)
	return name, nil
}

func (d *Doc) ensureGame() *About {
	if d.Game == nil {
		d.Game = &About{}
	}
	return d.Game
}
