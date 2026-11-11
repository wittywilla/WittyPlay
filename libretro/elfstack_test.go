// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package libretro

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// fakeELF is the headers of a library, with a program header for each type given, and
// the last one's flags set as asked.
func fakeELF(class byte, order binary.ByteOrder, flags uint32, types ...uint32) []byte {
	b := make([]byte, 0x1000)
	copy(b, "\x7fELF")
	b[4] = class
	b[5] = 1
	if order == binary.BigEndian {
		b[5] = 2
	}
	const phoff = 0x40
	if class == elfClass64 {
		order.PutUint64(b[32:], phoff)
		order.PutUint16(b[54:], 56)
		order.PutUint16(b[56:], uint16(len(types)))
		for i, t := range types {
			order.PutUint32(b[phoff+i*56:], t)
			order.PutUint32(b[phoff+i*56+4:], flags)
		}
		return b
	}
	order.PutUint32(b[28:], phoff)
	order.PutUint16(b[42:], 32)
	order.PutUint16(b[44:], uint16(len(types)))
	for i, t := range types {
		order.PutUint32(b[phoff+i*32:], t)
		order.PutUint32(b[phoff+i*32+24:], flags)
	}
	return b
}

func TestExecStackMark(t *testing.T) {
	const load = 1
	le, be := binary.ByteOrder(binary.LittleEndian), binary.ByteOrder(binary.BigEndian)
	for _, c := range []struct {
		name   string
		data   []byte
		wanted bool
	}{
		{"64-bit asking", fakeELF(elfClass64, le, 7, load, ptGNUStack), true},
		{"64-bit not asking", fakeELF(elfClass64, le, 6, load, ptGNUStack), false},
		{"32-bit asking", fakeELF(elfClass32, le, 7, load, ptGNUStack), true},
		{"32-bit not asking", fakeELF(elfClass32, le, 6, load, ptGNUStack), false},
		{"big-endian asking", fakeELF(elfClass64, be, 7, ptGNUStack), true},
		{"no stack header", fakeELF(elfClass64, le, 7, load, load), false},
		{"not ELF", []byte("MZ this is something else"), false},
		{"empty", nil, false},
		{"cut short", fakeELF(elfClass64, le, 7, load, ptGNUStack)[:0x50], false},
	} {
		t.Run(c.name, func(t *testing.T) {
			at, order, wanted := execStackMark(bytes.NewReader(c.data))
			if wanted != c.wanted {
				t.Fatalf("wanted = %v, expected %v", wanted, c.wanted)
			}
			if !wanted {
				return
			}
			out := bytes.Clone(c.data)
			if err := clearExecStack(out, at, order); err != nil {
				t.Fatal(err)
			}
			if _, _, again := execStackMark(bytes.NewReader(out)); again {
				t.Error("it still asks after the mark came off")
			}
			if diff := countDiffering(c.data, out); diff != 1 {
				t.Errorf("%d bytes changed; only the one bit should", diff)
			}
		})
	}
	// A header table that claims to be huge isn't followed.
	huge := fakeELF(elfClass64, le, 7, load)
	le.PutUint16(huge[56:], 0xFFFF)
	if _, _, wanted := execStackMark(bytes.NewReader(huge)); wanted {
		t.Error("followed a program header count of 65535")
	}
	if err := clearExecStack(make([]byte, 4), 8, le); err == nil {
		t.Error("cleared past the end of the file")
	}
}

func elfLE() binary.ByteOrder { return binary.LittleEndian }

func countDiffering(a, b []byte) int {
	n := 0
	for i := range a {
		if a[i] != b[i] {
			n++
		}
	}
	return n
}
