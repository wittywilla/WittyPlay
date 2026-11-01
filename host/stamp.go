package host

import (
	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
)

// Numbers drawn into the picture, so that what arrives at the far end can be matched to
// what left without believing anything else on the way. A strip along the top holds the
// frame's number and the newest input it saw, each 16 bits, most significant first, as
// white or black cells that survive any encoder. web/latency.ts reads them back.
//
// Only wittyplay-probe asks for this. It never runs in a normal game.

// StampBits is how many cells the strip has.
const StampBits = 32

// stampCell is the size of a cell for a picture of this size. Two pixels of chroma
// resolution and a little ringing fit well inside it on the smallest picture a console draws.
func stampCell(w, h int) (cw, ch int) {
	return max(w/40, 4), max(h/28, 4)
}

func stampFrame(frame []byte, w, h, format int, t ipc.FrameTiming) {
	bpp := engine.BytesPerPixel(format)
	cw, ch := stampCell(w, h)
	if bpp == 0 || cw*StampBits > w || ch > h || len(frame) < w*h*bpp {
		return
	}
	bits := uint32(uint16(t.Seq))<<16 | uint32(uint16(t.InputSeq))
	for i := 0; i < StampBits; i++ {
		var v byte
		if bits&(1<<(StampBits-1-i)) != 0 {
			v = 0xff
		}
		for y := 0; y < ch; y++ {
			row := frame[y*w*bpp+i*cw*bpp:][:cw*bpp]
			for x := range row {
				row[x] = v
			}
		}
	}
	// Known colours under the numbers, each two cells wide, for seeing what the path has
	// done to them.
	if ch*2 > h {
		return
	}
	for i, c := range StampColours {
		px := pixelBytes(c, format)
		for y := ch; y < ch*2; y++ {
			row := frame[y*w*bpp+i*2*cw*bpp:][:2*cw*bpp]
			for x := 0; x+bpp <= len(row); x += bpp {
				copy(row[x:], px)
			}
		}
	}
}

// StampColours are the colours drawn, red, green and blue. web/latency.ts has them too.
var StampColours = [][3]byte{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}, {0, 255, 255}, {255, 0, 255}, {128, 128, 128}, {64, 128, 192}, {192, 96, 48}}

func pixelBytes(c [3]byte, format int) []byte {
	r, g, b := uint16(c[0]), uint16(c[1]), uint16(c[2])
	switch format {
	case engine.FormatXRGB8888:
		return []byte{c[2], c[1], c[0], 0xff}
	case engine.FormatRGB565:
		v := r>>3<<11 | g>>2<<5 | b>>3
		return []byte{byte(v), byte(v >> 8)}
	}
	v := r>>3<<10 | g>>3<<5 | b>>3
	return []byte{byte(v), byte(v >> 8)}
}
