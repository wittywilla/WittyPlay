// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/stream"
)

// Games drawn on a graphics card, on a server that can't give them one. Every place that
// says a game can be played asks here first, so the game page, the console list and the
// Play button agree, and a game that can't run here is turned away before a runner starts.
// See the server's docs/GAMING.md, "Consoles that draw with a graphics card". Moved from the
// server's games_gpu.go and api_games.go (gpuStatus).

// gpuPlatformReason is why a runner can't draw on a graphics card on this operating
// system: WittyPlay's runner makes an OpenGL context on Windows and Linux only.
const gpuPlatformReason = "hardware-rendered games run on Windows, Linux and macOS servers"

// gpuAskAgain is how long a graphics card check that got no answer is left before
// something about to start a game asks again. Settings asks whenever it's opened.
const gpuAskAgain = 5 * time.Minute

// gpuOS says whether the runner can draw on a graphics card on this operating system at all.
func gpuOS(goos string) bool { return goos == "windows" || goos == "linux" || goos == "darwin" }

// gpuMissingFrom says why a game drawn on a graphics card can't run here, or "" when it
// can or nothing says otherwise. A final answer from the runner counts. Until there is one,
// a system the runner can't draw on at all says so, and anywhere else the game may try.
func gpuMissingFrom(p stream.GPUProbe, goos string) string {
	switch {
	case p.Name != "":
		return ""
	case p.Error != "" && !p.Again:
		return plainGPUReason(p.Error)
	case !gpuOS(goos):
		return gpuPlatformReason
	}
	return ""
}

// plainGPUReason takes the runner's "no usable graphics card" off the front of its answer,
// which the sentence it goes in already says, and keeps the reason.
func plainGPUReason(s string) string {
	s = strings.TrimSpace(s)
	for _, lead := range []string{"this server has no usable graphics card for this game", "this server has no usable graphics card", stream.NoGPUText} {
		if rest, ok := strings.CutPrefix(s, lead); ok {
			s = rest
			break
		}
	}
	s = strings.TrimSpace(strings.TrimLeft(s, ":;, "))
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = s[1 : len(s)-1]
	}
	return strings.TrimRight(strings.TrimSpace(s), ".")
}

// gpuMessage is what a person is told: not on this server, and why.
func gpuMessage(console, why string) string {
	who := "These games"
	if console != "" {
		who = console + " games"
	}
	msg := who + " can't play on this server. They're drawn on a graphics card, and this server has none it can use"
	if why != "" {
		msg += ": " + why
	}
	return msg + ". Games for older consoles still play."
}

// gpuAnswer is the graphics card check as it stands: its answer, whether that's final,
// when the last answer came in, and whether a check is running now.
func (e *engine) gpuAnswer() (p stream.GPUProbe, final bool, at time.Time, checking bool) {
	e.gpuMu.Lock()
	defer e.gpuMu.Unlock()
	return e.gpu, e.gpu.Name != "" || (e.gpu.Error != "" && !e.gpu.Again), e.gpuAt, e.gpuChecking
}

// GPUMissing says why games drawn on a graphics card can't run on this server, or "" when
// they can, or may. The first time anything asks, the check starts in the background, and
// the answer counts from when it comes in, a second or two later.
func (a *App) GPUMissing(ctx context.Context) string {
	p, final, at, checking := a.eng().gpuAnswer()
	if !final && !checking && (at.IsZero() || time.Since(at) > gpuAskAgain) {
		// Finding the runner is only done when the check runs.
		p = a.GPUStatus(ctx)
	}
	return gpuMissingFrom(p, runtime.GOOS)
}

