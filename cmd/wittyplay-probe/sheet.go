// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"

	"github.com/wittywilla/wittyplay"
)

// sheet lays pictures out in pairs, for comparing what the core made with what the browser
// showed by eye: wittyplay-probe sheet out.png a.png b.png c.png d.png
func sheet(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: sheet out.png picture...")
	}
	const cellW, cellH, gap, perRow = 640, 480, 8, 2
	n := len(args) - 1
	rows := (n + perRow - 1) / perRow
	out := image.NewRGBA(image.Rect(0, 0, perRow*cellW+(perRow+1)*gap, rows*cellH+(rows+1)*gap))
	draw.Draw(out, out.Bounds(), image.NewUniform(color.RGBA{40, 40, 40, 255}), image.Point{}, draw.Src)
	for i, name := range args[1:] {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		x0, y0 := gap+(i%perRow)*(cellW+gap), gap+(i/perRow)*(cellH+gap)
		b := img.Bounds()
		// A whole number of times larger, or smaller to fit: nearest, so nothing is hidden.
		scale := min(float64(cellW)/float64(b.Dx()), float64(cellH)/float64(b.Dy()))
		if scale >= 1 {
			scale = float64(int(scale))
		}
		w, h := int(float64(b.Dx())*scale), int(float64(b.Dy())*scale)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out.Set(x0+x, y0+y, img.At(b.Min.X+int(float64(x)/scale), b.Min.Y+int(float64(y)/scale)))
			}
		}
	}
	f, err := os.Create(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

// cia copies the program out of a 3DS install package, as the server does when one is played.
func cia(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: cia from.cia to.cxi")
	}
	t, err := wittyplay.Inspect3DS(args[0])
	if err != nil {
		return err
	}
	return t.CopyProgram(args[0], args[1])
}
