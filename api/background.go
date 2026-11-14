// SPDX-License-Identifier: AGPL-3.0-or-later
//
// WittyPlay - Console games streamed to a browser or an app, with every controller and a touch layout editor.
// Copyright (C) 2026 WittyWilla and contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	stddraw "image/draw"
	"image/jpeg"
	_ "image/png" // decoders for checking uploaded backgrounds
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/wittywilla/wittymodule/service"
)

// The picture behind a person's controller screen, and how it sits. Moved from the server
// (api_game_profiles.go) as it was; the pictures live in WittyPlay's own folder now.

func backgroundRoutes(mux *http.ServeMux, with func(func(*App, http.ResponseWriter, *http.Request)) http.Handler) {
	mux.Handle("GET /game-background", with(handleGameBackgroundGet))
	mux.Handle("PUT /game-background", with(handleGameBackgroundPut))
	mux.Handle("DELETE /game-background", with(handleGameBackgroundDelete))
	mux.Handle("GET /game-background/image", with(handleGameBackgroundImage))
	mux.Handle("PUT /game-background/image", with(handleGameBackgroundUpload))
}

// GameBackground is how a person's picture sits behind their controller screen.
type GameBackground struct {
	Version int64  `json:"version"` // 0 when there's no picture; changes with each new one
	Fit     string `json:"fit"`     // cover | contain
	X       int    `json:"x"`
	Y       int    `json:"y"`
	Dim     int    `json:"dim"`
}

func defaultGameBackground() GameBackground {
	return GameBackground{Fit: "cover", X: 50, Y: 50, Dim: 50}
}

func (a *App) getGameBackground(ctx context.Context, userID int64) (GameBackground, error) {
	b := defaultGameBackground()
	err := a.DB.QueryRowContext(ctx, `SELECT version, fit, x, y, dim FROM wittyplay_backgrounds WHERE user_id = ?`, userID).Scan(&b.Version, &b.Fit, &b.X, &b.Y, &b.Dim)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultGameBackground(), nil
	}
	return b, err
}

