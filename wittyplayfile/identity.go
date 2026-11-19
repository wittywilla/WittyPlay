// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package wittyplayfile

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A game's file says which dump it was made for. Before it is used for a game, what it says is
// compared with the game's file: one made for another dump, another region or another revision
// is flagged and not applied, rather than putting wrong details, cheats or core options on a
// game. A file's name alone never decides it, since people rename their games.

// Verdict is what comparing a file's identity with a game's file found.
type Verdict string

const (
	// Same: a checksum agrees, and nothing differs.
	Same Verdict = "same"
	// Other: the size or a checksum differs. The file is for another dump: flag it, don't apply it.
	Other Verdict = "other"
	// Unknown: nothing could be compared (the file says no checksums, or none was worked out).
	// The file is applied.
	Unknown Verdict = "unknown"
)

// Facts are what is known of a game's file. Empty checksums weren't worked out.
type Facts struct {
	Name  string
	Size  int64
	CRC32 string
	MD5   string
	SHA1  string
	// MoreCRC32: other CRC32s the game is also known by (its file inside a ZIP, a header left out).
	MoreCRC32 []string
}

// Compare compares an identity with a game's file.
func Compare(id *Identity, f Facts) Verdict {
	if id == nil {
		return Unknown
	}
	if id.Size > 0 && f.Size > 0 && id.Size != f.Size && !anyCRC(id.CRC32, f.MoreCRC32) {
		return Other
	}
	agreed := false
	for _, h := range []struct {
		want, got string
		more      []string
	}{{id.CRC32, f.CRC32, f.MoreCRC32}, {id.MD5, f.MD5, nil}, {id.SHA1, f.SHA1, nil}} {
		if h.want == "" || h.got == "" && len(h.more) == 0 {
			continue
		}
		if strings.EqualFold(h.want, h.got) || anyCRC(h.want, h.more) {
			agreed = true
			continue
		}
		return Other
	}
	if agreed {
		return Same
	}
	return Unknown
}

func anyCRC(want string, more []string) bool {
	for _, m := range more {
		if want != "" && strings.EqualFold(want, m) {
			return true
		}
	}
	return false
}

// MaxHashed is the largest file Hash works checksums out for: cartridges and small discs. A
// bigger file is compared by its size, and by the CRC32 a scan already has.
const MaxHashed = 256 << 20

// Hash works out a file's size and the checksums wanted ("crc32", "md5", "sha1"), reading it once.
// A file over MaxHashed gets its size only.
func Hash(path string, want ...string) (Facts, error) {
	f, err := os.Open(path)
	if err != nil {
		return Facts{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Facts{}, err
	}
	if !st.Mode().IsRegular() {
		return Facts{}, fmt.Errorf("%s is not a file", filepath.Base(path))
	}
	out := Facts{Name: filepath.Base(path), Size: st.Size()}
	if st.Size() > MaxHashed || len(want) == 0 {
		return out, nil
	}
	hs := map[string]hash.Hash{}
	var ws []io.Writer
	for _, w := range want {
		var h hash.Hash
		switch w {
		case "crc32":
			h = crc32.NewIEEE()
		case "md5":
			h = md5.New()
		case "sha1":
			h = sha1.New()
		default:
			continue
		}
		hs[w] = h
		ws = append(ws, h)
	}
	if _, err := io.Copy(io.MultiWriter(ws...), io.LimitReader(f, MaxHashed)); err != nil {
		return Facts{}, err
	}
	for k, h := range hs {
		v := hex.EncodeToString(h.Sum(nil))
		switch k {
		case "crc32":
			out.CRC32 = v
		case "md5":
			out.MD5 = v
		case "sha1":
			out.SHA1 = v
		}
	}
	return out, nil
}

// Wanted lists the checksums an identity gives, for Hash.
func (id *Identity) Wanted() []string {
	var out []string
	if id == nil {
		return nil
	}
	if id.CRC32 != "" {
		out = append(out, "crc32")
	}
	if id.MD5 != "" {
		out = append(out, "md5")
	}
	if id.SHA1 != "" {
		out = append(out, "sha1")
	}
	return out
}

// IdentityOf is an identity for a game's file, to write into a new .wittyplay: its name, size,
// and every checksum when it is small enough to work them out.
func IdentityOf(path string) (*Identity, error) {
	f, err := Hash(path, "crc32", "md5", "sha1")
	if err != nil {
		return nil, err
	}
	return &Identity{FileName: f.Name, Size: f.Size, CRC32: f.CRC32, MD5: f.MD5, SHA1: f.SHA1}, nil
}
