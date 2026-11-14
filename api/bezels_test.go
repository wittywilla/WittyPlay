// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"strconv"
	"testing"
)

func aPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImagesAreDecodedAndRemade(t *testing.T) {
	a, h := testApp(t, personHost(t, nil))
	good := aPNG(t, 16, 9)
	// Something extra hiding after the image's end must not survive.
	withTrailer := append(append([]byte{}, good...), []byte("<script>alert(1)</script>")...)
	w := as(t, h, "POST", "/game-bezels?name=a.png", alex, withTrailer)
	if w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var added struct {
		ID     int64       `json:"id"`
		Bezels []GameBezel `json:"bezels"`
	}
	decode(t, w, &added)
	if added.Bezels[0].Width != 16 || added.Bezels[0].Height != 9 {
		t.Errorf("size: %+v", added.Bezels[0])
	}
	bid := strconv.FormatInt(added.ID, 10)
	got := as(t, h, "GET", "/game-bezels/"+bid, alex, nil)
	if got.Code != 200 || got.Header().Get("Content-Type") != "image/png" || got.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("served as %d %v", got.Code, got.Header())
	}
	if bytes.Contains(got.Body.Bytes(), []byte("<script")) {
		t.Error("what was hiding in the file got through")
	}
	if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Errorf("what's served isn't a PNG: %v", err)
	}

	hostile := map[string][]byte{
		"svg":         []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html":        []byte(`<!doctype html><script>alert(1)</script>`),
		"text":        []byte("hello"),
		"empty":       {},
		"cut in half": good[:len(good)/2],
		"gif":         []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"),
	}
	for name, b := range hostile {
		if w := as(t, h, "POST", "/game-bezels?name=x.svg", alex, b); w.Code < 400 {
			t.Errorf("%s was accepted: %d", name, w.Code)
		}
	}
	// A tiny file claiming enormous pixels is refused from its header alone.
	var bomb bytes.Buffer
	bomb.Write([]byte("\x89PNG\r\n\x1a\n"))
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, 60000)
	binary.BigEndian.PutUint32(ihdr[4:], 60000)
	ihdr[8], ihdr[9] = 8, 6
	_ = binary.Write(&bomb, binary.BigEndian, uint32(13))
	bomb.WriteString("IHDR")
	bomb.Write(ihdr)
	_ = binary.Write(&bomb, binary.BigEndian, crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr...)))
	if w := as(t, h, "POST", "/game-bezels?name=bomb.png", alex, bomb.Bytes()); w.Code != 422 {
		t.Errorf("a decompression bomb got: %d %s", w.Code, w.Body)
	}
	if w := as(t, h, "POST", "/game-bezels?name=big.png", alex, make([]byte, maxBezelUpload+1)); w.Code != 413 {
		t.Errorf("oversized got: %d", w.Code)
	}

	// Nobody reaches anyone else's.
	if w := as(t, h, "GET", "/game-bezels/"+bid, sam, nil); w.Code != 404 {
		t.Errorf("another person read an image: %d", w.Code)
	}
	if w := as(t, h, "DELETE", "/game-bezels/"+bid, sam, nil); w.Code != 404 {
		t.Errorf("another person removed an image: %d", w.Code)
	}
	if w := as(t, h, "GET", "/game-bezels", sam, nil); bytes.Contains(w.Body.Bytes(), []byte(`"a.png"`)) {
		t.Errorf("another person's list: %s", w.Body)
	}

	// A guest only looks; a Kids profile may bring its own.
	if w := as(t, h, "POST", "/game-bezels?name=x", gus, good); w.Code != 403 {
		t.Errorf("a guest: %d", w.Code)
	}
	if w := as(t, h, "POST", "/game-bezels?name=x", kit, good); w.Code != 201 {
		t.Errorf("a Kids profile: %d", w.Code)
	}

	// There's a limit on how many a person keeps.
	for range maxBezels {
		as(t, h, "POST", "/game-bezels?name=n.png", alex, good)
	}
	if w := as(t, h, "POST", "/game-bezels?name=n.png", alex, good); w.Code != 409 {
		t.Errorf("too many images got: %d", w.Code)
	}
	if w := as(t, h, "DELETE", "/game-bezels/"+bid, alex, nil); w.Code != 200 {
		t.Errorf("delete: %d", w.Code)
	}
	if _, err := os.Stat(a.bezelFile(alex.User, added.ID)); err == nil {
		t.Error("the file stayed")
	}
	a.forget(alex.User)
	if _, err := os.Stat(a.bezelDir(alex.User)); err == nil {
		t.Error("an account's images stayed")
	}
}
