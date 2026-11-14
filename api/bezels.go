// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // the formats a person's own image may be in
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

// A person's own images for around the game's picture (bezels). Moved from the server
// (api_game_options.go, library/game_options.go): listed in wittyplay_bezels, the files in
// WittyPlay's own folder (bezels/u<user>/b<n>.png) instead of the server's <data>/gaming/bezels.
//
// A person's image is a hostile file until it's been decoded and written out again. Only the
// decoded pixels are kept, as a PNG made here, under a number. An SVG, or anything else with
// instructions in it, never gets past the decoder.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /game-bezels", w(handleBezels))
		mux.Handle("POST /game-bezels", w(handleBezelAdd))
		mux.Handle("GET /game-bezels/{bezel}", w(handleBezelImage))
		mux.Handle("DELETE /game-bezels/{bezel}", w(handleBezelDelete))
	})
	onForget(func(a *App, user int64) { _ = os.RemoveAll(a.bezelDir(user)) })
}

const (
	maxBezelUpload = 16 << 20
	maxBezelPixels = 16 << 20
	maxBezelSide   = 8192
	maxBezels      = 16
)

// Decoding one at a time keeps a burst of big images from using up a small server.
var bezelDecode sync.Mutex

// GameBezel is an image of a person's own for around the picture.
type GameBezel struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"createdAt"`
}

func (a *App) bezelDir(userID int64) string {
	return filepath.Join(a.Dir, "bezels", "u"+strconv.FormatInt(userID, 10))
}

func (a *App) bezelFile(userID, id int64) string {
	return filepath.Join(a.bezelDir(userID), "b"+strconv.FormatInt(id, 10)+".png")
}

func (a *App) gameBezels(ctx context.Context, userID int64) ([]GameBezel, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT id, name, width, height, size, created_at FROM wittyplay_bezels WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GameBezel{}
	for rows.Next() {
		var b GameBezel
		if err := rows.Scan(&b.ID, &b.Name, &b.Width, &b.Height, &b.Size, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (a *App) hasGameBezel(ctx context.Context, userID, id int64) bool {
	var one int
	return a.DB.QueryRowContext(ctx, `SELECT 1 FROM wittyplay_bezels WHERE id = ? AND user_id = ?`, id, userID).Scan(&one) == nil
}

func handleBezels(a *App, w http.ResponseWriter, r *http.Request) {
	list, err := a.gameBezels(r.Context(), who(r).User)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, list)
}

// reencodeImage decodes what a person sent and returns a clean PNG of it. Its size is checked
// from the header before any pixel is decoded, so a small file claiming enormous pixels is refused.
func reencodeImage(b []byte) (out []byte, width, height int, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, errors.New("use a PNG, JPEG or WebP image")
	}
	switch format {
	case "png", "jpeg", "webp":
	default:
		return nil, 0, 0, errors.New("use a PNG, JPEG or WebP image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxBezelSide || cfg.Height > maxBezelSide || cfg.Width*cfg.Height > maxBezelPixels {
		return nil, 0, 0, fmt.Errorf("this image is too large: %d megapixels at most", maxBezelPixels>>20)
	}
	bezelDecode.Lock()
	defer bezelDecode.Unlock()
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, errors.New("this image is damaged")
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), cfg.Width, cfg.Height, nil
}

func handleBezelAdd(a *App, w http.ResponseWriter, r *http.Request) {
	// A Kids profile may bring its own images; a guest only looks (the server's canReact).
	if id := who(r); !id.Admin && id.Kind == "guest" {
		writeErr(w, 403, "a guest can only look")
		return
	}
	ctx, id := r.Context(), who(r).User
	existing, err := a.gameBezels(ctx, id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if len(existing) >= maxBezels {
		writeErr(w, 409, fmt.Sprintf("you can keep %d images at most; remove one first", maxBezels))
		return
	}
	if !takeUploadSlot(w) {
		return
	}
	defer func() { <-importSlots }()
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBezelUpload))
	if err != nil {
		writeErr(w, 413, fmt.Sprintf("this image is too large: %d MB at most", maxBezelUpload>>20))
		return
	}
	clean, width, height, err := reencodeImage(b)
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	res, err := a.DB.ExecContext(ctx, `INSERT INTO wittyplay_bezels(user_id, name, width, height, size, created_at) VALUES(?,?,?,?,?,?)`,
		id, cleanUploadName(r.URL.Query().Get("name"), "Image"), width, height, len(clean), time.Now().Unix())
	var bid int64
	if err == nil {
		bid, err = res.LastInsertId()
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := writeFileAtomic(a.bezelFile(id, bid), clean); err != nil {
		_, _ = a.DB.ExecContext(ctx, `DELETE FROM wittyplay_bezels WHERE id = ?`, bid)
		writeErr(w, 500, err.Error())
		return
	}
	list, _ := a.gameBezels(ctx, id)
	writeJSON(w, 201, map[string]any{"id": bid, "bezels": list})
}

func handleBezelImage(a *App, w http.ResponseWriter, r *http.Request) {
	bid, ok := pathInt(r, "bezel")
	id := who(r).User
	if !ok || !a.hasGameBezel(r.Context(), id, bid) {
		writeErr(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, a.bezelFile(id, bid))
}

func handleBezelDelete(a *App, w http.ResponseWriter, r *http.Request) {
	bid, ok := pathInt(r, "bezel")
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	id := who(r).User
	found, err := changed(a.DB.ExecContext(r.Context(), `DELETE FROM wittyplay_bezels WHERE id = ? AND user_id = ?`, bid, id))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if !found {
		writeErr(w, 404, "not found")
		return
	}
	_ = os.Remove(a.bezelFile(id, bid))
	writeJSON(w, 200, map[string]any{"ok": true})
}