// GPUStatus is what's known about the graphics card (the server's gpuStatus). Finding out takes
// a second or two, or half a minute if a security scanner holds the runner's first start, so it
// happens in the background and nothing waits for it. An answer about the card is final; a
// failure to ask gets asked again next time.
func (a *App) GPUStatus(ctx context.Context) stream.GPUProbe {
	e := a.eng()
	e.gpuMu.Lock()
	defer e.gpuMu.Unlock()
	if e.gpuChecking || (e.gpu.Name != "" || (e.gpu.Error != "" && !e.gpu.Again)) {
		out := e.gpu
		out.Checking = e.gpuChecking
		return out
	}
	runner, _ := a.Runner(ctx)
	if runner == "" {
		return e.gpu
	}
	e.gpuChecking = true
	go func() {
		// What a fault leaves: nothing learnt about the card, so it's asked again next time.
		got := stream.GPUProbe{Error: "the check stopped on a fault", Again: true}
		defer func() {
			if v := recover(); v != nil {
				a.Log.Error("wittyplay: the graphics card check stopped on a fault", "cat", "games", "err", fmt.Sprint(v))
			}
			e.gpuMu.Lock()
			defer e.gpuMu.Unlock()
			e.gpu, e.gpuAt, e.gpuChecking = got, time.Now(), false
		}()
		got = probeGPU(runner, filepath.Join(a.Dir, "probe"), a.Log.With("cat", "games"))
	}()
	return stream.GPUProbe{Checking: true}
}

// probeGPU asks the runner about the card; tests stand in for it.
var probeGPU = stream.ProbeGPU

// coreDrawsOnGPU says whether a core draws on a graphics card when it runs as it normally does.
// A custom core goes by what it was seen to do when it was added; "maybe" gets to try.
func (a *App) coreDrawsOnGPU(c cores.Core) bool {
	if c.Source == cores.SourceCustom {
		for _, cc := range a.customCoreList() {
			if cc.ID == c.ID {
				return cc.WantsGPU
			}
		}
		return false
	}
	return wittyplay.Traits(c.ID).GPU
}

// cpuDrawing are cores that draw on a graphics card where there is one, and can have the
// processor draw instead, with these options laid over everything else. A server with no card
// it can use starts them this way. Each was seen drawing games correctly on an Apple M3 Pro with
// no graphics context at all: Nintendo 64 at full speed on about one core (angrylion draws, cxd4
// runs the graphics microcode, as angrylion needs), PSP mostly at full speed on four or five
// (PPSSPP's own software renderer; "none" never asks the runner for a context), and PlayStation 2
// mostly at full speed, slower in places (LRPS2's "Software (SW)" renderer, which hands frames
// over without a context), and a 3DS game at 60 frames a second (Azahar's own software
// renderer). docs/GAMING.md has the figures. Cores missing here have no such mode: Play!,
// Flycast, Panda3DS and the faster PlayStation cores.
var cpuDrawing = map[string]map[string]string{
	"mupen64plus_next": {"mupen64plus-rdp-plugin": "angrylion", "mupen64plus-rsp-plugin": "cxd4"},
	"parallel_n64":     {"parallel-n64-gfxplugin": "angrylion", "parallel-n64-rspplugin": "cxd4"},
	"ppsspp":           {"ppsspp_backend": "none"},
	"pcsx2":            {"pcsx2_renderer": "Software (SW)"},
	"azahar":           {"citra_graphics_api": "Software"},
}

// softwareNotice is the speed caveat for a game the processor draws, shown before it starts and
// when it does.
const softwareNotice = "This server has no graphics card games can use, so its processor draws this game instead, at the console's own resolution. It may run slower than on a server with a card, and keeps several processor cores busy while it plays."

