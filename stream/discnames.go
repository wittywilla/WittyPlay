// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"bufio"
	"errors"
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

// discFolder returns the file to give a core: rom itself when every name in its sheet is
// right, or a copy of the sheet in a folder under saveDir beside links to the real files.
func discFolder(rom, saveDir string) (string, error) {
	folder := filepath.Join(saveDir, discFolderName)
	_ = os.RemoveAll(folder) // whatever the last game left, if it was killed
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
