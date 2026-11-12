// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package emulators

import (
	"archive/tar"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/ulikunitz/xz"
)

// The archive kinds the projects use for their Mac and Windows downloads: PCSX2's Mac build is a
// .tar.xz, PCSX2, Dolphin and RPCS3 ship .7z for Windows, Dolphin and Cemu a .dmg for Macs. Each
// is opened under the same rules as a zip (newUnpacker): nothing outside dir, no writing through
// links, and a limit on the whole.

func unpackTarXz(file, dir, entry string, limit int64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	xr, err := xz.NewReader(f)
	if err != nil {
		return err
	}
	return unpackTar(tar.NewReader(xr), dir, entry, limit)
}

func unpack7z(file, dir, entry string, limit int64) error {
	r, err := sevenzip.OpenReader(file)
	if err != nil {
		return err
	}
	defer r.Close()
	var total uint64
	for _, f := range r.File {
		total += f.UncompressedSize
		if total > uint64(limit) {
			return ErrTooLarge
		}
	}
	u := newUnpacker(dir, entry, limit)
	for _, f := range r.File {
		mode := f.Mode()
		name := strings.ReplaceAll(f.Name, `\`, "/")
		switch {
		case mode.IsDir():
			if err := u.mkdir(name); err != nil {
				return err
			}
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			if err := u.link(name, string(target)); err != nil {
				return err
			}
		case mode.IsRegular() || mode&fs.ModeType == 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			// 7-Zip made on Windows keeps no Unix modes: the program to run is marked as one
			// afterwards (markEntry), and everything else is a plain file.
			perm := mode.Perm()
			if perm == 0 {
				perm = 0o644
			}
			err = u.file(name, perm, rc)
			rc.Close()
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("the archive holds something that isn't a file: %q", name)
		}
	}
	return nil
}

// copyTree copies a folder that's been opened read-only (a mounted disk image) through the
// unpacker, so a disk image is held to an archive's rules.
func copyTree(root, dir, entry string, limit int64) error {
	u := newUnpacker(dir, entry, limit)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		name := filepath.ToSlash(rel)
		// A disk image's own furniture: its background picture, the link to Applications.
		if strings.HasPrefix(name, ".") || name == "Applications" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := info.Mode(); {
		case mode.IsDir():
			return u.mkdir(name)
		case mode&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return u.link(name, target)
		case mode.IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			return u.file(name, mode.Perm(), f)
		default:
			return fmt.Errorf("the disk image holds something that isn't a file: %q", name)
		}
	})
}
