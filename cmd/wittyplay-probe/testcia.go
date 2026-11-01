package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"unicode/utf16"

	"github.com/wittywilla/wittyplay"
)

// testcia writes a 3DS program and an install package holding it, made here from nothing but
// this file: a few hand-assembled instructions that draw on the top screen, in a new shade
// every frame, so that time passing can be seen. There's no key in it and no encryption, and
// its NoCrypto flag says so. It exists to prove that a truly plain package plays from end to
// end, on any machine, without needing anyone's game or anyone's tools.
func testcia(args []string) error {
	fs := flag.NewFlagSet("test-cia", flag.ExitOnError)
	out := fs.String("o", "", "the package to write, with a .cia name")
	marked := fs.Bool("still-marked", false, "write the package as one that had its encryption taken off and kept its ticket: contents marked encrypted, a title key field filled in")
	_ = fs.Parse(args)
	if *out == "" {
		return errors.New("-o is needed")
	}
	program := buildNCCH(testProgramID)
	return os.WriteFile(*out, buildCIA(program, testProgramID, *marked), 0o644)
}

const (
	testProgramID = 0x000400000FF3F000
	pageSize      = 0x1000
	textAddress   = 0x00100000
)

// armAsm assembles the little that testProgram needs. An instruction is the 32-bit word
// the ARM architecture manual gives for it, with its condition "always".
type armAsm struct {
	words  []uint32
	labels map[string]int
	jumps  map[int]string // branch at this word goes to this label
	loads  map[int]uint32 // load from the literal pool at this word
}

const (
	r0 = iota
	r1
	_
	_
	_
	_
	_
	r7
	r8
	r9
	r10
)

func (a *armAsm) emit(w uint32)                 { a.words = append(a.words, w) }
func (a *armAsm) label(name string)             { a.labels[name] = len(a.words) }
func (a *armAsm) svc(n uint32)                  { a.emit(0xEF000000 | n) }
func (a *armAsm) movImm(d, imm uint32)          { a.emit(0xE3A00000 | d<<12 | imm) } // up to 255
func (a *armAsm) addImm(d, n, imm uint32)       { a.emit(0xE2800000 | n<<16 | d<<12 | imm) }
func (a *armAsm) andImm(d, n, imm uint32)       { a.emit(0xE2000000 | n<<16 | d<<12 | imm) }
func (a *armAsm) subsImm(d, n, imm uint32)      { a.emit(0xE2500000 | n<<16 | d<<12 | imm) }
func (a *armAsm) orrShift(d, n, m, left uint32) { a.emit(0xE1800000 | n<<16 | d<<12 | left<<7 | m) }
func (a *armAsm) strNext(d, base uint32)        { a.emit(0xE4800004 | base<<16 | d<<12) } // str d,[base],#4
func (a *armAsm) constant(d, v uint32) {
	a.loads[len(a.words)] = v
	a.emit(0xE59F0000 | d<<12) // ldr d,[pc,#later]
}
func (a *armAsm) branch(cond uint32, to string) {
	a.jumps[len(a.words)] = to
	a.emit(cond<<28&0xF0000000 | 0x0A000000)
}

// finish lays the constants out after the code and fills in what refers to them.
func (a *armAsm) finish() []byte {
	var pool []uint32
	for i, v := range a.loads {
		at := len(a.words) + len(pool)
		pool = append(pool, v)
		a.words[i] |= uint32((at-i-2)*4) & 0xFFF
	}
	for i, to := range a.jumps {
		a.words[i] |= uint32(a.labels[to]-i-2) & 0xFFFFFF
	}
	out := make([]byte, 0, (len(a.words)+len(pool))*4)
	for _, w := range append(a.words, pool...) {
		out = binary.LittleEndian.AppendUint32(out, w)
	}
	return out
}

const (
	svcSleepThread = 0x0A

	screenBytes = 240 * 400 * 3 // top screen, three bytes a pixel
	vramVirtual = 0x1F000000
	// Where the top screen's picture is until a program says otherwise, in video memory.
	topScreen = vramVirtual + 0x1E6000
)

