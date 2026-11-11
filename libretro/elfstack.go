// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"encoding/binary"
	"errors"
	"io"
)

// A library can say it wants the stack to be executable. Almost none means it: the mark
// comes from one assembly file that left out a line saying it doesn't, and it makes the
// whole library and the process it joins that much easier to attack. Since version 2.41,
// glibc refuses to load such a library at all, so a core built that way can't start on a
// current Linux. elfstack finds the mark, so that the core can be loaded without it.

const (
	elfClass32 = 1
	elfClass64 = 2

	ptGNUStack = 0x6474e551
	pfExec     = 1

	// A real library has about a dozen program headers.
	maxProgramHeaders = 256
)

// execStackMark says where in an ELF file the flags are that ask for an executable stack,
// when they do. A library with no such header at all is left to whoever loads it. A file
// that isn't ELF, or is damaged, isn't asking for anything, and the loader says what's
// wrong with it.
func execStackMark(f io.ReaderAt) (flagsAt int64, order binary.ByteOrder, wanted bool) {
	var h [64]byte
	if _, err := f.ReadAt(h[:], 0); err != nil && !errors.Is(err, io.EOF) {
		return 0, nil, false
	}
	if string(h[:4]) != "\x7fELF" {
		return 0, nil, false
	}
	switch h[5] {
	case 1:
		order = binary.LittleEndian
	case 2:
		order = binary.BigEndian
	default:
		return 0, nil, false
	}
	var at, size int64
	var count int
	var flagsOffset int64
	switch h[4] {
	case elfClass64:
		at, size, count, flagsOffset = int64(order.Uint64(h[32:])), int64(order.Uint16(h[54:])), int(order.Uint16(h[56:])), 4
		if size < 56 {
			return 0, nil, false
		}
	case elfClass32:
		at, size, count, flagsOffset = int64(order.Uint32(h[28:])), int64(order.Uint16(h[42:])), int(order.Uint16(h[44:])), 24
		if size < 32 {
			return 0, nil, false
		}
	default:
		return 0, nil, false
	}
	if at <= 0 || count > maxProgramHeaders {
		return 0, nil, false
	}
	var p [8]byte
	for i := int64(0); i < int64(count); i++ {
		head := at + i*size
		if _, err := f.ReadAt(p[:4], head); err != nil {
			return 0, nil, false
		}
		if order.Uint32(p[:]) != ptGNUStack {
			continue
		}
		if _, err := f.ReadAt(p[:4], head+flagsOffset); err != nil {
			return 0, nil, false
		}
		if order.Uint32(p[:])&pfExec == 0 {
			return 0, nil, false
		}
		return head + flagsOffset, order, true
	}
	return 0, nil, false
}

// clearExecStack takes the mark off a copy of a library held in memory.
func clearExecStack(b []byte, flagsAt int64, order binary.ByteOrder) error {
	if flagsAt < 0 || flagsAt+4 > int64(len(b)) {
		return errors.New("the library is shorter than its headers say")
	}
	order.PutUint32(b[flagsAt:], order.Uint32(b[flagsAt:])&^pfExec)
	return nil
}
