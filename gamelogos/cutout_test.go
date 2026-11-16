// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamelogos

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// A light grey console with a white button on it, on white: the white round it goes, the console
// and its button stay.
func TestCutOutWhiteBackground(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			c := color.RGBA{255, 255, 255, 255}
			if x >= 30 && x < 90 && y >= 20 && y < 60 {
				c = color.RGBA{200, 200, 205, 255} // the console
				if x >= 55 && x < 65 && y >= 35 && y < 45 {
					c = color.RGBA{255, 255, 255, 255} // a white button, inside it
				}
			}
			img.Set(x, y, c)
		}
	}
	var src bytes.Buffer
	if err := jpeg.Encode(&src, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
	out, ok, err := CutOut(src.Bytes())
	if err != nil || !ok {
		t.Fatalf("CutOut = ok %v, err %v; want a cut-out", ok, err)
	}
	got, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	alpha := func(x, y int) uint8 { return color.NRGBAModel.Convert(got.At(x, y)).(color.NRGBA).A }
	for _, p := range [][2]int{{2, 2}, {117, 77}, {10, 40}, {60, 10}} {
		if a := alpha(p[0], p[1]); a != 0 {
			t.Errorf("background at %v: alpha %d, want 0", p, a)
		}
	}
	for _, p := range [][2]int{{40, 30}, {80, 50}, {60, 40}} {
		if a := alpha(p[0], p[1]); a != 255 {
			t.Errorf("console at %v: alpha %d, want 255 (the white button inside it stays)", p, a)
		}
	}
}

// A photo taken in a room, its edge all sorts of colours, is left as it is.
func TestCutOutLeavesABusyEdge(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), uint8((x + y) * 2), 255})
		}
	}
	var src bytes.Buffer
	_ = png.Encode(&src, img)
	if _, ok, err := CutOut(src.Bytes()); ok || err != nil {
		t.Fatalf("CutOut = ok %v, err %v; want it left as it is", ok, err)
	}
}

// A picture that's already see-through round its edge is left as it is.
func TestCutOutLeavesATransparentOne(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	img.Set(20, 20, color.NRGBA{10, 10, 10, 255})
	var src bytes.Buffer
	_ = png.Encode(&src, img)
	if _, ok, _ := CutOut(src.Bytes()); ok {
		t.Fatal("CutOut cut a picture that was already see-through")
	}
}

func TestCutOutRefusesNonsense(t *testing.T) {
	if _, ok, err := CutOut([]byte("not a picture")); ok || err == nil {
		t.Fatalf("CutOut = ok %v, err %v; want an error", ok, err)
	}
}