// testProgram does this forever: fills the top screen with a shade that goes up by one, and
// sleeps for a frame. It asks nothing of the system's services.
func testProgram() []byte {
	a := &armAsm{labels: map[string]int{}, jumps: map[int]string{}, loads: map[int]uint32{}}
	a.movImm(r7, 0)
	a.label("frame")
	a.addImm(r7, r7, 1)
	a.andImm(r10, r7, 0xFF)
	a.orrShift(r10, r10, r10, 8)
	a.orrShift(r10, r10, r10, 16)
	a.constant(r8, topScreen)
	a.constant(r9, screenBytes/4)
	a.label("fill")
	a.strNext(r10, r8)
	a.subsImm(r9, r9, 1)
	a.branch(1, "fill") // not yet zero
	a.constant(r0, 16666667)
	a.movImm(r1, 0)
	a.svc(svcSleepThread)
	a.branch(14, "frame") // always
	return a.finish()
}

// buildNCCH lays out a program the way a console expects to find one: a header saying there's
// no encryption, an extended header, and a file system holding the code.
func buildNCCH(programID uint64) []byte {
	const (
		headerSize = 0x200
		exSize     = 0x800
		exefsHead  = 0x200
	)
	roAddress := uint32(textAddress + pageSize)
	dataAddress := roAddress + pageSize
	text := testProgram()
	if len(text) > pageSize {
		panic("the test program has outgrown its page")
	}
	code := make([]byte, 3*pageSize)
	copy(code, text)

	le := binary.LittleEndian
	ex := make([]byte, exSize)
	copy(ex, "WTEST")
	ex[0xD] = 0x02 // an SD card title, with its code not compressed
	segment := func(at int, addr uint32) {
		le.PutUint32(ex[at:], addr)
		le.PutUint32(ex[at+4:], 1)
		le.PutUint32(ex[at+8:], pageSize)
	}
	segment(0x10, textAddress)
	le.PutUint32(ex[0x1C:], 0x4000) // stack
	segment(0x20, roAddress)
	segment(0x30, dataAddress)
	le.PutUint64(ex[0x1C8:], programID) // jump id
	le.PutUint64(ex[0x200:], programID)
	le.PutUint32(ex[0x208:], 2) // core version
	ex[0x20F] = 0x30            // priority
	// Every system call, then the video memory as a mapped range.
	caps := ex[0x370:]
	for i := range 8 {
		le.PutUint32(caps[i*4:], 0xF0000000|uint32(i)<<24|0xFFFFFF)
	}
	le.PutUint32(caps[8*4:], 0xFF800000|vramVirtual>>12)
	le.PutUint32(caps[9*4:], 0xFF800000|(vramVirtual+0x600000)>>12)
	for i := 10; i < 28; i++ {
		le.PutUint32(caps[i*4:], 0xFFFFFFFF)
	}
	copy(ex[0x400+0x200:], ex[0x200:0x400]) // the access descriptor repeats what's granted

	exefs := make([]byte, exefsHead+len(code))
	copy(exefs, ".code")
	le.PutUint32(exefs[12:], uint32(len(code)))
	copy(exefs[exefsHead:], code)
	sum := sha256.Sum256(code)
	copy(exefs[exefsHead-32:], sum[:])

	total := headerSize + exSize + len(exefs)
	for total%0x200 != 0 {
		total++
	}
	b := make([]byte, total)
	copy(b[0x100:], "NCCH")
	le.PutUint32(b[0x104:], uint32(total/0x200))
	le.PutUint64(b[0x108:], programID)
	copy(b[0x110:], "00")
	le.PutUint16(b[0x112:], 2)
	le.PutUint64(b[0x118:], programID)
	copy(b[0x150:], "CTR-P-WTST")
	le.PutUint32(b[0x180:], 0x400)
	b[0x188+4] = 1           // made for the original 3DS
	b[0x188+5] = 0x03        // a program
	b[0x188+7] = 0x02 | 0x04 // no file system of its own, and NoCrypto
	le.PutUint32(b[0x1A0:], (headerSize+exSize)/0x200)
	le.PutUint32(b[0x1A4:], uint32(len(exefs)/0x200))
	le.PutUint32(b[0x1A8:], exefsHead/0x200)
	copy(b[headerSize:], ex)
	copy(b[headerSize+exSize:], exefs)
	return b
}

