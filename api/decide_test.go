// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// How a game plays on this machine: one answer for Play, the game's page and the console list.

func consoleView(t *testing.T, a *App, id string) ConsoleView {
	t.Helper()
	for _, v := range a.ConsoleViews(context.Background()) {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("no console %s", id)
	return ConsoleView{}
}

// A Nintendo 64 game on a server with no card it can use plays, drawn by the processor, with the
// options that have it do so over anything anyone chose, and no room made for a graphics driver.
// With a card, it draws on the card.
func TestTheProcessorDrawsWhereACoreCan(t *testing.T) {
	a, _, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Error: macNoGPU})
	putCores(t, a, "mupen64plus_next")
	it := addCoreGame(t, ph, 7, "n64", "Game.z64", append([]byte{0x80, 0x37, 0x12, 0x40}, make([]byte, 4092)...))
	ctx := context.Background()
	c := wittyplay.ByID("n64")
	check := a.CheckGame(ctx, &it, c)
	if check["ready"] != true || check["software"] != true || check["needsGpu"] != false || check["notice"] != softwareNotice {
		t.Fatalf("check: %v", check)
	}
	if v := consoleView(t, a, "n64"); !v.Ready || !v.Software || v.SoftwareWhy != "no-card" || v.Reason != "" || v.PlaysOn != "mupen64plus_next" {
		t.Errorf("n64 in the list: %+v", v)
	}
	p, no := a.PlanGame(ctx, &it)
	if no != nil {
		t.Fatalf("plan: %v", no)
	}
	if p.CoreID != "mupen64plus_next" || !p.Software || p.GPU || slices.Contains(p.RunnerArgs, "-gpu") || len(p.Notices) != 1 {
		t.Errorf("plan: %+v", p)
	}
	for k, v := range cpuDrawing["mupen64plus_next"] {
		if p.CPUOptions[k] != v {
			t.Errorf("%s = %q, want %q", k, p.CPUOptions[k], v)
		}
	}
	// With a card, it draws on the card, where the card draws it right.
	hostOS = "linux"
	setCardAnswer(a, stream.GPUProbe{Name: "A card"})
	if check := a.CheckGame(ctx, &it, c); check["ready"] != true || check["software"] != nil || check["needsGpu"] != true {
		t.Errorf("with a card: %v", check)
	}
	if p, _ := a.PlanGame(ctx, &it); p == nil || p.Software || !p.GPU || p.CPUOptions != nil {
		t.Errorf("with a card: %+v", p)
	}
}

// A console with a core of each kind plays on the one that needs no card, even where someone chose
// the other; with a card, the one chosen.
func TestSoftwareCoreStandsInForAGPUCore(t *testing.T) {
	a, _, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Error: llvmpipeNo})
	putCores(t, a, "swanstation", "pcsx_rearmed")
	ctx := context.Background()
	if err := a.SetConfig(ctx, settingCores, map[string]string{"ps1": "swanstation"}); err != nil {
		t.Fatal(err)
	}
	it := addCoreGame(t, ph, 7, "ps1", "Game.cue", []byte("FILE \"Game.bin\" BINARY\n"))
	if p := a.PickCore(ctx, wittyplay.ByID("ps1"), it.ID); p.Core == nil || p.Core.ID != "pcsx_rearmed" || p.Software {
		t.Errorf("without a card: %+v", p)
	}
	if v := consoleView(t, a, "ps1"); !v.Ready {
		t.Errorf("ps1: %+v", v)
	}
	setCardAnswer(a, stream.GPUProbe{Name: "A card"})
	if p := a.PickCore(ctx, wittyplay.ByID("ps1"), it.ID); p.Core == nil || p.Core.ID != "swanstation" {
		t.Errorf("with a card: %+v", p)
	}
}