func (a *App) setGameBackground(ctx context.Context, userID int64, b GameBackground) error {
	_, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_backgrounds(user_id, version, fit, x, y, dim) VALUES(?,?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET version = excluded.version, fit = excluded.fit, x = excluded.x, y = excluded.y, dim = excluded.dim`,
		userID, b.Version, b.Fit, b.X, b.Y, b.Dim)
	return err
}

// backgroundPath is a person's picture, in WittyPlay's own folder: built only from a number.
func (a *App) backgroundPath(userID int64) string {
	return filepath.Join(a.Dir, "backgrounds", "u"+strconv.FormatInt(userID, 10)+".jpg")
}

func handleGameBackgroundGet(a *App, w http.ResponseWriter, r *http.Request) {
	b, err := a.getGameBackground(r.Context(), who(r).User)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, b)
}

func handleGameBackgroundPut(a *App, w http.ResponseWriter, r *http.Request) {
	var in GameBackground
	if err := readJSON(r, &in); err != nil {
		writeErr(w, 400, "this setting can't be read")
		return
	}
	if (in.Fit != "cover" && in.Fit != "contain") || in.X < 0 || in.X > 100 || in.Y < 0 || in.Y > 100 || in.Dim < 0 || in.Dim > 90 {
		writeErr(w, 400, "this setting is out of range")
		return
	}
	ctx, userID := r.Context(), who(r).User
	cur, err := a.getGameBackground(ctx, userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	in.Version = cur.Version // which picture it is isn't for a client to say
	if err := a.setGameBackground(ctx, userID, in); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, in)
}

// One upload decoded at a time, and none at all while too many are coming in.
var (
	importSlots          = make(chan struct{}, 2)
	decodeBackgroundSlot = make(chan struct{}, 1)
)

func handleGameBackgroundUpload(a *App, w http.ResponseWriter, r *http.Request) {
	if id := who(r); !id.Admin && id.Kind == "guest" {
		writeErr(w, 403, "a guest can only look")
		return
	}
	select {
	case importSlots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "10")
		writeErr(w, 503, "other files are being imported right now. Try again in a moment")
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBackgroundBytes))
	if err != nil {
		writeErr(w, 413, "the picture must be 10 MB or smaller")
		return
	}
	ctx, userID := r.Context(), who(r).User
	select {
	case decodeBackgroundSlot <- struct{}{}:
	case <-ctx.Done():
		return
	}
	clean, err := cleanBackground(b)
	<-decodeBackgroundSlot
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	dst := a.backgroundPath(userID)
	if err := writeFileAtomic(dst, clean); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cur, err := a.getGameBackground(ctx, userID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cur.Version = time.Now().UnixMilli()
	if err := a.setGameBackground(ctx, userID, cur); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cur)
}

func writeFileAtomic(dst string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// GET /game-background/image: only ever the person asking's own.
func handleGameBackgroundImage(a *App, w http.ResponseWriter, r *http.Request) {
	path := a.backgroundPath(who(r).User)
	if _, err := os.Stat(path); err != nil {
		writeErr(w, 404, "no picture")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Disposition", "inline")
	http.ServeFile(w, r, path)
}

func handleGameBackgroundDelete(a *App, w http.ResponseWriter, r *http.Request) {
	userID := who(r).User
	if err := os.Remove(a.backgroundPath(userID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), `DELETE FROM wittyplay_backgrounds WHERE user_id = ?`, userID); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, defaultGameBackground())
}

var _ = service.Who

// Limits on an upload. Decoding an image takes far more memory than its file, so its
// size in pixels is checked from its header first, and one is decoded at a time.
const (
	maxBackgroundBytes  = 10 << 20
	maxBackgroundPixels = 24_000_000
	maxBackgroundSide   = 8192
	minBackgroundSide   = 16
	backgroundOutSide   = 2560 // what's kept: plenty for a screen, and small to send
)

var backgroundFormats = map[string]bool{"png": true, "jpeg": true, "webp": true}

var errBackgroundFormat = errors.New("use a PNG, JPEG or WebP image")

// cleanBackground decodes an upload and draws it again as a new JPEG, so that all that
// survives is pixels: nothing the file carried beyond its picture, whatever it says it
// is. Its header is read before anything else, and refused if it's too big to decode.
func cleanBackground(b []byte) (out []byte, err error) {
	defer func() {
		if p := recover(); p != nil { // a decoder tripped on a hostile file
			out, err = nil, errBackgroundFormat
		}
	}()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || !backgroundFormats[format] {
		return nil, errBackgroundFormat
	}
	if cfg.Width < minBackgroundSide || cfg.Height < minBackgroundSide {
		return nil, fmt.Errorf("the picture must be at least %d pixels on each side", minBackgroundSide)
	}
	if cfg.Width > maxBackgroundSide || cfg.Height > maxBackgroundSide || cfg.Width*cfg.Height > maxBackgroundPixels {
		return nil, errors.New("the picture has too many pixels: use one of up to 24 megapixels")
	}
	src, decoded, err := image.Decode(bytes.NewReader(b))
	if err != nil || decoded != format {
		return nil, errBackgroundFormat
	}
	orientation := 1
	if format == "jpeg" { // phones store upright photos sideways, and say so in the part we drop
		if o := jpegOrientation(b); o >= 2 && o <= 8 {
			orientation = o
		}
	}
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if long := max(w, h); long > backgroundOutSide {
		w, h = max(1, w*backgroundOutSide/long), max(1, h*backgroundOutSide/long)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	stddraw.Draw(dst, dst.Bounds(), image.Black, image.Point{}, stddraw.Src) // transparency becomes black
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, upright(dst, orientation), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// upright turns a picture by its EXIF orientation, 1 to 8.
func upright(src *image.RGBA, orientation int) image.Image {
	if orientation <= 1 {
		return src
	}
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.SetRGBA(dx, dy, src.RGBAAt(x, y))
		}
	}
	return dst
}

// jpegOrientation finds how a JPEG says it should be turned, or 0: the one field of its EXIF
// a background picture needs, so an upload doesn't start a module just for this.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	for p := 2; p+4 <= len(b) && b[p] == 0xFF; {
		marker, size := b[p+1], int(b[p+2])<<8|int(b[p+3])
		if size < 2 || p+2+size > len(b) {
			return 0
		}
		if marker == 0xE1 && size >= 8 && string(b[p+4:p+10]) == "Exif\x00\x00" {
			return exifOrientation(b[p+10 : p+2+size])
		}
		if marker == 0xDA { // the picture itself: nothing to find past here
			return 0
		}
		p += 2 + size
	}
	return 0
}

func exifOrientation(t []byte) int {
	if len(t) < 8 {
		return 0
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	at := int(order.Uint32(t[4:8]))
	if at < 8 || at+2 > len(t) {
		return 0
	}
	n := int(order.Uint16(t[at:]))
	for i := 0; i < n; i++ {
		e := at + 2 + i*12
		if e+12 > len(t) {
			return 0
		}
		if order.Uint16(t[e:]) == 0x0112 {
			return int(order.Uint16(t[e+8:]))
		}
	}
	return 0
}