// buildCIA wraps a program in an install package, as real ones are laid out. With marked, it's
// as a package is after a tool has taken its encryption off and changed nothing else: the
// table of contents still marks the program encrypted, and the ticket still has a title key.
// That field here is random bytes: it's a key to nothing.
func buildCIA(program []byte, programID uint64, marked bool) []byte {
	const (
		headLen, certLen, tikLen = 0x2020, 0xA00, 0x350
		rsaSig                   = 0x10004
		sigSkip                  = 0x140
		tmdLen                   = sigSkip + 0xC4 + 64*0x24 + 0x30
		metaLen                  = 0x400 + 0x36C0
	)
	le, be := binary.LittleEndian, binary.BigEndian
	pad := func(b []byte) []byte {
		for len(b)%64 != 0 {
			b = append(b, 0)
		}
		return b
	}
	head := make([]byte, headLen)
	le.PutUint32(head, headLen)
	le.PutUint32(head[8:], certLen)
	le.PutUint32(head[12:], tikLen)
	le.PutUint32(head[16:], tmdLen)
	le.PutUint32(head[20:], metaLen)
	le.PutUint64(head[24:], uint64(len(program)))
	head[0x20] = 0x80 // content 0 is here

	tik := make([]byte, tikLen)
	be.PutUint32(tik, rsaSig)
	copy(tik[sigSkip:], "Root-CA00000003-XS0000000c")
	if marked {
		_, _ = rand.Read(tik[sigSkip+0x7F : sigSkip+0x8F])
	}
	be.PutUint64(tik[sigSkip+0x9C:], programID)

	tmd := make([]byte, tmdLen)
	be.PutUint32(tmd, rsaSig)
	copy(tmd[sigSkip:], "Root-CA00000003-CP0000000b")
	be.PutUint64(tmd[sigSkip+0x4C:], programID)
	be.PutUint16(tmd[sigSkip+0x9E:], 1) // one content
	rec := tmd[sigSkip+0xC4+64*0x24:]
	be.PutUint32(rec, 0)
	be.PutUint16(rec[4:], 0)
	if marked {
		be.PutUint16(rec[6:], 1)
	}
	be.PutUint64(rec[8:], uint64(len(program)))
	sum := sha256.Sum256(program)
	copy(rec[16:], sum[:])

	meta := make([]byte, metaLen)
	smdh := meta[0x400:]
	copy(smdh, "SMDH")
	name := utf16.Encode([]rune("WittyPlay 3DS test"))
	for lang := range 16 {
		for i, c := range name {
			le.PutUint16(smdh[8+lang*0x200+i*2:], c)
		}
		for i, c := range name {
			le.PutUint16(smdh[8+lang*0x200+0x80+i*2:], c)
		}
	}

	out := pad(head)
	out = pad(append(out, make([]byte, certLen)...))
	out = pad(append(out, tik...))
	out = pad(append(out, tmd...))
	out = pad(append(out, program...))
	return append(out, meta...)
}