// A console whose every core needs a card, on a server without one, isn't played here, and says
// so everywhere: Play is turned away before a runner starts.
func TestAGPUGameSaysNotOnThisServerEverywhere(t *testing.T) {
	a, _, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Error: macNoGPU})
	putCores(t, a, "flycast", "fceumm")
	ctx := context.Background()
	dc := addCoreGame(t, ph, 7, "dc", "Game.gdi", []byte("1\n"))
	if p, no := a.PlanGame(ctx, &dc); p != nil || no == nil || no.Status != 409 || no.Body["reason"] != "gpu" || no.Body["needsGpu"] != true {
		t.Fatalf("play: %+v %+v", p, no)
	}
	check := a.CheckGame(ctx, &dc, wittyplay.ByID("dc"))
	if check["ready"] != false || check["reason"] != "gpu" || check["runsOn"] == nil {
		t.Errorf("check: %v", check)
	}
	if v := consoleView(t, a, "dc"); v.Ready || v.Reason != "gpu" || v.GPUMissing == "" {
		t.Errorf("dc in the list: %+v", v)
	}
	// Other consoles still play.
	nes := addCoreGame(t, ph, 8, "nes", "Game.nes", []byte("NES\x1a"))
	if p, no := a.PlanGame(ctx, &nes); no != nil || p.CoreID != "fceumm" || p.GPU {
		t.Errorf("nes: %+v %+v", p, no)
	}
	// A runner that says, starting it, there's no card: the same answer.
	no := StartRefusal(wittyplay.ByID("dc"), errors.New("the game could not start: this server has no usable graphics card for this game (OpenGL here is only llvmpipe)"))
	if no == nil || no.Body["gpuMissing"] != "OpenGL here is only llvmpipe" {
		t.Errorf("start refusal: %+v", no)
	}
	if StartRefusal(wittyplay.ByID("dc"), errors.New("the core crashed")) != nil {
		t.Error("a crash isn't a missing card")
	}
}

