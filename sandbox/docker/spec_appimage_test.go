// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"slices"
	"strings"
	"testing"
)

// TestAppRunOnlyForAppImages: the exec-allowed tmpfs at /run/app exists only when the program is
// an AppImage, and even then it's nosuid and nodev.
func TestAppRunOnlyForAppImages(t *testing.T) {
	plain := request()
	plain.ProgramDir = "/data/emulators/pcsx2/2.2.0"
	s, err := Build(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Tmpfs[ContainerAppRun]; ok {
		t.Fatalf("a program that isn't an AppImage got %s: %v", ContainerAppRun, s.Tmpfs)
	}
	for target, opts := range s.Tmpfs {
		if !strings.Contains(opts, "noexec") {
			t.Errorf("tmpfs %s allows exec: %s", target, opts)
		}
	}
	if slices.ContainsFunc(s.Env, func(e string) bool { return strings.HasPrefix(e, "APPIMAGE_EXTRACT_AND_RUN=") }) {
		t.Error("APPIMAGE_EXTRACT_AND_RUN set for a program that isn't an AppImage")
	}

	app := plain
	app.AppImage = true
	s, err = Build(app)
	if err != nil {
		t.Fatal(err)
	}
	opts, ok := s.Tmpfs[ContainerAppRun]
	if !ok {
		t.Fatalf("an AppImage has no %s: %v", ContainerAppRun, s.Tmpfs)
	}
	o := strings.Split(opts, ",")
	for _, want := range []string{"exec", "nosuid", "nodev", "mode=0700", "uid=1000", "gid=1000"} {
		if !slices.Contains(o, want) {
			t.Errorf("%s is missing %q: %s", ContainerAppRun, want, opts)
		}
	}
	if slices.Contains(o, "noexec") || slices.Contains(o, "suid") || slices.Contains(o, "dev") {
		t.Errorf("%s has conflicting options: %s", ContainerAppRun, opts)
	}
	for target, opts := range s.Tmpfs {
		if target != ContainerAppRun && !strings.Contains(opts, "noexec") {
			t.Errorf("tmpfs %s allows exec beside the AppImage's: %s", target, opts)
		}
	}
	if !slices.Contains(s.Env, "TMPDIR="+ContainerAppRun) || !slices.Contains(s.Env, "APPIMAGE_EXTRACT_AND_RUN=1") {
		t.Errorf("an AppImage isn't pointed at %s: %v", ContainerAppRun, s.Env)
	}
	if err := Check(s); err != nil {
		t.Fatalf("Check refuses Build's own AppImage spec: %v", err)
	}

	// An AppImage needs the folder it's in.
	noDir := request()
	noDir.AppImage = true
	if _, err := Build(noDir); err == nil {
		t.Error("an AppImage without an emulator folder was accepted")
	}
}

// TestProgramFolderIsReadOnly: an installed emulator's folder is mounted read-only, and Check
// refuses a spec that would let a container write to it.
func TestProgramFolderIsReadOnly(t *testing.T) {
	r := request()
	r.ProgramDir = "/data/emulators/pcsx2/2.2.0"
	r.AppImage = true
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(s.Mounts, func(m Mount) bool { return m.Target == ContainerProgram })
	if i < 0 {
		t.Fatalf("no %s mount: %+v", ContainerProgram, s.Mounts)
	}
	if m := s.Mounts[i]; m.Source != r.ProgramDir || !m.ReadOnly {
		t.Fatalf("the emulator's folder isn't mounted read-only from %s: %+v", r.ProgramDir, m)
	}
	s.Mounts[i].ReadOnly = false
	if Check(s) == nil {
		t.Error("Check accepted a writable emulator folder")
	}

	for _, bad := range []string{"relative/dir", "/proc/1", "/var/run/docker.sock", "/data/../etc"} {
		r := request()
		r.ProgramDir = bad
		if _, err := Build(r); err == nil {
			t.Errorf("emulator folder %q was accepted", bad)
		}
	}
}

// TestCheckRefusesExecTmpfs: no tmpfs but /run/app may allow exec, and none may allow suid or
// devices.
func TestCheckRefusesExecTmpfs(t *testing.T) {
	r := request()
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Spec){
		"exec /tmp":        func(s *Spec) { s.Tmpfs["/tmp"] = "rw,exec,nosuid,nodev,size=64m" },
		"suid /run/app":    func(s *Spec) { s.Tmpfs[ContainerAppRun] = "rw,exec,nodev,size=64m" },
		"dev /run/app":     func(s *Spec) { s.Tmpfs[ContainerAppRun] = "rw,exec,nosuid,size=64m" },
		"exec elsewhere":   func(s *Spec) { s.Tmpfs["/opt/x"] = "rw,exec,nosuid,nodev" },
		"home without dev": func(s *Spec) { s.Tmpfs[ContainerEmuHome] = "rw,noexec,nosuid" },
	} {
		t.Run(name, func(t *testing.T) {
			c := s
			c.Tmpfs = map[string]string{}
			for k, v := range s.Tmpfs {
				c.Tmpfs[k] = v
			}
			mut(&c)
			if Check(c) == nil {
				t.Errorf("Check accepted %v", c.Tmpfs)
			}
		})
	}
}
