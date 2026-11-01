package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
)

// testrom writes a program for a console nobody has a game with a clear licence for. Each is
// a few instructions, written here by hand, that change what's on screen for as long as they
// run. They exist so that a core can be seen working, and do nothing else.
func testrom(args []string) error {
	fs := flag.NewFlagSet("test-rom", flag.ExitOnError)
	console := fs.String("console", "", "a7800 or dc")
	out := fs.String("o", "", "the file to write: .a78 or .elf")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-o is needed")
	}
	switch *console {
	case "a7800":
		return os.WriteFile(*out, testA7800(), 0o644)
	case "dc":
		return os.WriteFile(*out, testDreamcast(), 0o644)
	}
	return fmt.Errorf("no test program for %q", *console)
}

// testA7800 is a 16 KB cartridge whose program steps the background colour once a frame.
func testA7800() []byte {
	const (
		headerSize = 128
		romSize    = 0x4000
		origin     = 0xC000
	)
	code := []byte{
		0x78,       // sei
		0xD8,       // cld
		0xA9, 0x07, // lda #7
		0x85, 0x01, // sta INPTCTRL: stay in 7800 mode
		0xA9, 0x60, // lda #$60
		0x85, 0x3C, // sta CTRL: no display lists, so the background shows
		0xA2, 0x00, // ldx #0
		// frame:
		0xE8,       // inx
		0x86, 0x20, // stx BACKGRND
		0xA5, 0x28, 0x10, 0xFC, // until MSTAT says it's the vertical blank
		0xA5, 0x28, 0x30, 0xFC, // until it's over
		0x4C, 0x0C, 0xC0, // jmp frame
	}
	rom := make([]byte, headerSize+romSize)
	rom[0] = 1
	copy(rom[1:], "ATARI7800")
	copy(rom[17:], "WittyPlay test")
	binary.BigEndian.PutUint32(rom[49:], romSize)
	rom[55], rom[56] = 1, 1 // joysticks
	copy(rom[100:], "ACTUAL CART DATA STARTS HERE")
	image := rom[headerSize:]
	copy(image, code)
	const rti = origin + 0x100
	image[rti-origin] = 0x40
	binary.LittleEndian.PutUint16(image[romSize-6:], rti)    // NMI
	binary.LittleEndian.PutUint16(image[romSize-4:], origin) // reset
	binary.LittleEndian.PutUint16(image[romSize-2:], rti)    // IRQ
	return rom
}

// testDreamcast is an ELF file for the SH-4 whose program fills the frame buffer with a value
// that keeps going up.
func testDreamcast() []byte {
	const (
		entry     = 0x8C010000
		frameBuf  = 0xA5200000
		pixels    = 640 * 480
		ehdrSize  = 52
		phdrSize  = 32
		textStart = ehdrSize + phdrSize
		machineSH = 42
	)
	le := binary.LittleEndian
	// SH-4 instructions are 16 bits. A load from a literal is pc-relative: (pc&~3)+4+disp*4.
	insns := []uint16{
		0xD105, // start: mov.l frame buffer,r1
		0xD205, // mov.l pixels,r2
		0x7301, // add #1,r3
		0x2131, // loop: mov.w r3,@r1
		0x7102, // add #2,r1
		0x4210, // dt r2
		0x8BFB, // bf loop
		0xAFF7, // bra start
		0x0009, // nop, in the branch's delay slot
		0x0009, // nop, to line the constants up
	}
	text := make([]byte, 0, len(insns)*2+16)
	for _, in := range insns {
		text = le.AppendUint16(text, in)
	}
	for len(text)%4 != 0 {
		text = append(text, 0)
	}
	// start's two loads count 5 words on from where they read, so the constants come here.
	for len(text) < 0x18 {
		text = le.AppendUint32(text, 0x00090009)
	}
	text = le.AppendUint32(text, frameBuf)
	text = le.AppendUint32(text, pixels)

	h := make([]byte, textStart)
	copy(h, "\x7fELF")
	h[4], h[5], h[6] = 1, 1, 1 // 32-bit, little-endian, version 1
	le.PutUint16(h[16:], 2)    // an executable
	le.PutUint16(h[18:], machineSH)
	le.PutUint32(h[20:], 1)
	le.PutUint32(h[24:], entry)
	le.PutUint32(h[28:], ehdrSize)
	le.PutUint16(h[40:], ehdrSize)
	le.PutUint16(h[42:], phdrSize)
	le.PutUint16(h[44:], 1)
	le.PutUint16(h[46:], 40)
	p := h[ehdrSize:]
	le.PutUint32(p, 1) // load this
	le.PutUint32(p[4:], textStart)
	le.PutUint32(p[8:], entry)
	le.PutUint32(p[12:], entry)
	le.PutUint32(p[16:], uint32(len(text)))
	le.PutUint32(p[20:], uint32(len(text)))
	le.PutUint32(p[24:], 7)
	le.PutUint32(p[28:], 4)
	return append(h, text...)
}
