// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 3DS game files. Most copies are encrypted, and the cores can only read them
// decrypted. Nothing here decrypts anything or goes looking for keys: this reads
// the plain headers that say what a file is, so that an encrypted one is refused
// with words a person can act on rather than a core's failure.

// ErrEncrypted3DS is why an encrypted 3DS file won't play.
var ErrEncrypted3DS = errors.New("This 3DS game file is encrypted: its program is scrambled, as it came from the console or the store. It can't be played here, and this server won't decrypt it")

// Azahar, like the 3DS, starts as the older model unless it's told otherwise. A game made
// for the New 3DS, whose header says so, won't run on that.
const (
	NewThreeDSOption = "citra_is_new_3ds"
	NewThreeDSValue  = "New 3DS"
)

// ThreeDSFile is what a 3DS file's headers say.
type ThreeDSFile struct {
	Encrypted bool
	// Program is where the game's main program is, for a package that wraps it. A
	// core can't open a package, but it can open that program as a file of its own.
	Program    int64
	ProgramLen int64
	Package    bool
	// NewOnly: this game only runs on a New 3DS, so a core has to be told to be one.
	NewOnly bool
	// Unflagged: its program was decrypted by a tool that left the NoCrypto flag unset. It
	// plays, but Azahar goes by the flag and calls it encrypted, so a core gets a copy of
	// the program with the flag set, made by CopyProgram.
	Unflagged bool
}

const (
	ncchFlags    = 0x188 // eight flag bytes
	ncchNoCrypto = 0x04  // in the last of them
	ncchFixedKey = 0x01  // in the last: a fixed key, for system titles
	ncchSeed     = 0x20  // in the last: a seed is part of the key
	ncchNew3DS   = 0x02  // in the fifth: the platform, set for a New 3DS
	mediaUnit    = 0x200
	ciaAlign     = 64
)

// Inspect3DS reads a 3DS file's headers. Files it has nothing to say about, such as
// homebrew programs, are simply not encrypted as far as it knows.
func Inspect3DS(path string) (ThreeDSFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return ThreeDSFile{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ThreeDSFile{}, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cia":
		return inspectCIA(f, st.Size())
	case ".3ds", ".cci":
		return inspectCCI(f, st.Size())
	case ".cxi", ".app":
		return inspectNCCH(f, 0, st.Size())
	}
	return ThreeDSFile{}, nil
}

func readAt(f io.ReaderAt, off int64, n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := f.ReadAt(b, off); err != nil {
		return nil, fmt.Errorf("this 3DS file is cut short or isn't what its name says: %w", err)
	}
	return b, nil
}

// inspectNCCH says what one program's header says.
func inspectNCCH(f io.ReaderAt, off, size int64) (ThreeDSFile, error) {
	enc, err := ncchEncrypted(f, off, size)
	if err != nil {
		return ThreeDSFile{}, err
	}
	out := ThreeDSFile{Encrypted: enc, Program: off, ProgramLen: size}
	if flags, err := readAt(f, off+ncchFlags, 8); err == nil {
		out.NewOnly = flags[4]&ncchNew3DS != 0
		out.Unflagged = !enc && flags[7]&ncchNoCrypto == 0
	}
	return out, nil
}

// ncchEncrypted looks at one program's header. Its magic number sits outside what
// is encrypted within a program, so if it's missing, the whole program is scrambled
// by the package around it. A flag saying "no encryption" counts as plain, and so does
// an extended header that reads plainly: some tools decrypt a program without setting
// the flag, and its jump id then matches the program's own id. That's the test 3DS
// emulators make themselves (Citra's "marked as encrypted but with decrypted exheader").
func ncchEncrypted(f io.ReaderAt, off, size int64) (bool, error) {
	if size < ncchFlags+8 {
		return false, errors.New("this 3DS file is too short to hold a game")
	}
	head, err := readAt(f, off+0x100, 4)
	if err != nil {
		return false, err
	}
	if string(head) != "NCCH" {
		return true, nil
	}
	flags, err := readAt(f, off+ncchFlags, 8)
	if err != nil {
		return false, err
	}
	if flags[7]&ncchNoCrypto != 0 {
		return false, nil
	}
	return !plainExheader(f, off), nil
}

const (
	ncchExhSize = 0x180
	ncchProgID  = 0x118
	ncchExhAt   = 0x200
	exhJumpID   = 0x1C8 // in the extended header's system info
)

// plainExheader says a program's extended header reads as it stands: its jump id matches
// the program id (the low half, as emulators allow for merged updates). Encrypted bytes
// match by chance one time in four billion; a zero id never counts.
func plainExheader(f io.ReaderAt, off int64) bool {
	size, err1 := readAt(f, off+ncchExhSize, 4)
	prog, err2 := readAt(f, off+ncchProgID, 8)
	jump, err3 := readAt(f, off+ncchExhAt+exhJumpID, 8)
	if err1 != nil || err2 != nil || err3 != nil || binary.LittleEndian.Uint32(size) == 0 {
		return false
	}
	p := binary.LittleEndian.Uint32(prog)
	return p != 0 && p == binary.LittleEndian.Uint32(jump)
}

