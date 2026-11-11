// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

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
	// The display list list: 12 zones of 16 lines, all showing an empty display list. Only the
	// background colour is then left to show.
	const (
		emptyList = origin + 0x100
		zones     = origin + 0x110
	)
	code := []byte{
		0x78,       // sei
		0xD8,       // cld
		0xA9, 0x07, // lda #7
		0x85, 0x01, // sta INPTCTRL: stay in 7800 mode
		0xA9, zones >> 8, // lda #>zones
		0x85, 0x2C, // sta DPPH
		0xA9, zones & 0xFF, // lda #<zones
		0x85, 0x30, // sta DPPL
		0xA9, 0x40, // lda #$40
		0x85, 0x3C, // sta CTRL: display lists on
		0xA2, 0x00, // ldx #0
		// frame:
		0xE8,       // inx
		0x86, 0x20, // stx BACKGRND
		0xA5, 0x28, 0x10, 0xFC, // until MSTAT says it's the vertical blank
		0xA5, 0x28, 0x30, 0xFC, // until it's over
		0x4C, 0x14, 0xC0, // jmp frame
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
	for i := 0; i < 12; i++ {
		entry := image[zones-origin+i*3:]
		entry[0], entry[1], entry[2] = 0x0F, emptyList>>8, emptyList&0xFF // 16 lines
	}
	const rti = origin + 0x200
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
	var insns []uint16
	var literals []uint32
	var fixups []int
	load := func(reg uint16, v uint32) {
		fixups = append(fixups, len(insns))
		insns = append(insns, 0xD000|reg<<8)
		literals = append(literals, v)
	}
	set := func(register, v uint32) {
		load(4, register)
		load(5, v)
		insns = append(insns, 0x2452) // mov.l r5,@r4
	}
	// Turn the picture on: 640x480 at the start of the frame buffer, in RGB565.
	set(0xA05F8050, 0x00200000)        // FB_R_SOF1
	set(0xA05F805C, 1<<20|479<<10|319) // FB_R_SIZE
	set(0xA05F8044, 1|1<<2)            // FB_R_CTRL
	set(0xA05F80E8, 0x00000000)        // VO_CONTROL: not blanked
	start := len(insns)
	load(1, frameBuf)
	load(2, pixels)
	insns = append(insns, 0x7301) // add #1,r3
	loop := len(insns)
	insns = append(insns, 0x2131, 0x7102, 0x4210)                        // mov.w r3,@r1; add #2,r1; dt r2
	insns = append(insns, 0x8B00|uint16(uint8(int8(loop-len(insns)-2)))) // bf loop
	insns = append(insns, 0xA000|uint16(start-len(insns)-2)&0xFFF)       // bra start
	insns = append(insns, 0x0009)                                        // nop, in its delay slot
	if len(insns)%2 != 0 {
		insns = append(insns, 0x0009)
	}
	for i, at := range fixups {
		pc := at * 2
		pool := len(insns)*2 + i*4
		insns[at] |= uint16((pool - (pc&^3 + 4)) / 4)
	}
	text := make([]byte, 0, len(insns)*2+len(literals)*4)
	for _, in := range insns {
		text = le.AppendUint16(text, in)
	}
	for _, v := range literals {
		text = le.AppendUint32(text, v)
	}

	names := []byte("\x00.text\x00.shstrtab\x00")
	textLen := uint32(len(text))
	body := append(append([]byte{}, text...), names...)
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	shoff := uint32(textStart + len(body))
	section := func(name, kind, flags, addr, off, size uint32) []byte {
		sh := make([]byte, 40)
		le.PutUint32(sh, name)
		le.PutUint32(sh[4:], kind)
		le.PutUint32(sh[8:], flags)
		le.PutUint32(sh[12:], addr)
		le.PutUint32(sh[16:], off)
		le.PutUint32(sh[20:], size)
		le.PutUint32(sh[32:], 4)
		return sh
	}
	body = append(body, make([]byte, 40)...)
	body = append(body, section(1, 1, 6, entry, textStart, textLen)...)
	body = append(body, section(7, 3, 0, 0, textStart+textLen, uint32(len(names)))...)

	h := make([]byte, textStart)
	copy(h, "\x7fELF")
	h[4], h[5], h[6] = 1, 1, 1 // 32-bit, little-endian, version 1
	le.PutUint16(h[16:], 2)    // an executable
	le.PutUint16(h[18:], machineSH)
	le.PutUint32(h[20:], 1)
	le.PutUint32(h[24:], entry)
	le.PutUint32(h[28:], ehdrSize)
	le.PutUint32(h[32:], shoff)
	le.PutUint16(h[40:], ehdrSize)
	le.PutUint16(h[42:], phdrSize)
	le.PutUint16(h[44:], 1)
	le.PutUint16(h[46:], 40)
	le.PutUint16(h[48:], 3)
	le.PutUint16(h[50:], 2)
	p := h[ehdrSize:]
	le.PutUint32(p, 1) // load this
	le.PutUint32(p[4:], textStart)
	le.PutUint32(p[8:], entry)
	le.PutUint32(p[12:], entry)
	le.PutUint32(p[16:], textLen)
	le.PutUint32(p[20:], textLen)
	le.PutUint32(p[24:], 7)
	le.PutUint32(p[28:], 4)
	return append(h, body...)
}
