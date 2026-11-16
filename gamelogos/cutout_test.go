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

// A cream console (a Famicom's plastic, close to white) with a small white button on it, a black
// controller, and a cord making a loop of background the edge can't reach, all on white: the white
// round them and inside the loop goes; the cream plastic, the button and the controller stay whole.
func TestCutOutWhiteBackground(t *testing.T) {
	const W, H = 200, 120
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			c := color.RGBA{255, 255, 255, 255}
			switch {
			case x >= 90 && x < 180 && y >= 20 && y < 100:
				c = color.RGBA{236, 232, 218, 255} // the console's cream plastic
				if x >= 130 && x < 136 && y >= 50 && y < 56 {
					c = color.RGBA{255, 255, 255, 255} // a small white button on it
				}
			case x >= 10 && x < 50 && y >= 70 && y < 100:
				c = color.RGBA{20, 20, 24, 255} // a black controller
			case (x >= 20 && x < 70 && (y == 20 || y == 21 || y == 60 || y == 61)) || (y >= 20 && y < 62 && (x == 20 || x == 21 || x == 68 || x == 69)):
				c = color.RGBA{30, 30, 30, 255} // a cord's loop, around a pocket of white
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
	for name, p := range map[string][2]int{"top left": {2, 2}, "bottom right": {197, 117}, "between": {75, 90}, "inside the cord's loop": {45, 40}} {
		if a := alpha(p[0], p[1]); a != 0 {
			t.Errorf("background %s at %v: alpha %d, want 0", name, p, a)
		}
	}
	for name, p := range map[string][2]int{"cream plastic": {100, 30}, "more cream": {170, 90}, "the white button": {132, 52}, "the controller": {30, 85}} {
		if a := alpha(p[0], p[1]); a != 255 {
			t.Errorf("%s at %v: alpha %d, want 255", name, p, a)
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
