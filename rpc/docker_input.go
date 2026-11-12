// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/emulators/configfile"
	"github.com/wittywilla/wittyplay/emulators/inputmap"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// writeInputMap writes the keys each player's buttons press (the keys.p1 to keys.p4 settings,
// or WittyPlay's own) into the emulator's own controller settings before it starts, so that
// every player's pad works with nothing set up by hand. The keys are named as the emulator
// names them on the system it runs on: X11 in a container or the local sandbox, or this
// machine's own for an emulator on its desktop.
func writeInputMap(e *emulators.Emulator, dir string, eff emulators.Values, goos string, onHost bool) error {
	if !onHost {
		goos = "linux"
	}
	id := e.Base()
	if !inputmap.Supported(id) {
		return nil
	}
	km, err := inputagent.ParseKeys(keysJSON(eff))
	if err != nil {
		return fmt.Errorf("the key map: %w", err)
	}
	targets, err := inputmap.Targets(id, goos, inputmap.KeyMap(km))
	if err != nil {
		return err
	}
	a := emulators.Applied{Edits: map[string]emulators.FileEdits{}}
	whole := map[string][]byte{}
	for _, t := range targets {
		if len(t.Path) == 0 {
			text, ok := t.Value.(string)
			if !ok {
				return fmt.Errorf("a whole file for %s that isn't text", t.File)
			}
			whole[t.File] = []byte(text)
			continue
		}
		fe := a.Edits[t.File]
		fe.Format = t.Format
		fe.Edits = append(fe.Edits, configfile.Edit{Path: t.Path, Value: t.Value})
		a.Edits[t.File] = fe
	}
	if err := writeWhole(dir, whole); err != nil {
		return err
	}
	return a.WriteFiles(dir)
}

// writeWhole writes files that are nothing but controller bindings, through os.Root like every
// other settings write: the emulator may have left a link in its own folder.
func writeWhole(dir string, files map[string][]byte) error {
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, b := range files {
		local := filepath.FromSlash(name)
		if !filepath.IsLocal(local) {
			return fmt.Errorf("the controller file %q isn't inside the settings folder", name)
		}
		if d := filepath.Dir(local); d != "." {
			if err := root.MkdirAll(d, 0o700); err != nil {
				return err
			}
		}
		if st, err := root.Lstat(local); err == nil && !st.Mode().IsRegular() {
			return fmt.Errorf("the controller file %s isn't a plain file", name)
		}
		tmp := local + ".wittyplay-tmp"
		_ = root.Remove(tmp)
		if err := root.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		if err := root.Rename(tmp, local); err != nil {
			_ = root.Remove(tmp)
			return err
		}
	}
	return nil
}
