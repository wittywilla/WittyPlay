// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/sandbox/docker"
	"github.com/wittywilla/wittyplay/sandbox/host"
)

// What a game on this machine's own desktop needs beyond what a container's does: its command
// line from the list, its paths as this machine's, a copy of its own for a build that writes
// beside itself, and its input passed straight to the runtime.

// CommandLine is the emulator's command as a container would see it (the program under
// /opt/emulator, the game under /game, settings under /saves): the platform's own arguments
// from the list if it has them, else the emulator's. {game}, {config}, {bios} and {settings}
// are filled in; a command line without {game} ends with it, one without {settings} has the
// settings' own arguments just before the game. The host runtime turns these paths into this
// machine's.
func CommandLine(e *emulators.Emulator, art *emulators.Artifact, entry string, settingsArgs []string, romName string) []string {
	tmpl := e.Args
	if art != nil && len(art.Args) > 0 {
		tmpl = art.Args
	}
	game := docker.ContainerROM + "/" + romName
	config := docker.ContainerSaves + "/" + art.SettingsDir()
	fill := strings.NewReplacer("{game}", game, "{config}", config, "{bios}", docker.ContainerBIOS)
	out := []string{docker.ContainerProgram + "/" + entry}
	hasGame := slices.ContainsFunc(tmpl, func(a string) bool { return strings.Contains(a, "{game}") })
	placed := false
	for _, a := range tmpl {
		if a == "{settings}" {
			out = append(out, settingsArgs...)
			placed = true
			continue
		}
		if strings.Contains(a, "{game}") && !placed {
			out = append(out, settingsArgs...)
			placed = true
		}
		out = append(out, fill.Replace(a))
	}
	if !hasGame {
		if !placed {
			out = append(out, settingsArgs...)
		}
		out = append(out, game)
	}
	return out
}

// hostPaths turns the container paths some settings hold (PCSX2's BIOS folder is "/bios") into
// this machine's, for an emulator on its desktop.
func hostPaths(v emulators.Values, saves, biosDir string) emulators.Values {
	out := emulators.Values{}
	for k, val := range v {
		s, ok := val.(string)
		if !ok {
			out[k] = val
			continue
		}
		switch {
		case s == docker.ContainerBIOS || strings.HasPrefix(s, docker.ContainerBIOS+"/"):
			if biosDir != "" {
				s = filepath.Join(biosDir, filepath.FromSlash(strings.TrimPrefix(s, docker.ContainerBIOS)))
			}
		case s == docker.ContainerSaves || strings.HasPrefix(s, docker.ContainerSaves+"/"):
			s = filepath.Join(saves, filepath.FromSlash(strings.TrimPrefix(s, docker.ContainerSaves)))
		}
		out[k] = s
	}
	return out
}