func inspectCCI(f io.ReaderAt, size int64) (ThreeDSFile, error) {
	head, err := readAt(f, 0x100, 4)
	if err != nil {
		return ThreeDSFile{}, err
	}
	if string(head) != "NCSD" {
		return ThreeDSFile{}, errors.New("this file doesn't look like a 3DS game card image")
	}
	table, err := readAt(f, 0x120, 8)
	if err != nil {
		return ThreeDSFile{}, err
	}
	off := int64(binary.LittleEndian.Uint32(table)) * mediaUnit
	n := int64(binary.LittleEndian.Uint32(table[4:])) * mediaUnit
	if off <= 0 || n <= 0 || off+n > size {
		return ThreeDSFile{}, errors.New("this 3DS game card image has no usable game inside")
	}
	return inspectNCCH(f, off, n)
}

// setNoCrypto marks a copied program as unencrypted: the crypto method (flags[3]) back to
// none, and in flags[7] the NoCrypto bit on and the fixed-key and seed bits off.
func setNoCrypto(f *os.File) error {
	flags := make([]byte, 8)
	if _, err := f.ReadAt(flags, ncchFlags); err != nil {
		return err
	}
	flags[3] = 0
	flags[7] = flags[7]&^(ncchFixedKey|ncchSeed) | ncchNoCrypto
	_, err := f.WriteAt(flags, ncchFlags)
	return err
}

func alignCIA(n int64) int64 { return (n + ciaAlign - 1) &^ (ciaAlign - 1) }

// inspectCIA reads an installable package: a header giving the size of each part,
// then certificates, a ticket, a table of contents, and the contents themselves.
func inspectCIA(f io.ReaderAt, size int64) (ThreeDSFile, error) {
	h, err := readAt(f, 0, 0x20)
	if err != nil {
		return ThreeDSFile{}, err
	}
	le := binary.LittleEndian
	headLen := int64(le.Uint32(h))
	certLen, tikLen, tmdLen := int64(le.Uint32(h[8:])), int64(le.Uint32(h[12:])), int64(le.Uint32(h[16:]))
	contentLen := int64(le.Uint64(h[24:]))
	if headLen != 0x2020 || tmdLen <= 0 || contentLen <= 0 || certLen < 0 || tikLen < 0 {
		return ThreeDSFile{}, errors.New("this file doesn't look like a 3DS install package")
	}
	tmd := alignCIA(alignCIA(alignCIA(headLen)+certLen) + tikLen)
	contents := alignCIA(tmd + tmdLen)
	if contents >= size || contentLen > size-contents {
		return ThreeDSFile{}, errors.New("this 3DS install package is cut short")
	}
	// The table of contents starts with a signature, whose type sets its length.
	sig, err := readAt(f, tmd, 4)
	if err != nil {
		return ThreeDSFile{}, err
	}
	var skip int64
	switch binary.BigEndian.Uint32(sig) {
	case 0x10000, 0x10003:
		skip = 4 + 0x200 + 0x3C
	case 0x10001, 0x10004:
		skip = 4 + 0x100 + 0x3C
	case 0x10002, 0x10005:
		skip = 4 + 0x3C + 0x40
	default:
		return ThreeDSFile{}, errors.New("this 3DS install package has a table of contents that can't be read")
	}
	// The header is 0xC4 bytes, then 64 info records of 0x24, then a record per content.
	first := tmd + skip + 0xC4 + 64*0x24
	if first+0x30 > tmd+tmdLen {
		return ThreeDSFile{}, errors.New("this 3DS install package has a table of contents that can't be read")
	}
	rec, err := readAt(f, first, 0x30)
	if err != nil {
		return ThreeDSFile{}, err
	}
	n := int64(binary.BigEndian.Uint64(rec[8:]))
	if n <= 0 || n > contentLen {
		return ThreeDSFile{}, errors.New("this 3DS install package has a table of contents that can't be read")
	}
	// A package also marks each content as encrypted or not. The bytes are what count: NCCH
	// where it belongs can't happen by accident in something scrambled, and some tools
	// that took a package's encryption off left the mark on.
	inner, err := inspectNCCH(f, contents, n)
	if err != nil && binary.BigEndian.Uint16(rec[6:])&1 != 0 {
		// Too short to read, and marked encrypted: there's no more to find out.
		inner, err = ThreeDSFile{Encrypted: true}, nil
	}
	inner.Package, inner.Program, inner.ProgramLen = true, contents, n
	return inner, err
}

// NeedsCopy says a core can't open this file as it is, but can open its program copied out.
func (t ThreeDSFile) NeedsCopy() bool { return !t.Encrypted && (t.Package || t.Unflagged) }

// CopyProgram writes the program out of an unencrypted package, or out of a file decrypted
// without its NoCrypto flag, to a file of its own. It's a plain copy of bytes that are
// already readable; for an unflagged program, its flags then say so, as GodMode9 sets them
// when it decrypts: no crypto, and no key-selection bits left over.
func (t ThreeDSFile) CopyProgram(from, to string) error {
	if !t.NeedsCopy() || t.ProgramLen <= 0 {
		return errors.New("there's no unencrypted program to copy out of this file")
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.NewSectionReader(in, t.Program, t.ProgramLen))
	if err == nil && t.Unflagged {
		err = setNoCrypto(out)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