// Play's other refusals: a console nobody plays here, no core, a BIOS missing.
func TestPlayRefusals(t *testing.T) {
	a, _, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A card"})
	ctx := context.Background()
	unknown := addCoreGame(t, ph, 6, "no-such-console", "Game.bin", []byte("x"))
	if _, no := a.PlanGame(ctx, &unknown); no == nil || no.Status != 404 {
		t.Errorf("an unknown console: %+v", no)
	}
	nes := addCoreGame(t, ph, 7, "nes", "Game.nes", []byte("NES\x1a"))
	if _, no := a.PlanGame(ctx, &nes); no == nil || no.Status != 409 || no.Body["needCore"] != true || !strings.Contains(no.Error(), "No core is installed") {
		t.Errorf("no core: %+v", no)
	}
	putCores(t, a, "pcsx2")
	ps2 := addCoreGame(t, ph, 8, "ps2", "Game.iso", make([]byte, 4096))
	no := func() *Refusal { _, no := a.PlanGame(ctx, &ps2); return no }()
	if no == nil || no.Body["needBios"] != true || no.Body["file"] == "" {
		t.Fatalf("no BIOS: %+v", no)
	}
	if check := a.CheckGame(ctx, &ps2, wittyplay.ByID("ps2")); check["reason"] != "bios" || check["ready"] != false {
		t.Errorf("check with no BIOS: %v", check)
	}
	// A BIOS left in a folder beside the game, under a name its core takes, is found when it's played.
	bios := filepath.Join(filepath.Dir(ps2.Files[0].Path), wittyplay.BIOSFolderName, "SCPH-39001_USA.BIN")
	if err := os.MkdirAll(filepath.Dir(bios), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bios, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	p, no := a.PlanGame(ctx, &ps2)
	if no != nil || p.CoreID != "pcsx2" {
		t.Fatalf("with a BIOS beside it: %+v %+v", p, no)
	}
	rule := wittyplay.Traits("pcsx2").RequiredBIOS
	if rule == nil || p.CoreOptions[rule.Option] == "" {
		t.Errorf("the BIOS it starts with: %v", p.CoreOptions)
	}
	// The test console needs no core.
	t.Setenv("WMS_GAMING_TESTCORE", "1")
	test := addCoreGame(t, ph, 9, wittyplay.TestConsole, "Game.test", []byte("x"))
	if p, no := a.PlanGame(ctx, &test); no != nil || p.CorePath != ipc.TestCore || p.CoreID != "test" {
		t.Errorf("the test console: %+v %+v", p, no)
	}
}

// The other cores are tried in turn when a game won't load: those with what they need, drawn by
// the processor where the card can't, and never a custom core.
func TestTheOtherCoresAreFallbacks(t *testing.T) {
	a, _, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A card"})
	putCores(t, a, "fceumm", "nestopia", "mesen")
	nes := addCoreGame(t, ph, 7, "nes", "Game.nes", []byte("NES\x1a"))
	p, no := a.PlanGame(context.Background(), &nes)
	if no != nil || p.CoreID != "fceumm" || len(p.Fallbacks) != 2 || p.Fallbacks[0].CoreID == "fceumm" {
		t.Fatalf("%+v %+v", p, no)
	}
	// Only one core, for a game no other core loads.
	a.PlanOnly(context.Background(), p, *a.Cores().Find("mesen"))
	if p.CoreID != "mesen" || p.Fallbacks != nil {
		t.Errorf("only: %+v", p)
	}
}

// On a Mac whose runner can use the card, PlayStation 2 plays on Play!, on the card, where nobody
// chose a core; someone who chose LRPS2 keeps it, drawn by the processor.
func TestAMacCardPlaysPS2OnPlay(t *testing.T) {
	a, _, ph := coresApp(t)
	hostOS = "darwin"
	setCardAnswer(a, stream.GPUProbe{Name: "Apple M3 Pro"})
	putCores(t, a, "pcsx2", "play")
	bios := filepath.Join(a.SystemDir(), "pcsx2", "bios", "SCPH-39001_USA.BIN")
	if err := os.MkdirAll(filepath.Dir(bios), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bios, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	it := addCoreGame(t, ph, 7, "ps2", "Game.iso", make([]byte, 4096))
	check := a.CheckGame(ctx, &it, wittyplay.ByID("ps2"))
	if check["ready"] != true || check["software"] != nil || check["needsGpu"] != true || check["core"].(map[string]any)["id"] != "play" {
		t.Fatalf("check: %v", check)
	}
	if v := consoleView(t, a, "ps2"); !v.Ready || v.Software || v.PlaysOn != "play" || v.BIOSMissing != "" {
		t.Errorf("ps2 in the list: %+v", v)
	}
	if err := a.SetConfig(ctx, settingCores, map[string]string{"ps2": "pcsx2"}); err != nil {
		t.Fatal(err)
	}
	if p := a.PickCore(ctx, wittyplay.ByID("ps2"), it.ID); p.Core == nil || p.Core.ID != "pcsx2" || !p.Software || p.Notice != cardFaultNotice() {
		t.Errorf("chosen: %+v", p)
	}
	if v := consoleView(t, a, "ps2"); !v.Software || v.SoftwareWhy != "card-fault" {
		t.Errorf("chosen, in the list: %+v", v)
	}
}

// On a Mac with a card, 3DS plays on Azahar, drawn by the processor, rather than giving way to
// Panda3DS; someone who chose Panda3DS keeps it; elsewhere Azahar draws on the card.
func TestAMacPlays3DSOnAzaharsProcessor(t *testing.T) {
	a, _, _ := coresApp(t)
	hostOS = "darwin"
	setCardAnswer(a, stream.GPUProbe{Name: "Apple M3 Pro"})
	putCores(t, a, "azahar", "panda3ds")
	ctx := context.Background()
	c := wittyplay.ByID("3ds")
	if p := a.PickCore(ctx, c, 0); p.Core == nil || p.Core.ID != "azahar" || !p.Software || p.Notice != cardFaultNotice() {
		t.Fatalf("%+v", p)
	}
	if err := a.SetConfig(ctx, settingCores, map[string]string{"3ds": "panda3ds"}); err != nil {
		t.Fatal(err)
	}
	if p := a.PickCore(ctx, c, 0); p.Core == nil || p.Core.ID != "panda3ds" || p.Software {
		t.Errorf("chosen: %+v", p)
	}
	hostOS = "linux"
	if err := a.SetConfig(ctx, settingCores, nil); err != nil {
		t.Fatal(err)
	}
	if p := a.PickCore(ctx, c, 0); p.Core == nil || p.Core.ID != "azahar" || p.Software {
		t.Errorf("on Linux: %+v", p)
	}
}

// How each core went is counted, across games and for each.
func TestHowPlayWentIsCounted(t *testing.T) {
	a, _, _ := coresApp(t)
	ctx := context.Background()
	a.NoteCorePlay(ctx, "fceumm", 7, 1, 0, 0, "")
	a.NoteCorePlay(ctx, "fceumm", 7, 0, 1, 30, "it stopped")
	a.NoteCorePlay(ctx, "", 7, 1, 0, 0, "")
	totals := a.corePlayTotals(ctx)
	if p := totals["fceumm"]; p.Starts != 1 || p.Failures != 1 || p.Seconds != 30 || len(totals) != 1 {
		t.Errorf("%+v", totals)
	}
	if got := a.configText(ctx, "gamingMaxSessions", "2"); got != "2" {
		t.Errorf("unset: %q", got)
	}
	for _, v := range []any{3, "3"} {
		_ = a.SetConfig(ctx, "gamingMaxSessions", v)
		if got := a.configText(ctx, "gamingMaxSessions", "2"); got != "3" {
			t.Errorf("%v: %q", v, got)
		}
	}
	_ = a.SetConfig(ctx, "gamingCustomCores", true)
	if got := a.configText(ctx, "gamingCustomCores", "0"); got != "1" {
		t.Errorf("a switch: %q", got)
	}
}