// cardFaults are cores whose drawing on a graphics card goes wrong on a system, as seen with real
// games, so that there they're drawn by the processor even where the runner can use a card. On a
// Mac (an Apple M3 Pro, OpenGL 4.1 through a CGL context), with the owner's games:
//   - Mupen64Plus-Next's GLideN64 handed over black pictures for Super Mario 64 and Banjo-Kazooie.
//     Without its framebuffer emulation it drew Super Mario 64 at full speed, but Banjo-Kazooie at
//     38 frames a second, going black after half a minute. The processor draws both at 60.
//   - ParaLLEl N64's OpenGL plugins drew nothing at all.
//   - LRPS2's OpenGL renderer needs GL_ARB_shading_language_420pack, which macOS doesn't have, and
//     made no picture.
//   - Azahar asks for a Vulkan context on a Mac, which the runner can't offer, and wouldn't start.
//
// PPSSPP, SwanStation, Beetle PSX HW and Play! drew their games correctly at full speed there, on
// the card, and aren't listed. Each listed core has a way for the processor to draw it.
var cardFaults = map[string]map[string]string{
	"darwin": {
		"mupen64plus_next": "GLideN64 draws black pictures with a Mac's OpenGL",
		"parallel_n64":     "its OpenGL plugins draw nothing with a Mac's OpenGL",
		"pcsx2":            "its OpenGL renderer needs an extension a Mac's OpenGL doesn't have",
		"azahar":           "it asks for Vulkan on a Mac, which the game runner can't offer",
	},
}

// keepsOnCPU are cores whose processor drawing keeps the console's games at full speed, so
// where their card drawing goes wrong they stay, drawn by the processor, rather than giving way
// to another core that draws on the card: Azahar plays far more 3DS games than Panda3DS, and
// its software renderer kept 60 frames a second on an Apple M3 Pro.
var keepsOnCPU = map[string]bool{"azahar": true}

// hostOS is the system this server runs on. Tests change it.
var hostOS = runtime.GOOS

// drawsWrongHere says why a core's drawing on a graphics card goes wrong on this server's
// system, or "" when it doesn't, as far as is known. Never for a custom core.
func drawsWrongHere(c cores.Core) string {
	if c.Source == cores.SourceCustom {
		return ""
	}
	return cardFaults[hostOS][c.ID]
}

// rightOnTheCard is the first of a console's cores, other than first, that draws on a graphics
// card and draws right on this system's: what plays when nobody chose a core and first draws
// wrongly here. drawsOnCard says a core draws on the card and can start (its BIOS is here).
func rightOnTheCard(list []cores.Core, first cores.Core, drawsOnCard func(cores.Core) bool) *cores.Core {
	for _, c := range list {
		if c.Source != cores.SourceCustom && c.ID != first.ID && drawsWrongHere(c) == "" && drawsOnCard(c) {
			return &c
		}
	}
	return nil
}

// cardFaultNotice is the speed caveat for a game the processor draws because its core draws
// wrongly on this system's graphics card.
func cardFaultNotice() string {
	where := "this server's system"
	if hostOS == "darwin" {
		where = "a Mac"
	}
	return "On " + where + " this console's games are drawn by the server's processor, at the console's own resolution, since its emulator doesn't draw them correctly on the graphics card there. They may run slower than on a card, and keep several processor cores busy while they play."
}

// cpuOptions are the options that have a core drawn by the processor, or nil if it can't be.
// Never for a custom core: what it offers isn't known.
func cpuOptions(c cores.Core) map[string]string {
	if c.Source == cores.SourceCustom {
		return nil
	}
	return cpuDrawing[c.ID]
}

// needsCard says a core can't run at all without a graphics card.
func (a *App) needsCard(c cores.Core) bool { return a.coreDrawsOnGPU(c) && cpuOptions(c) == nil }

// withoutGPUArg is a runner's arguments for a core the processor draws: none of what a
// graphics driver needs.
func withoutGPUArg(args []string) []string {
	return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "-gpu" })
}

// allOfferedNeedCard says no core offered for a console could run without a graphics card.
func allOfferedNeedCard(console *wittyplay.Console) bool {
	if len(console.Cores) == 0 {
		return false
	}
	for _, c := range console.Cores {
		if !wittyplay.Traits(c.ID).GPU || cpuDrawing[c.ID] != nil {
			return false
		}
	}
	return true
}
