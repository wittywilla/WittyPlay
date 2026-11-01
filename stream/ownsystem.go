package stream

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wittywilla/wittyplay"
)

// ownSystemFolder is where, in the player's save folder, a core that keeps saves in its
// system folder finds that folder.
const ownSystemFolder = "system"

// maxSystemCopy bounds what gets copied each time a game starts. A BIOS and a core's support
// files are a few megabytes. Anything that doesn't fit, such as a texture pack, is left out.
const maxSystemCopy = 64 << 20

// ownSystem gives a core that keeps saves in its system folder a folder of its own, since
// a running core can't write to the shared one and shouldn't see other players' saves.
// Its saves stay there. Beside them go copies of the BIOS in use and the core's support
// files, which go again when release is called. They aren't links: on Linux a core could
// write through a hard link to the real file, and the save folder shouldn't hold firmware
// for backups to pick up.
//
// Any other core carries on with the shared folder.
func ownSystem(coreID, systemDir, saveDir string, options map[string]string) (dir string, release func(), err error) {
	t := wittyplay.Traits(coreID)
	if t.SavesIn == "" {
		return systemDir, func() {}, nil
	}
	saves := filepath.FromSlash(t.SavesIn)
	dir = filepath.Join(saveDir, ownSystemFolder)
	if err := os.MkdirAll(filepath.Join(dir, saves), 0o700); err != nil {
		return systemDir, func() {}, err
	}
	top := filepath.Dir(saves)
	keep := []string{filepath.Base(saves)}
	if t.CacheIn != "" && filepath.Dir(filepath.FromSlash(t.CacheIn)) == top {
		keep = append(keep, filepath.Base(t.CacheIn))
	}
	release = func() { removeExcept(filepath.Join(dir, top), keep...) }
	release() // left behind if the server stopped suddenly
	bios, biosName := "", ""
	if r := t.RequiredBIOS; r != nil {
		bios, biosName = filepath.FromSlash(r.Folder), options[r.Option]
	}
	if err := copySupport(systemDir, top, saves, bios, biosName, dir); err != nil {
		release()
		return systemDir, func() {}, err
	}
	return dir, release, nil
}

// removeExcept empties a folder, apart from what's named.
func removeExcept(dir string, keep ...string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !slices.Contains(keep, e.Name()) {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
}

// copySupport copies a core's folder from the system folder, without any saves that were
// left in it and without the BIOS files other than the one in use. These share the BIOS's
// name when they go with it. With no name given, every BIOS goes and the core picks.
func copySupport(systemDir, top, saves, bios, biosName, to string) error {
	wanted := strings.TrimSuffix(filepath.Base(biosName), filepath.Ext(biosName))
	left := int64(maxSystemCopy)
	root := filepath.Join(systemDir, top)
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(systemDir, p)
		if err != nil {
			return err
		}
		if rel == saves {
			return filepath.SkipDir
		}
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o700)
		}
		if !e.Type().IsRegular() {
			return nil
		}
		if filepath.Dir(rel) == bios && biosName != "" {
			name := e.Name()
			if !strings.EqualFold(strings.TrimSuffix(name, filepath.Ext(name)), wanted) {
				return nil
			}
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.Size() > left {
			return nil
		}
		left -= info.Size()
		return copyFile(p, filepath.Join(to, rel))
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s: %w", filepath.Base(from), err)
	}
	return out.Close()
}