// portableCopy keeps a copy of an installed emulator in a person's own folder, made again when
// the installed version changes, for a build that writes its settings and data beside itself
// (RPCS3 on Windows). The installed one stays read-only.
func portableCopy(inst *emulators.Installed, saves string) (string, error) {
	dir := filepath.Join(saves, "app")
	marker := filepath.Join(dir, ".wittyplay-version")
	want := inst.Version + " " + inst.SHA256
	if b, err := os.ReadFile(marker); err == nil && string(b) == want {
		return dir, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	err := filepath.WalkDir(inst.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(inst.Dir, p)
		if err != nil || rel == "." {
			return err
		}
		to := filepath.Join(dir, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(to, 0o700)
		case info.Mode()&os.ModeSymlink != 0:
			return nil // the installer allowed only links inside; a copy needs none on Windows
		case info.Mode().IsRegular():
			// Settings the person changed are theirs: an existing file outside the program's own
			// isn't overwritten unless the installed copy is newer.
			if st, err := os.Stat(to); err == nil && !st.ModTime().Before(info.ModTime()) && st.Size() == info.Size() {
				return nil
			}
			return copyFile(p, to, info.Mode().Perm()|0o200)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return dir, os.WriteFile(marker, []byte(want), 0o600)
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := to + ".wittyplay-tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, to)
}

// hostSender passes input to an emulator on this machine.
type hostSender struct {
	rt *host.Runtime
	id string
}

func (h *hostSender) Send(p []byte) { _ = h.rt.Input(h.id, p) }
func (h *hostSender) Close()        { h.rt.ReleaseInput(h.id) }

// encoderChoices are the encoders on offer for an emulator's stream: the processor everywhere,
// VA-API for a container with a card passed through, and on this machine its own.
func encoderChoices() []emulators.Choice {
	out := []emulators.Choice{{Value: "x264", Label: "The processor (x264)"}}
	switch runtime.GOOS {
	case "linux":
		out = append(out, emulators.Choice{Value: "vaapi", Label: "The graphics card (VA-API; needs it passed through)"})
	case "darwin":
		out = append(out, emulators.Choice{Value: "videotoolbox", Label: "This Mac's media engine (VideoToolbox)"})
	case "windows":
		out = append(out, emulators.Choice{Value: "nvenc", Label: "An NVIDIA graphics card (NVENC)"},
			emulators.Choice{Value: "qsv", Label: "Intel graphics (Quick Sync)"}, emulators.Choice{Value: "amf", Label: "An AMD graphics card (AMF)"})
	}
	return out
}

// seedFirmware copies the firmware an emulator writes to (xemu's hard disk image) from the BIOS
// folder into the person's own folder, the first time: the BIOS folder is everyone's and is given
// to the emulator read-only. A file that isn't there is the BIOS check's to report, not this.
func seedFirmware(e *emulators.Emulator, biosDir, saves string) error {
	if biosDir == "" {
		return nil
	}
	for _, name := range e.Seed {
		to := filepath.Join(saves, name)
		if _, err := os.Lstat(to); err == nil {
			continue
		}
		from := filepath.Join(biosDir, name)
		st, err := os.Stat(from)
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		if err := copyFile(from, to, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// dumpRoot is what a game file stands for, when it stands for a folder: PS3_DISC.SFB for its
// disc's folder (which RPCS3 boots), and a Wii U game's code/*.rpx for the game's folder, with
// the program's place inside it (Cemu needs code, content and meta beside each other). Any
// other file is itself.
func dumpRoot(rom string) (root, inGame string) {
	base := filepath.Base(rom)
	dir := filepath.Dir(rom)
	switch {
	case strings.EqualFold(base, "PS3_DISC.SFB"):
		return dir, ""
	case strings.EqualFold(filepath.Ext(base), ".rpx") && strings.EqualFold(filepath.Base(dir), "code"):
		return filepath.Dir(dir), filepath.Base(dir) + "/" + base
	}
	return rom, ""
}

// maxPlaced bounds what placing firmware copies: a Switch's firmware is a few hundred megabytes.
const maxPlaced = 2 << 30

// placeFirmware copies what the owner put in the BIOS folder to where the emulator looks for it
// in its own settings folder (Placement): keys, and firmware the emulator keeps as files. A file
// already there and as new is left alone; nothing is copied through a link.
func placeFirmware(e *emulators.Emulator, goos, biosDir, saves string) error {
	if biosDir == "" {
		return nil
	}
	var total int64
	for _, pl := range e.Placements(goos) {
		from := filepath.Join(biosDir, filepath.FromSlash(pl.From))
		to := filepath.Join(saves, filepath.FromSlash(pl.To))
		st, err := os.Lstat(from)
		if err != nil {
			continue // not supplied: the status says so
		}
		if st.Mode().IsRegular() {
			if err := copyIfNewer(from, to, st, &total); err != nil {
				return err
			}
			continue
		}
		if !st.IsDir() {
			continue
		}
		err = filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(from, p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case d.IsDir():
				return os.MkdirAll(filepath.Join(to, rel), 0o700)
			case info.Mode().IsRegular():
				return copyIfNewer(p, filepath.Join(to, rel), info, &total)
			}
			return nil // links and the rest are left out
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func copyIfNewer(from, to string, st os.FileInfo, total *int64) error {
	if have, err := os.Lstat(to); err == nil && have.Mode().IsRegular() && have.Size() == st.Size() && !have.ModTime().Before(st.ModTime()) {
		return nil
	}
	*total += st.Size()
	if *total > maxPlaced {
		return fmt.Errorf("the firmware to copy is larger than %d bytes", int64(maxPlaced))
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if have, err := os.Lstat(to); err == nil && !have.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a plain file", to)
	}
	return copyFile(from, to, 0o600)
}
