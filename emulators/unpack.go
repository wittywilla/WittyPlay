package emulators

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// unpack opens a download that has already passed its hash check into dir. Nothing may land
// outside dir: names with "..", absolute names and links that lead out are refused, nothing is
// written through a link, and the whole is held to limit bytes. Files get their modes as
// they're made, never afterwards, because a module's sandbox may refuse chmod.
func unpack(kind, file string, size int64, dir, entry string, limit int64) error {
	switch kind {
	case ArchiveAppImage:
		return unpackAppImage(file, dir, entry, limit)
	case ArchiveZip:
		return unpackZip(file, size, dir, entry, limit)
	case ArchiveTarGz:
		f, err := os.Open(file)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		return unpackTar(tar.NewReader(gz), dir, entry, limit)
	case ArchiveTarXz:
		return ErrXZ
	}
	return fmt.Errorf("archive %q isn't one this reads", kind)
}

// unpacker keeps the rules for one archive.
type unpacker struct {
	dir, entry string
	limit      int64
	written    int64
	files      int
	links      map[string]bool // names that are links: nothing goes through one
}

func newUnpacker(dir, entry string, limit int64) *unpacker {
	return &unpacker{dir: dir, entry: path.Clean(filepath.ToSlash(entry)), limit: limit, links: map[string]bool{}}
}

// clean checks an archive's name for a file and returns it in slash form.
func (u *unpacker) clean(name string) (string, error) {
	name = strings.TrimPrefix(name, "./")
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("the archive names a path outside its folder: %q", name)
	}
	c := path.Clean(name)
	if c == "." || !filepath.IsLocal(filepath.FromSlash(c)) {
		return "", fmt.Errorf("the archive names a path outside its folder: %q", name)
	}
	for _, part := range strings.Split(c, "/") {
		if part == ".." {
			return "", fmt.Errorf("the archive names a path outside its folder: %q", name)
		}
	}
	// Nothing may be written through a link made earlier in the same archive.
	for p := path.Dir(c); p != "."; p = path.Dir(p) {
		if u.links[p] {
			return "", fmt.Errorf("the archive writes through a link: %q", name)
		}
	}
	if u.links[c] {
		return "", fmt.Errorf("the archive writes over a link: %q", name)
	}
	u.files++
	if u.files > maxFiles {
		return "", errors.New("the archive holds too many files")
	}
	return c, nil
}

func (u *unpacker) mkdir(name string) error {
	c, err := u.clean(name)
	if err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(u.dir, filepath.FromSlash(c)), 0o755)
}

func (u *unpacker) file(name string, mode os.FileMode, r io.Reader) error {
	c, err := u.clean(name)
	if err != nil {
		return err
	}
	out := filepath.Join(u.dir, filepath.FromSlash(c))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 || c == u.entry {
		perm = 0o755 // never setuid, setgid or sticky, whatever the archive says
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	room := u.limit - u.written
	n, err := io.Copy(f, io.LimitReader(r, room+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	u.written += n
	if err != nil {
		return err
	}
	if u.written > u.limit {
		return ErrTooLarge
	}
	return nil
}

// link makes a symbolic link whose target stays inside the folder, judged without following
// anything: no absolute target, and no ".." anywhere in it.
func (u *unpacker) link(name, target string) error {
	c, err := u.clean(name)
	if err != nil {
		return err
	}
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") || filepath.IsAbs(target) {
		return fmt.Errorf("the archive has a link that leads out: %q -> %q", name, target)
	}
	for _, part := range strings.Split(target, "/") {
		if part == ".." {
			return fmt.Errorf("the archive has a link that leads out: %q -> %q", name, target)
		}
	}
	if !filepath.IsLocal(filepath.FromSlash(path.Join(path.Dir(c), target))) {
		return fmt.Errorf("the archive has a link that leads out: %q -> %q", name, target)
	}
	out := filepath.Join(u.dir, filepath.FromSlash(c))
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(target, out); err != nil {
		return err
	}
	u.links[c] = true
	return nil
}

func unpackAppImage(file, dir, entry string, limit int64) error {
	u := newUnpacker(dir, entry, limit)
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return u.file(entry, 0o755, f)
}

func unpackZip(file string, size int64, dir, entry string, limit int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	var total uint64
	for _, zf := range zr.File {
		total += zf.UncompressedSize64
		if total > uint64(limit) {
			return ErrTooLarge
		}
	}
	u := newUnpacker(dir, entry, limit)
	for _, zf := range zr.File {
		mode := zf.Mode()
		switch {
		case strings.HasSuffix(zf.Name, "/") || mode.IsDir():
			if err := u.mkdir(zf.Name); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			if err := u.link(zf.Name, string(target)); err != nil {
				return err
			}
		case mode.IsRegular():
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			err = u.file(zf.Name, mode, rc)
			rc.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the archive holds something that isn't a file: %q", zf.Name)
		}
	}
	return nil
}

func unpackTar(tr *tar.Reader, dir, entry string, limit int64) error {
	u := newUnpacker(dir, entry, limit)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err = u.mkdir(h.Name)
		case tar.TypeReg:
			err = u.file(h.Name, os.FileMode(h.Mode), tr)
		case tar.TypeSymlink:
			err = u.link(h.Name, h.Linkname)
		case tar.TypeXGlobalHeader:
			continue
		default:
			// Hard links, devices, fifos: an emulator needs none of them.
			err = fmt.Errorf("the archive holds something that isn't a file: %q", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

// markEntry checks the program is there, a plain file, and executable. Its mode was given as it
// was made, so nothing here changes it.
func markEntry(dir, entry string) error {
	p := filepath.Join(dir, filepath.FromSlash(entry))
	st, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("the download doesn't hold the program %q", entry)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("the program %q isn't a plain file", entry)
	}
	if st.Mode()&0o100 == 0 && os.PathSeparator == '/' {
		return fmt.Errorf("the program %q isn't executable", entry)
	}
	return nil
}
