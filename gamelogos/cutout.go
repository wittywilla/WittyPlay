// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamelogos

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sort"

	_ "image/jpeg" // Commons' thumbnails
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// CutOut takes a picture's plain background away: most photos of a console's hardware on
// Wikimedia Commons are shot on white, which reads as a white box on WittyPlay's dark pages. It
// looks at the colour all round the picture's edge, and only when nearly all of it is one plain
// colour does it fill inward from the edge through that colour, making it transparent. The fill
// goes by how far each pixel is from the background's colour, never by how far it is from its
// neighbour, so it stops at a light grey console rather than creeping into it. Pixels near the
// limit get some of their opacity back, and the background's colour is taken out of them, so the
// edge is soft and has no white halo.
//
// ok is false when the picture is left as it is: its edge isn't one plain colour (a photo taken
// in a room), it's already transparent there, or nearly all of it would go.
func CutOut(src []byte) (out []byte, ok bool, err error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(src))
	if err != nil {
		return nil, false, err
	}
	if cfg.Width < 8 || cfg.Height < 8 || cfg.Width*cfg.Height > maxCutOutPixels {
		return nil, false, nil
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	pix := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			pix.Set(x, y, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	bg, plain := plainEdge(pix)
	if !plain {
		return nil, false, nil
	}

	// Fill inward from every edge pixel close enough to the background.
	n := w * h
	gone := make([]bool, n)
	queue := make([]int, 0, 2*(w+h))
	at := func(i int) color.NRGBA { o := i * 4; p := pix.Pix; return color.NRGBA{p[o], p[o+1], p[o+2], p[o+3]} }
	try := func(i int) {
		if !gone[i] && far(at(i), bg) < cutLimit {
			gone[i] = true
			queue = append(queue, i)
		}
	}
	for x := 0; x < w; x++ {
		try(x)
		try((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		try(y * w)
		try(y*w + w - 1)
	}
	for len(queue) > 0 {
		i := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		x, y := i%w, i/w
		if x > 0 {
			try(i - 1)
		}
		if x < w-1 {
			try(i + 1)
		}
		if y > 0 {
			try(i - w)
		}
		if y < h-1 {
			try(i + w)
		}
	}
	count := 0
	for _, g := range gone {
		if g {
			count++
		}
	}
	// Nothing to take, or nearly everything: either way the picture is better as it is.
	if count*100 < n*2 || count*100 > n*96 {
		return nil, false, nil
	}

	for i, g := range gone {
		if !g {
			continue
		}
		c := at(i)
		d := far(c, bg)
		o := i * 4
		if d <= cutClear {
			pix.Pix[o+3] = 0
			continue
		}
		// Between clear and the limit: partly kept, with the background's colour taken out.
		a := float64(d-cutClear) / float64(cutLimit-cutClear)
		pix.Pix[o] = unmix(c.R, bg.R, a)
		pix.Pix[o+1] = unmix(c.G, bg.G, a)
		pix.Pix[o+2] = unmix(c.B, bg.B, a)
		pix.Pix[o+3] = uint8(a * float64(c.A))
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, pix); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}

const (
	// A Commons thumbnail is far smaller; anything bigger isn't worth the memory on a small server.
	maxCutOutPixels = 4096 * 4096
	// How far from the background's colour, at most, on any of red, green and blue (0 to 255): up
	// to cutClear is see-through, up to cutLimit fades in, and from there on it's the picture.
	cutClear = 14
	cutLimit = 46
	// How alike the edge must be: this share of it within edgeLimit of its middle colour.
	edgeShare = 0.82
	edgeLimit = 28
)

// plainEdge is the colour round the picture's edge, if nearly all of the edge is that one colour
// and it isn't see-through already.
func plainEdge(p *image.NRGBA) (color.NRGBA, bool) {
	w, h := p.Rect.Dx(), p.Rect.Dy()
	var edge []color.NRGBA
	add := func(x, y int) { edge = append(edge, p.NRGBAAt(x, y)) }
	for x := 0; x < w; x++ {
		add(x, 0)
		add(x, h-1)
	}
	for y := 1; y < h-1; y++ {
		add(0, y)
		add(w-1, y)
	}
	mid := func(get func(color.NRGBA) uint8) uint8 {
		v := make([]int, len(edge))
		for i, c := range edge {
			v[i] = int(get(c))
		}
		sort.Ints(v)
		return uint8(v[len(v)/2])
	}
	bg := color.NRGBA{mid(func(c color.NRGBA) uint8 { return c.R }), mid(func(c color.NRGBA) uint8 { return c.G }), mid(func(c color.NRGBA) uint8 { return c.B }), mid(func(c color.NRGBA) uint8 { return c.A })}
	if bg.A < 250 {
		return bg, false
	}
	near := 0
	for _, c := range edge {
		if far(c, bg) <= edgeLimit {
			near++
		}
	}
	return bg, float64(near) >= edgeShare*float64(len(edge))
}

// far is how far apart two colours are: the most either of red, green or blue differs.
func far(a, b color.NRGBA) int {
	d := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return max(d(a.R, b.R), d(a.G, b.G), d(a.B, b.B))
}

// unmix takes the background's share out of a colour that is a of the picture and the rest of it
// background: c = a·picture + (1−a)·background.
func unmix(c, bg uint8, a float64) uint8 {
	v := (float64(c) - (1-a)*float64(bg)) / a
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v + 0.5)
}
