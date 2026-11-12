// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/wittywilla/wittyplay/emulators"
)

// maxDiscover is the largest settings file read for options the schema doesn't know.
const maxDiscover = 4 << 20

// personSchema is schemaFor, plus the options found in this person's own settings files for the
// emulator that the schema doesn't describe: a newer emulator's, or one the generator missed.
// The files are the emulator's own, so they're read through os.Root and held to a size.
func (b *DockerBackend) personSchema(e *emulators.Emulator, user int64) []emulators.Setting {
	schema := schemaFor(e)
	if user <= 0 || b.dataDir == "" {
		return schema
	}
	var art *emulators.Artifact
	switch {
	case e.Own:
		art = e.Here()
	case e.Manual != nil:
		goos, _ := b.platform()
		art = e.ManualArtifact(goos)
	case b.inst != nil:
		art = b.inst.Artifact(e)
	}
	dir := filepath.Join(b.dataDir, "docker", "saves", strconv.FormatInt(user, 10), e.ID, filepath.FromSlash(art.SettingsDir()))
	root, err := os.OpenRoot(dir)
	if err != nil {
		return schema
	}
	defer root.Close()
	type file struct{ name, format string }
	seen := map[file]bool{}
	var files []file
	for _, s := range schema {
		if s.Apply == nil {
			continue
		}
		var f file
		switch {
		case s.Apply.File != nil:
			f = file{s.Apply.File.File, s.Apply.File.Format}
		case s.Apply.INI != nil:
			f = file{s.Apply.INI.File, "ini"}
		default:
			continue
		}
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	for _, f := range files {
		h, err := root.Open(filepath.FromSlash(f.name))
		if err != nil {
			continue
		}
		st, err := h.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Size() > maxDiscover {
			h.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(h, maxDiscover))
		h.Close()
		if err != nil {
			continue
		}
		schema = append(schema, emulators.Discover(f.format, data, schema)...)
	}
	return schema
}