// ciaInfo says what a 3DS install package's own bytes say about whether it's encrypted, with
// each byte's place in the file, for someone to check for themselves. It reads and prints; it
// never decrypts anything, and it doesn't print a whole title key, which is no use to anyone
// reading this. With -hash it also reads every content through.
func ciaInfo(args []string) error {
	fs := flag.NewFlagSet("cia-info", flag.ExitOnError)
	hash := fs.Bool("hash", false, "check each content against the checksum in the table of contents, which covers its plain bytes")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: cia-info [-hash] file.cia")
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	read := func(at int64, n int) []byte {
		b := make([]byte, n)
		_, _ = f.ReadAt(b, at)
		return b
	}
	le, be := binary.LittleEndian, binary.BigEndian
	h := read(0, 0x20)
	headLen, certLen, tikLen, tmdLen := int64(le.Uint32(h)), int64(le.Uint32(h[8:])), int64(le.Uint32(h[12:])), int64(le.Uint32(h[16:]))
	metaLen, contentLen := int64(le.Uint32(h[20:])), int64(le.Uint64(h[24:]))
	if headLen != 0x2020 {
		return errors.New("this isn't a 3DS install package: its header length isn't 0x2020")
	}
	align := func(n int64) int64 { return (n + 63) &^ 63 }
	tik := align(align(headLen) + certLen)
	tmd := align(tik + tikLen)
	contents := align(tmd + tmdLen)
	fmt.Printf("File size %d. Header at 0: certificates %d, ticket %d, table of contents %d, meta %d, contents %d.\n", st.Size(), certLen, tikLen, tmdLen, metaLen, contentLen)
	if tikLen > 0 {
		sig := sigSkipOf(be.Uint32(read(tik, 4)))
		b := read(tik+sig, 0x100)
		fmt.Printf("Ticket at %#x: issuer %q. Title key field at %#x starts %x and is %s; common key index at %#x is %d.\n", tik, cstr(b[:0x40]), tik+sig+0x7F, b[0x7F:0x83], zeroWord(b[0x7F:0x8F]), tik+sig+0xB1, b[0xB1])
		fmt.Println("  A title key here is itself encrypted. A package keeps its ticket whether or not its contents are still encrypted, so this says nothing alone.")
	}
	sig := sigSkipOf(be.Uint32(read(tmd, 4)))
	th := read(tmd+sig, 0xC4)
	count := int(be.Uint16(th[0x9E:]))
	fmt.Printf("Table of contents at %#x: title %x, version %d, %d contents.\n", tmd, th[0x4C:0x54], be.Uint16(th[0x9C:]), count)
	at := contents
	for i := range min(count, 64) {
		recAt := tmd + sig + 0xC4 + 64*0x24 + int64(i)*0x30
		rec := read(recAt, 0x30)
		flags, size := be.Uint16(rec[6:]), int64(be.Uint64(rec[8:]))
		magic, ncch := read(at+0x100, 4), read(at+0x188, 8)
		fmt.Printf("Content %d, id %d: mark at %#x is %#04x (bit 0 is \"encrypted\"), %d bytes, at %#x.\n", be.Uint16(rec[4:]), be.Uint32(rec), recAt+6, flags, size, at)
		fmt.Printf("  Bytes at %#x, where \"NCCH\" belongs: % x (%s).\n", at+0x100, magic, magicWord(magic))
		if string(magic) == "NCCH" {
			fmt.Printf("  NCCH flags at %#x: % x. Crypto method (byte 3) %#02x, platform (byte 4) %#02x; byte 7 %#02x: FixedCryptoKey %v, NoCrypto %v, SeedCrypto %v.\n", at+0x188, ncch, ncch[3], ncch[4], ncch[7], ncch[7]&1 != 0, ncch[7]&4 != 0, ncch[7]&0x20 != 0)
		} else {
			fmt.Printf("  So the flags at %#x are scrambled too, and mean nothing until the package's own layer is taken off.\n", at+0x188)
		}
		if *hash {
			s := sha256.New()
			if _, err := io.Copy(s, io.NewSectionReader(f, at, size)); err != nil {
				return err
			}
			got := s.Sum(nil)
			same := "these match, so these are the plain bytes"
			if string(got) != string(rec[16:48]) {
				same = "these differ, so these aren't the plain bytes"
			}
			fmt.Printf("  Checksum in the table of contents %x..., of the bytes in this file %x...: %s.\n", rec[16:24], got[:8], same)
		}
		at += size
	}
	t, err := wittyplay.Inspect3DS(fs.Arg(0))
	if err != nil {
		return err
	}
	if t.Encrypted {
		fmt.Println("Verdict: encrypted. It's refused, and nothing here can change that.")
	} else {
		fmt.Println("Verdict: not encrypted. It imports and plays.")
	}
	return nil
}

func sigSkipOf(kind uint32) int64 {
	switch kind {
	case 0x10000, 0x10003:
		return 4 + 0x200 + 0x3C
	case 0x10001, 0x10004:
		return 4 + 0x100 + 0x3C
	}
	return 4 + 0x3C + 0x40
}

func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

func zeroWord(b []byte) string {
	for _, c := range b {
		if c != 0 {
			return "filled in"
		}
	}
	return "all zero"
}

func magicWord(b []byte) string {
	if string(b) == "NCCH" {
		return "there it is, readable"
	}
	return "not it, so this content is scrambled at the package's layer"
}
