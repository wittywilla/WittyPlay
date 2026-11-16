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
// colour does it fill inward from the edge through pixels nearly exactly that colour, making them
// transparent, and take big flat pockets of it the edge can't reach (inside a cord's loop). The
// limit is strict, so it stops at a light grey or cream console rather than seeping into it. The
// picture's own edge pixels get the share of opacity their colour says they have, against the
// picture's colour just inside, with the background's colour taken out, so the edge is soft and
// has no white fringe.
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

	n := w * h
	at := func(i int) color.NRGBA { o := i * 4; p := pix.Pix; return color.NRGBA{p[o], p[o+1], p[o+2], p[o+3]} }
	dist := make([]uint8, n)
	for i := range dist {
		dist[i] = uint8(far(at(i), bg))
	}
	near4 := func(i int, f func(j int)) {
		x, y := i%w, i/w
		if x > 0 {
			f(i - 1)
		}
		if x < w-1 {
			f(i + 1)
		}
		if y > 0 {
			f(i - w)
		}
		if y < h-1 {
			f(i + w)
		}
	}

	// The background: from every edge pixel that is it, inward through pixels that are nearly
	// exactly its colour. A strict limit, so light plastic (a Famicom's cream, an SNES's grey) is
	// never taken, however close to white it is.
	gone := make([]bool, n)
	fill := func(seeds []int, ok func(int) bool, mark []bool) int {
		queue := append([]int(nil), seeds...)
		count := 0
		for _, i := range seeds {
			mark[i] = true
			count++
		}
		for len(queue) > 0 {
			i := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			near4(i, func(j int) {
				if !mark[j] && ok(j) {
					mark[j] = true
					count++
					queue = append(queue, j)
				}
			})
		}
		return count
	}
	isBg := func(i int) bool { return dist[i] <= cutBackground }
	var seeds []int
	addSeed := func(i int) {
		if isBg(i) && !gone[i] {
			gone[i] = true
			seeds = append(seeds, i)
		}
	}
	for x := 0; x < w; x++ {
		addSeed(x)
		addSeed((h-1)*w + x)
	}
	for y := 0; y < h; y++ {
		addSeed(y * w)
		addSeed(y*w + w - 1)
	}
	for _, i := range seeds {
		gone[i] = false
	}
	fill(seeds, isBg, gone)

	// Pockets of background the edge can't reach: inside a loop of a controller's cord, between a
	// controller and the console. Only big, flat ones (a studio's backdrop, not a white label or
	// button, which have shading and edges of their own).
	seen := make([]bool, n)
	minPocket := max(64, n/400)
	for i := 0; i < n; i++ {
		if gone[i] || seen[i] || !isBg(i) {
			continue
		}
		var members []int
		queue := []int{i}
		seen[i] = true
		sum := 0
		for len(queue) > 0 {
			k := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			members = append(members, k)
			sum += int(dist[k])
			near4(k, func(j int) {
				if !seen[j] && !gone[j] && isBg(j) {
					seen[j] = true
					queue = append(queue, j)
				}
			})
		}
		if len(members) >= minPocket && sum <= len(members)*cutPocketFlat {
			for _, k := range members {
				gone[k] = true
			}
		}
	}

	// Specks: a few light pixels left in the background where a JPEG's noise went just past the
	// limit. A small island of light pixels with background all round it is background too.
	seen = make([]bool, n)
	maxSpeck := max(6, n/40000)
	for i := 0; i < n; i++ {
		if gone[i] || seen[i] {
			continue
		}
		var members []int
		queue := []int{i}
		seen[i] = true
		light := true
		for len(queue) > 0 && len(members) <= maxSpeck {
			k := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			members = append(members, k)
			light = light && dist[k] <= cutSpeck
			near4(k, func(j int) {
				if !seen[j] && !gone[j] {
					seen[j] = true
					queue = append(queue, j)
				}
			})
		}
		if len(queue) == 0 && len(members) <= maxSpeck && light {
			for _, k := range members {
				gone[k] = true
			}
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

	// The edge: each pixel of the picture beside the background is partly background (a photo's
	// edge blends the two). How much is read from its colour against the picture's own colour just
	// inside it: a mid-grey pixel beside a black controller is half see-through, with the white
	// taken out, so there's no white fringe; a light pixel beside light plastic stays as it is.
	edge := func(i int) bool {
		if gone[i] {
			return false
		}
		x, y := i%w, i/w
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				xx, yy := x+dx, y+dy
				if xx >= 0 && yy >= 0 && xx < w && yy < h && gone[yy*w+xx] {
					return true
				}
			}
		}
		return false
	}
	alpha := make([]float64, n)
	for i := range alpha {
		alpha[i] = 1
		if gone[i] {
			alpha[i] = 0
		}
	}
	for i := 0; i < n; i++ {
		if !edge(i) {
			continue
		}
		// The picture's own colour here: the farthest from the background within two pixels.
		x, y := i%w, i/w
		inside := 0
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				xx, yy := x+dx, y+dy
				if xx >= 0 && yy >= 0 && xx < w && yy < h && !gone[yy*w+xx] {
					inside = max(inside, int(dist[yy*w+xx]))
				}
			}
		}
		if inside <= cutBackground {
			continue
		}
		a := float64(int(dist[i])-cutBackground) / float64(inside-cutBackground)
		if a < 1 {
			alpha[i] = max(a, 0)
		}
	}
	for i := 0; i < n; i++ {
		o := i * 4
		switch a := alpha[i]; {
		case a <= 0.02:
			pix.Pix[o+3] = 0
		case a < 0.98:
			c := at(i)
			pix.Pix[o] = unmix(c.R, bg.R, a)
			pix.Pix[o+1] = unmix(c.G, bg.G, a)
			pix.Pix[o+2] = unmix(c.B, bg.B, a)
			pix.Pix[o+3] = uint8(a * float64(c.A))
		}
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
	// How far from the background's colour, at most, on any of red, green and blue (0 to 255), a
	// pixel is still background: strict, so light plastic is never taken. A JPEG's white is within it.
	cutBackground = 12
	// How flat a pocket of background the edge can't reach must be: its pixels' average distance.
	cutPocketFlat = 5
	// How light a speck in the background is, at most, to be taken as background.
	cutSpeck = 48
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
