// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

//go:build !darwin && !windows

package host

import (
	"context"
	"errors"
	"os/exec"
	"runtime"

	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// Linux and FreeBSD run an emulator in sandbox/local instead: a virtual X display of its own,
// boxed in by bubblewrap on Linux. Nothing here is used on them.

var errHere = errors.New("emulators on this machine run in WittyPlay's local sandbox on " + runtime.GOOS)

func unsupported() string {
	return "on " + runtime.GOOS + " an emulator on this machine runs in the local sandbox, with a display of its own, not on the desktop"
}

func platformMissing() []string { return nil }
func sandboxName() string       { return "none" }
func memoryMB() int64           { return 0 }

type window struct{}

type box struct{}

func (b *box) close()                       {}
func (b *box) started(*exec.Cmd) error      { return errHere }
func sandbox(*exec.Cmd, plan) (*box, error) { return nil, errHere }
func systemEnv(plan) []string               { return nil }
func hideConsole(*exec.Cmd)                 {}
func platformEncoders() []string            { return []string{"x264"} }

func findWindow(context.Context, int) (window, error) { return window{}, errHere }

func newSink(int, window, int, int) inputagent.Sink { return nil }

func newCapture(window, int, int, int) (capture, error) { return nil, errHere }

func suspend(*exec.Cmd, *box, bool) error { return errHere }
func kill(*exec.Cmd, *box)                {}
func terminate(*exec.Cmd, *box)           {}
