package stream

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Disc images come as a sheet (.cue, .gdi, or .m3u for several discs) naming the files that
// hold the data, and those names were mostly written on systems where capitals don't matter.
// "GAME.BIN" in a sheet beside "Game.bin" plays on Windows, and on Linux most cores can't open
// it. The library belongs to its owner and stays as it is, so a game like that is given a
// folder of its own with links under the names its sheet uses.

// maxSheet bounds how much of a sheet is read. Real ones are a few kilobytes.
const maxSheet = 1 << 20

const discFolderName = ".disc-names"

// DiscFile is the file a game session gives a core for rom, preparing it in saveDir as one
// does: for a tool that loads games itself and should see them as a server would.
func DiscFile(rom, saveDir string) (string, error) { return discFolder(rom, saveDir) }

// discFolder returns the file to give a core: rom itself when every name in its sheet is
// right, or a copy of the sheet in a folder under saveDir beside links to the real files.
func discFolder(rom, saveDir string) (string, error) {
	folder := filepath.Join(saveDir, discFolderName)
	_ = os.RemoveAll(folder) // whatever the last game left, if it was killed
	if sheet, err := rawDiscSheet(rom, folder); err != nil || sheet != "" {
		if err != nil {
			return rom, err
		}
		return sheet, nil
	}
	links := map[string]string{}
	var sheets []string
	if err := collectNames(rom, 0, links, &sheets); err != nil {
		return rom, err
	}
	if len(links) == 0 {
		return rom, nil
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return rom, err
	}
	for _, sheet := range sheets {
		if err := copySheet(sheet, filepath.Join(folder, filepath.Base(sheet))); err != nil {
			return rom, err
		}
	}
	for name, real := range links {
		if err := os.Symlink(real, filepath.Join(folder, name)); err != nil && !errors.Is(err, os.ErrExist) {
			return rom, err
		}
	}
	return filepath.Join(folder, filepath.Base(rom)), nil
}

// rawDiscSheet gives a disc kept as a lone .bin of raw CD sectors, with no sheet, a sheet of
// its own. Some cores read such a file as if it held only data, and Play! crashes on one (#31);
// a sheet naming it as one track of raw sectors is what they need. The sheet goes in the same
// folder as the ones above, beside a link to the file, or naming it in full where no link can
// be made (Windows, without the right to make one). Anything else gives "".
func rawDiscSheet(rom, folder string) (string, error) {
	if !strings.EqualFold(filepath.Ext(rom), ".bin") {
		return "", nil
	}
	mode := rawSectorMode(rom)
	if mode == 0 {
		return "", nil
	}
	base := strings.TrimSuffix(filepath.Base(rom), filepath.Ext(rom))
	// One that came with a sheet is that sheet's to describe.
	if entries, err := os.ReadDir(filepath.Dir(rom)); err == nil {
		for _, e := range entries {
			if strings.EqualFold(e.Name(), base+".cue") {
				return "", nil
			}
		}
	}
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return "", err
	}
	name := filepath.Base(rom)
	if err := os.Symlink(rom, filepath.Join(folder, name)); err != nil && !errors.Is(err, os.ErrExist) {
		if strings.ContainsRune(rom, '"') {
			return "", nil // no way to name it in a sheet; the core gets it as it is
		}
		name = rom
	}
	sheet := filepath.Join(folder, base+".cue")
	body := fmt.Sprintf("FILE \"%s\" BINARY\r\n  TRACK 01 MODE%d/2352\r\n    INDEX 01 00:00:00\r\n", name, mode)
	if err := os.WriteFile(sheet, []byte(body), 0o600); err != nil {
		return "", err
	}
	return sheet, nil
}

// rawSectorMode says whether a file starts with a raw CD sector, by its sync pattern, and
// which mode it is in: 1 or 2, or 0 for anything else, such as a cartridge kept as a .bin.
func rawSectorMode(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	var b [16]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return 0
	}
	sync := []byte{0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0}
	if !bytes.Equal(b[:12], sync) || b[15] != 1 && b[15] != 2 {
		return 0
	}
	return int(b[15])
}

// collectNames finds the names a sheet uses that don't exist as written but do in other
// capitals, and every sheet that has to move for them to be found. A name that doesn't exist
// in any capitals is left for the core to report.
func collectNames(sheet string, depth int, links map[string]string, sheets *[]string) error {
	if depth > 2 {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(sheet))
	if ext != ".cue" && ext != ".gdi" && ext != ".m3u" {
		return nil
	}
	names, err := sheetNames(sheet, ext)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // whoever opens it says so
		}
		return err
	}
	dir := filepath.Dir(sheet)
	var entries []os.DirEntry
	found := len(links)
	var moved []string
	for _, name := range names {
		// A name with a folder in it is left alone: it's the sheet's own layout.
		if name != filepath.Base(name) || name == "." || name == ".." {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			if ext == ".m3u" {
				moved = append(moved, path)
			}
			continue
		}
		if entries == nil {
			if entries, err = os.ReadDir(dir); err != nil {
				return nil
			}
		}
		for _, e := range entries {
			if strings.EqualFold(e.Name(), name) {
				links[name] = filepath.Join(dir, e.Name())
				if ext == ".m3u" {
					moved = append(moved, links[name])
				}
				break
			}
		}
	}
	for _, m := range moved {
		before := len(links)
		if err := collectNames(m, depth+1, links, sheets); err != nil {
			return err
		}
		if len(links) == before {
			// This disc needs nothing changed, but it must still be found from the new folder.
			links[filepath.Base(m)] = m
		}
	}
	if len(links) > found {
		*sheets = append(*sheets, sheet)
	}
	return nil
}

// sheetNames reads the file names out of a sheet.
func sheetNames(sheet, ext string) ([]string, error) {
	f, err := os.Open(sheet)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var names []string
	sc := bufio.NewScanner(io.LimitReader(f, maxSheet))
	sc.Buffer(make([]byte, 0, 4096), maxSheet)
	first := true
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		if line == "" {
			first = false
			continue
		}
		switch ext {
		case ".m3u":
			if !strings.HasPrefix(line, "#") {
				names = append(names, line)
			}
		case ".cue":
			if len(line) > 5 && strings.EqualFold(line[:5], "FILE ") {
				names = append(names, firstField(line[5:]))
			}
		case ".gdi":
			if !first { // the first line says how many tracks there are
				if f := strings.Fields(line); len(f) >= 6 {
					name := strings.Join(f[4:len(f)-1], " ")
					names = append(names, strings.Trim(name, `"`))
				}
			}
		}
		first = false
	}
	return names, sc.Err()
}

// firstField reads a name that may be in quotes, from a line that goes on after it.
func firstField(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, `"`) {
		if end := strings.IndexByte(s[1:], '"'); end >= 0 {
			return s[1 : end+1]
		}
		return strings.Trim(s, `"`)
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

func copySheet(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(in, maxSheet))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
