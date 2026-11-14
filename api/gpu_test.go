// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay/stream"
)

// What the runner answers on a Mac, and on Linux with only a software OpenGL.
const (
	macNoGPU   = "this server has no usable graphics card for this game: hardware-rendered games run on Windows, Linux and macOS servers"
	llvmpipeNo = "this server has no usable graphics card for this game (OpenGL here is only llvmpipe)"
)

func TestGPUMissingSaysWhy(t *testing.T) {
	for _, c := range []struct {
		name string
		p    stream.GPUProbe
		goos string
		want string
	}{
		{"a card", stream.GPUProbe{Name: "Apple M3 Pro"}, "darwin", ""},
		{"the runner said no, on FreeBSD", stream.GPUProbe{Error: macNoGPU}, "freebsd", gpuPlatformReason},
		{"the runner said no, on Linux", stream.GPUProbe{Error: llvmpipeNo}, "linux", "OpenGL here is only llvmpipe"},
		// A Mac's runner draws on its card: until it answers, a game may try, as on Linux.
		{"no answer yet, on a Mac", stream.GPUProbe{Checking: true}, "darwin", ""},
		{"couldn't ask, on FreeBSD", stream.GPUProbe{Error: "the runner could not check: exit status 1", Again: true}, "freebsd", gpuPlatformReason},
		{"no answer yet, on Linux", stream.GPUProbe{}, "linux", ""},
		{"couldn't ask, on Windows", stream.GPUProbe{Error: "the runner could not check: exit status 1", Again: true}, "windows", ""},
	} {
		if got := gpuMissingFrom(c.p, c.goos); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if got := plainGPUReason("this server has no usable graphics card for this game: no OpenGL library (libEGL) is installed"); got != "no OpenGL library (libEGL) is installed" {
		t.Errorf("%q", got)
	}
}

func TestGPUMessageSaysNotHereAndWhy(t *testing.T) {
	m := gpuMessage("Nintendo 64", gpuPlatformReason)
	for _, want := range []string{"Nintendo 64 games can't play on this server", "graphics card", gpuPlatformReason} {
		if !strings.Contains(m, want) {
			t.Errorf("%q doesn't say %q", m, want)
		}
	}
	if m := gpuMessage("PSP", ""); strings.Contains(m, ": .") || !strings.HasSuffix(m, ".") {
		t.Errorf("%q", m)
	}
}

func TestEveryCardFaultHasAProcessorPath(t *testing.T) {
	for goos, faults := range cardFaults {
		for id, why := range faults {
			if cpuDrawing[id] == nil {
				t.Errorf("%s on %s: listed (%s) with no way for the processor to draw it", id, goos, why)
			}
		}
	}
}

// What a server says about how a core draws here follows its own card: on a Mac whose runner can
// use the card, the cores that draw right there use it and the rest are drawn by the processor.
func TestHowACoreDrawsOnAMacWithACard(t *testing.T) {
	t.Cleanup(func() { hostOS = runtime.GOOS })
	hostOS = "darwin"
	for _, c := range []struct {
		id, noGPU, want string
	}{
		{"ppsspp", "", drawsGPU},
		{"play", "", drawsGPU},
		{"swanstation", "", drawsGPU},
		{"mupen64plus_next", "", drawsCPUFallback},
		{"parallel_n64", "", drawsCPUFallback},
		{"pcsx2", "", drawsCPUFallback},
		{"azahar", "", drawsCPUFallback},
		// No card it can use: as before.
		{"ppsspp", macNoGPU, drawsCPUFallback},
		{"play", macNoGPU, drawsNone},
		{"mupen64plus_next", macNoGPU, drawsCPUFallback},
	} {
		got, note := drawsHere(c.id, true, c.noGPU)
		if got != c.want {
			t.Errorf("%s (no card: %q): %s, want %s: %s", c.id, c.noGPU, got, c.want, note)
		}
		if c.noGPU == "" && got == drawsCPUFallback && !strings.Contains(note, "Mac") {
			t.Errorf("%s: the note doesn't say why: %s", c.id, note)
		}
	}
	hostOS = "linux"
	if got, _ := drawsHere("mupen64plus_next", true, ""); got != drawsGPU {
		t.Errorf("on Linux with a card: %s", got)
	}
}

// The card check runs once, in the background, and its answer counts from when it comes in; a
// failure to ask is asked again.
func TestTheCardCheckRunsInTheBackground(t *testing.T) {
	a, _, _ := coresApp(t)
	asked := make(chan struct{}, 4)
	probeGPU = func(runner, dir string, _ *slog.Logger) stream.GPUProbe {
		asked <- struct{}{}
		if !strings.HasSuffix(dir, "probe") || runner == "" {
			t.Errorf("asked %q in %q", runner, dir)
		}
		return stream.GPUProbe{Error: llvmpipeNo}
	}
	if p := a.GPUStatus(context.Background()); !p.Checking {
		t.Fatalf("the first answer: %+v", p)
	}
	<-asked
	deadline := time.Now().Add(5 * time.Second)
	for a.GPUStatus(context.Background()).Checking && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := a.GPUMissing(context.Background()); got != "OpenGL here is only llvmpipe" {
		t.Errorf("after the check: %q", got)
	}
	// Final: not asked again.
	_ = a.GPUStatus(context.Background())
	select {
	case <-asked:
		t.Error("a final answer was asked again")
	case <-time.After(50 * time.Millisecond):
	}
	if r := StartRefusal(nil, nil); r != nil {
		t.Error("no failure, no refusal")
	}
}
