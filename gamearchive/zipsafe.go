// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// zipCheck (moved with this package from the server's zipsafe) checks a zip before anything allocates for it. Reading a zip's directory takes
// memory for each entry and its name, and looking a name up in it takes memory for every folder
// of every name, so a file can ask for far more memory than it takes on disk.
package gamearchive

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

var (
	// errZipTooLarge: the directory is bigger than this kind of file ever needs.
	errZipTooLarge = errors.New("this zip's list of contents is too large")
	// errZipDamaged: it has no readable directory.
	errZipDamaged = errors.New("this zip has no readable list of contents")
)

// zipLimits are what one kind of file may hold. Zero means no limit.
type zipLimits struct {
	MaxEntries   int64 // entries in the directory
	MaxDirectory int64 // bytes of it
	MaxName      int   // bytes in one name
	MaxFolders   int64 // folders of all names added up: what a lookup's memory grows with
}

const (
	zipEndLen     = 22         // the record at a zip's end, without its comment
	zipMaxTail    = 65535 + 22 // that record with the longest comment
	zipLocatorLen = 20         // what points at the zip64 record
	zipEnd64Len   = 56         // the zip64 record
)

// zipCheck reads a zip's end, where it says how many entries follow and how much room they take.
func zipCheck(r io.ReaderAt, size int64, l zipLimits) error {
	if size < zipEndLen {
		return errZipDamaged
	}
	tail := min(size, zipMaxTail)
	buf := make([]byte, tail)
	if _, err := r.ReadAt(buf, size-tail); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	i := zipFindEnd(buf)
	if i < 0 {
		return errZipDamaged
	}
	entries := int64(binary.LittleEndian.Uint16(buf[i+10:]))
	dir := int64(binary.LittleEndian.Uint32(buf[i+12:]))
	offset := int64(binary.LittleEndian.Uint32(buf[i+16:]))
	if entries == 0xffff || dir == 0xffffffff || offset == 0xffffffff {
		// The real numbers are in the zip64 record, if there is one. Without one these stand as
		// they are, as the reader takes them.
		if e, d, ok := zip64Numbers(r, size-tail+int64(i)); ok {
			entries, dir = e, d
		}
	}
	if l.MaxEntries > 0 && entries > l.MaxEntries {
		return errZipTooLarge
	}
	if l.MaxDirectory > 0 && dir > l.MaxDirectory {
		return errZipTooLarge
	}
	return nil
}

// zipFindEnd finds the record the zip reader will use, in the same way: the last signature with room
// for a whole record after it. A comment is the file's own to fill, so a signature there too
// close to the end is a decoy, and one whose comment doesn't fit the reader refuses as well.
func zipFindEnd(b []byte) int {
	if len(b) < zipEndLen {
		return -1
	}
	i := bytes.LastIndex(b[:len(b)-zipEndLen+4], []byte("PK\x05\x06"))
	if i < 0 || i+zipEndLen+int(binary.LittleEndian.Uint16(b[i+20:])) > len(b) {
		return -1
	}
	return i
}

func zip64Numbers(r io.ReaderAt, endAt int64) (entries, dir int64, ok bool) {
	if endAt < zipLocatorLen {
		return 0, 0, false
	}
	loc := make([]byte, zipLocatorLen)
	if _, err := r.ReadAt(loc, endAt-zipLocatorLen); err != nil || !bytes.Equal(loc[:4], []byte("PK\x06\x07")) {
		return 0, 0, false
	}
	at := binary.LittleEndian.Uint64(loc[8:])
	if at > 1<<62 {
		return 0, 0, false
	}
	rec := make([]byte, zipEnd64Len)
	if _, err := r.ReadAt(rec, int64(at)); err != nil || !bytes.Equal(rec[:4], []byte("PK\x06\x06")) {
		return 0, 0, false
	}
	e, d := binary.LittleEndian.Uint64(rec[32:]), binary.LittleEndian.Uint64(rec[40:])
	if e > 1<<62 || d > 1<<62 {
		return 0, 0, false
	}
	return int64(e), int64(d), true
}

// zipCheckNames looks at what the directory holds, once it is read and before anything looks a
// name up. Open builds a tree of every folder of every name when first called.
func zipCheckNames(zr *zip.Reader, l zipLimits) error {
	folders := int64(0)
	for _, f := range zr.File {
		if l.MaxName > 0 && len(f.Name) > l.MaxName {
			return errZipTooLarge
		}
		folders += int64(strings.Count(f.Name, "/"))
		if l.MaxFolders > 0 && folders > l.MaxFolders {
			return errZipTooLarge
		}
	}
	return nil
}
