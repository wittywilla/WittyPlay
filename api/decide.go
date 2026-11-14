// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// How a game plays on this machine: the one place that decides it. Pressing Play (PlanGame), a
// game's page (CheckGame), the console list (ConsoleViews), the core picker and the settings page
// all ask here, so they agree. It brings together what were the server's pickCore and play path
// (api_games.go), its game check (api_games_home.go), and the runner and its settings
// (gameRunner, configureGames).

// ---- the runner and its settings ----

// settingCores is the setting that says which core each console plays on unless a game says
// otherwise: a JSON object, console to core (the server's gamingCores).
const settingCores = "gamingCores"

// configText is one of WittyPlay's settings as text, whatever it was stored as: a string as it
// is, a number in figures, true and false as "1" and "0". def when it isn't set. The server kept
// these as text, and its settings pages may write them either way.
func (a *App) configText(ctx context.Context, key, def string) string {
	var raw json.RawMessage
	if ok, err := a.Config(ctx, key, &raw); !ok || err != nil {
		return def
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		if b {
			return "1"
		}
		return "0"
	}
	var f json.Number
	if json.Unmarshal(raw, &f) == nil {
		return f.String()
	}
	return def
}

// PreferredCores is which core each console plays on unless a game says otherwise.
func (a *App) PreferredCores(ctx context.Context) map[string]string {
	out := map[string]string{}
	if _, err := a.Config(ctx, settingCores, &out); err != nil || out == nil {
		return map[string]string{}
	}
	return out
}

// programBeside finds one of WittyPlay's programs beside this one, or on the PATH when onPath.
func programBeside(name string, onPath bool) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if onPath {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// ownRunner is WittyPlay's own game runner, whichever one plays games: a core's test-load is a
// question only it knows how to answer, and it needs no graphics card.
func ownRunner() string {
	if p := os.Getenv("WMS_WITTYPLAY_RUNNER"); p != "" {
		return p
	}
	return programBeside("wittyplay-game-runner", false)
}

// Runner is the program that runs a game here: WittyRetroArch when it's installed and the
// gamingRunner setting is auto or retroarch, else WittyPlay's own runner beside this program.
// Both speak the same protocol over pipes, so a GPL program runs as a separate process.
func (a *App) Runner(ctx context.Context) (path string, args []string) {
	if p := os.Getenv("WMS_WITTYPLAY_RUNNER"); p != "" {
		return p, nil
	}
	if choice := a.configText(ctx, "gamingRunner", "auto"); choice == "auto" || choice == "retroarch" {
		if p := programBeside("wittyretroarch", true); p != "" {
			return p, []string{"--wittyplay"}
		}
	}
	if p := ownRunner(); p != "" {
		return p, nil
	}
	return "", nil
}

// hwEncoder is one hardware encoder in the server's tools: whether it worked when tried.
type hwEncoder struct {
	Name    string `json:"name"`
	Working bool   `json:"working"`
}

func hardwareEncoders(t host.Tools) []hwEncoder {
	var out []hwEncoder
	for _, raw := range t.Hardware {
		var e hwEncoder
		if json.Unmarshal(raw, &e) == nil && e.Name != "" {
			out = append(out, e)
		}
	}
	return out
}

// Configure sets how the next game runs (rpc.Mgr), with no restart needed for a new runner: the
// runner, FFmpeg and the hardware encoders the server found working (those switched on in
// hwEncoders go first), how many games at once, and the network settings.
func (a *App) Configure(ctx context.Context) error {
	runner, args := a.Runner(ctx)
	if runner == "" {
		return errors.New("WittyPlay's game runner is missing from this install")
	}
	tools, err := a.Host.Tools(ctx)
	if err != nil {
		a.Log.WarnContext(ctx, "wittyplay: the server's tools couldn't be asked", "cat", "games", "err", err)
	}
	enabled := map[string]bool{}
	for _, n := range strings.Split(a.configText(ctx, "hwEncoders", ""), ",") {
		enabled[strings.TrimSpace(n)] = true
	}
	var hw, gpuEnc []string
	for _, e := range hardwareEncoders(tools) {
		if e.Working && enabled[e.Name] {
			hw = append(hw, e.Name)
		}
		if e.Working {
			gpuEnc = append(gpuEnc, e.Name)
		}
	}
	maxSessions, _ := strconv.Atoi(a.configText(ctx, "gamingMaxSessions", "2"))
	maxGPU, _ := strconv.Atoi(a.configText(ctx, "gamingMaxGpuSessions", "2"))
	port, _ := strconv.Atoi(a.configText(ctx, "gamingUdpPort", "0"))
	if port < 0 || port > 65535 {
		port = 0
	}
	encoders := tools.Encoders
	rpc.Mgr.Configure(func(c *stream.Config) {
		c.Runner, c.RunnerArgs, c.HWEncoders, c.GPUEncoders, c.MaxSessions, c.MaxGPU, c.UDPPort = runner, args, hw, gpuEnc, maxSessions, maxGPU, port
		c.FFmpeg = tools.FFmpeg
		c.HaveEncoder = func(n string) bool { return encoders[n] }
		c.PublicIPs = splitNonEmpty(a.configText(ctx, "gamingPublicIp", ""))
		c.STUN = splitNonEmpty(a.configText(ctx, "gamingStun", ""))
	})
	return nil
}

func splitNonEmpty(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// StartOptions are the options a core starts a console's games with here: WittyPlay's own for it,
// at the resolution an administrator chose.
func (a *App) StartOptions(ctx context.Context, consoleID, coreID string) map[string]string {
	return wittyplay.StartOptions(consoleID, coreID, a.configText(ctx, "gamingResolution", ""), a.SystemDir())
}

// ---- which core ----

// PreferredCore decides what to ask the cores for: this game's own choice, then the console's,
// then, when nothing in the table is here, the first custom core assigned to the console. Only
// cores from the table, or approved and assigned for this console, count, so no setting can reach
// a file nobody approved. "" asks for the first core here.
func (a *App) PreferredCore(ctx context.Context, console *wittyplay.Console, itemID int64) string {
	mgr := a.Cores()
	all := mgr.List()
	usable := func(id string) bool {
		for i := range all {
			if all[i].ID != id {
				continue
			}
			if all[i].Unapproved {
				return false
			}
			return all[i].Source != cores.SourceCustom || slices.Contains(all[i].Consoles, console.ID) || consoleListsCore(console, id)
		}
		return false
	}
	if itemID != 0 {
		if id := a.gameCoreChoice(ctx, itemID); id != "" && usable(id) {
			return id
		}
	}
	if id := a.PreferredCores(ctx)[console.ID]; id != "" && usable(id) {
		return id
	}
	if mgr.ForConsole(console, "") != nil {
		return ""
	}
	for i := range all {
		if all[i].Source == cores.SourceCustom && slices.Contains(all[i].Consoles, console.ID) {
			return all[i].ID
		}
	}
	return ""
}

// Pick is the core a game would start on, as pressing Play chooses it.
type Pick struct {
	Core     *cores.Core // what it starts on, or nil
	Software bool        // it draws on a graphics card, which this server hasn't got or draws it wrongly on, so the processor draws it
	Notice   string      // with Software: the speed caveat, and why
	Blocked  *cores.Core // with no Core: the one wanted, which is missing a BIOS
	NoGPU    string      // with no Core: why what could play it can't draw on this server
}

// PickCore chooses a game's core (itemID 0 for a console's games in general). On a server without
// a graphics card it can use, a core that draws on one starts with the processor drawing, if it
// can; if not, it gives way to another installed core that needs no card, and then to one that
// can do without. When none can play it here, NoGPU says why, ahead of a missing BIOS or core:
// finding either would change nothing.
func (a *App) PickCore(ctx context.Context, console *wittyplay.Console, itemID int64) Pick {
	systemDir := a.SystemDir()
	mgr := a.Cores()
	// Goes through PreferredCore: it's what keeps a file nobody approved from being used.
	preferred := a.PreferredCore(ctx, console, itemID)
	core, blocked := mgr.Playable(console, preferred, systemDir)
	if core != nil && !a.coreDrawsOnGPU(*core) {
		return Pick{Core: core}
	}
	if core == nil && blocked == nil && !allOfferedNeedCard(console) {
		return Pick{} // nothing installed, and a core that would play here could be
	}
	if core == nil && blocked != nil && !a.needsCard(*blocked) {
		return Pick{Blocked: blocked}
	}
	why := a.GPUMissing(ctx)
	if why == "" {
		if core != nil && drawsWrongHere(*core) != "" {
			// Nobody chose it: a core that draws right on this system's card plays instead, as
			// Play! does PlayStation 2 on a Mac. A game Play! can't open still goes on to LRPS2,
			// as the next core to try.
			if preferred == "" && !keepsOnCPU[core.ID] {
				if c := rightOnTheCard(mgr.AllForConsole(console, ""), *core, func(c cores.Core) bool {
					return a.coreDrawsOnGPU(c) && wittyplay.MissingBIOS(c.ID, systemDir) == ""
				}); c != nil {
					return Pick{Core: c}
				}
			}
			if cpuOptions(*core) != nil {
				return Pick{Core: core, Software: true, Notice: cardFaultNotice()}
			}
		}
		return Pick{Core: core, Blocked: blocked}
	}
	if core != nil && cpuOptions(*core) != nil {
		return Pick{Core: core, Software: true, Notice: softwareNotice}
	}
	var needsBIOS, onCPU *cores.Core
	for _, c := range mgr.AllForConsole(console, preferred) {
		// A custom core only ever runs where an administrator chose it.
		if c.Source == cores.SourceCustom {
			continue
		}
		bios := wittyplay.MissingBIOS(c.ID, systemDir) == ""
		switch {
		case a.coreDrawsOnGPU(c):
			switch {
			case cpuOptions(c) == nil:
			case bios && onCPU == nil:
				onCPU = &c
			case !bios && needsBIOS == nil:
				needsBIOS = &c // it would play here, drawn by the processor, with a BIOS
			}
		case bios:
			return Pick{Core: &c}
		case needsBIOS == nil:
			needsBIOS = &c
		}
	}
	switch {
	case onCPU != nil:
		return Pick{Core: onCPU, Software: true, Notice: softwareNotice}
	case needsBIOS != nil:
		return Pick{Blocked: needsBIOS} // it would play here, with a BIOS
	}
	return Pick{NoGPU: why}
}

// ---- no ----

// Refusal is why a game can't start or be got ready here: the status and the JSON answer the
// server gave ({"error": "...", and needBios, needCore, reason gpu and the like}).
type Refusal struct {
	Status int
	Body   map[string]any
}

func (r *Refusal) Error() string {
	if s, ok := r.Body["error"].(string); ok {
		return s
	}
	return "this game can't play here"
}

// Write answers with the refusal.
func (r *Refusal) Write(w http.ResponseWriter) { writeJSON(w, r.Status, r.Body) }

func refuse(status int, msg string) *Refusal {
	return &Refusal{Status: status, Body: map[string]any{"error": msg}}
}

// GPURefusal is the answer to a request for a game that can't draw here, and why.
func GPURefusal(console *wittyplay.Console, why string) *Refusal {
	return &Refusal{Status: 409, Body: map[string]any{"error": gpuMessage(console.Name, why), "reason": "gpu", "needsGpu": true, "gpuMissing": why, "console": console.ID}}
}

// StartRefusal is the answer when a game's runner said, starting it, that there's no graphics
// card it can use; nil for any other failure.
func StartRefusal(console *wittyplay.Console, err error) *Refusal {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, stream.NoGPUText) {
		return nil
	}
	why := ""
	if i := strings.Index(msg, "this server has "+stream.NoGPUText); i >= 0 {
		why = plainGPUReason(msg[i:])
	}
	return GPURefusal(console, why)
}

// ---- starting a game ----

// Plan is what a game starts with here. Put it into stream.Options: Core (CorePath), CoreID, GPU,
// RunnerArgs, CoreOptions (with anyone's own options laid over), Fallbacks, SystemDir, CoreAssets
// and LoadLimit; add Notices; and with Software, lay CPUOptions over everything last.
type Plan struct {
	Console  *wittyplay.Console
	Core     *cores.Core // nil for the test console
	CorePath string      // the core's library, or ipc.TestCore
	CoreID   string      // "test" for the test console
	GPU      bool        // it draws on the graphics card
	// Software: it draws on a graphics card, which this server hasn't got or draws wrongly on, so
	// the processor draws it: CPUOptions go over every other option, whoever chose them, or the
	// core asks for a card that isn't there.
	Software    bool
	CPUOptions  map[string]string
	Notices     []string
	RunnerArgs  []string
	CoreOptions map[string]string
	// Fallbacks are the console's other cores, tried in turn if the game won't load on this one.
	Fallbacks []stream.CoreChoice
	SystemDir string
	// A custom core's assets, and longer to load: a virus scanner may look again at a file that
	// has only just moved here.
	CoreAssets string
	LoadLimit  time.Duration
}

// gamePath is a game item's file, as the server describes it.
func gamePath(it *service.Item) string {
	if it == nil || len(it.Files) == 0 {
		return ""
	}
	return it.Files[0].Path
}

// PlanGame decides how a game starts on this machine: its console (404 when this server doesn't
// play it), whether that console plays here at all, the BIOS files beside it, which core, and that
// core's options, its region's BIOS and the cores to try next. A refusal is the answer to give.
// The game's file is the server's: it's read, never written.
func (a *App) PlanGame(ctx context.Context, it *service.Item) (*Plan, *Refusal) {
	c := console(itemConsole(it))
	if c == nil {
		return nil, refuse(404, "not found")
	}
	if c.Unavailable != "" && !a.CustomFor(ctx, c.ID) {
		return nil, refuse(409, c.Unavailable)
	}
	systemDir := a.SystemDir()
	p := &Plan{Console: c, CorePath: ipc.TestCore, CoreID: "test", SystemDir: systemDir}
	if c.ID == wittyplay.TestConsole {
		return p, nil
	}
	// BIOS files left in a folder beside the games are fetched first, for every core that might
	// use one: by what they are, then by their names.
	path := gamePath(it)
	a.FindBIOSNear(ctx, path)
	for _, want := range c.Cores {
		if err := wittyplay.CollectBIOS(c, want.ID, path, systemDir); err != nil {
			// File names only, never what is in them.
			a.Log.WarnContext(ctx, "copying a BIOS", "cat", "games", "console", c.ID, "core", want.ID, "err", err)
		}
	}
	pick := a.PickCore(ctx, c, it.ID)
	if pick.NoGPU != "" {
		return nil, GPURefusal(c, pick.NoGPU)
	}
	core, blocked := pick.Core, pick.Blocked
	if core == nil && blocked != nil {
		return nil, &Refusal{Status: 409, Body: map[string]any{"error": wittyplay.BIOSHelp(blocked.ID, c.Name), "needBios": true, "console": c.ID,
			"file": wittyplay.MissingBIOS(blocked.ID, systemDir)}}
	}
	if core == nil {
		a.Log.WarnContext(ctx, "a game couldn't start: no core is installed for its console", "cat", "games", "console", c.ID, "game", it.Title)
		return nil, &Refusal{Status: 409, Body: map[string]any{"error": "No core is installed for " + c.Name + ".", "needCore": true, "console": c.ID}}
	}
	if core.Source == cores.SourceCustom {
		if err := a.CheckCustomCore(ctx, core); err != nil {
			return nil, refuse(409, err.Error())
		}
	}
	a.planOn(ctx, p, *core, pick.Software)
	if pick.Software {
		p.Notices = append(p.Notices, pick.Notice)
	}
	if rule := wittyplay.Traits(core.ID).RequiredBIOS; rule != nil && rule.Option != "" {
		if bios := a.regionBIOSFor(ctx, it, rule.Folder, p.CoreOptions[rule.Option]); bios != "" {
			p.CoreOptions = overOptions(p.CoreOptions, map[string]string{rule.Option: bios})
		}
	}
	// If the game won't load on this core, the others get their turn.
	others := a.Cores().AllForConsole(c, core.ID)
	if len(others) > 0 {
		others = others[1:]
	}
	noGPU := ""
	if slices.ContainsFunc(others, a.coreDrawsOnGPU) {
		noGPU = a.GPUMissing(ctx)
	}
	for _, other := range others {
		// A custom core only ever runs where an administrator assigned it, and after its check.
		if other.Source == cores.SourceCustom || wittyplay.MissingBIOS(other.ID, systemDir) != "" {
			continue
		}
		next := stream.CoreChoice{Core: other.Path, CoreID: other.ID, RunnerArgs: wittyplay.RunnerArgs(other.ID), GPU: wittyplay.Traits(other.ID).GPU, Options: a.StartOptions(ctx, c.ID, other.ID)}
		if a.coreDrawsOnGPU(other) && (noGPU != "" || drawsWrongHere(other) != "") {
			cpu := cpuOptions(other)
			if cpu == nil {
				continue
			}
			next.Options, next.GPU, next.RunnerArgs = overOptions(next.Options, cpu), false, withoutGPUArg(next.RunnerArgs)
		}
		p.Fallbacks = append(p.Fallbacks, next)
	}
	return p, nil
}

// planOn sets a plan to start on one core.
func (a *App) planOn(ctx context.Context, p *Plan, core cores.Core, software bool) {
	c := core
	p.Core, p.CorePath, p.CoreID = &c, core.Path, core.ID
	p.Software = software
	p.GPU = !software && (wittyplay.Traits(core.ID).GPU || (core.Source == cores.SourceCustom && core.WantsGPU))
	p.RunnerArgs = wittyplay.RunnerArgs(core.ID)
	p.CPUOptions = nil
	if software {
		p.RunnerArgs = withoutGPUArg(p.RunnerArgs)
		p.CPUOptions = cpuDrawing[core.ID]
	}
	p.CoreOptions = a.StartOptions(ctx, p.Console.ID, core.ID)
	p.CoreAssets, p.LoadLimit = "", 0
	if core.Source == cores.SourceCustom {
		p.CoreAssets, p.LoadLimit = core.Assets, stream.ProbeTimeout
	}
}

// PlanOnly changes a plan to start on exactly this core, with nothing else to try: what a
// Satellaview game (Mesen-S) and a libdragon one (ParaLLEl N64) need, which no other core loads.
// It keeps the plan's software drawing, where the processor draws.
func (a *App) PlanOnly(ctx context.Context, p *Plan, core cores.Core) {
	a.planOn(ctx, p, core, p.Software && cpuOptions(core) != nil)
	p.Fallbacks = nil
}

// ---- how play went ----

// NoteCorePlay adds to what's known about a core on a game. Every core is counted, the table's
// too: it's how "works best" can mean something.
func (a *App) NoteCorePlay(ctx context.Context, core string, item int64, starts, failures int, seconds float64, lastErr string) {
	if core == "" {
		return
	}
	if len(lastErr) > 400 {
		lastErr = lastErr[:400]
	}
	_, err := a.DB.ExecContext(context.WithoutCancel(ctx), `INSERT INTO game_core_plays(core_id, item_id, starts, failures, seconds, last_error, last_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(core_id, item_id) DO UPDATE SET starts = starts + excluded.starts, failures = failures + excluded.failures, seconds = seconds + excluded.seconds,
		last_error = CASE WHEN excluded.last_error != '' THEN excluded.last_error ELSE last_error END, last_at = excluded.last_at`,
		core, item, starts, failures, seconds, lastErr, time.Now().Unix())
	if err != nil {
		a.Log.Warn("recording how a core went", "cat", "games", "err", err)
	}
}

// CoreStartFailed writes down a core that failed to start a game: against the core, and in the
// server's audit log for a custom core.
func (a *App) CoreStartFailed(ctx context.Context, p *Plan, item int64, on host.On, err error) {
	if p == nil || p.Core == nil || err == nil {
		return
	}
	a.NoteCorePlay(ctx, p.Core.ID, item, 1, 1, 0, err.Error())
	if p.Core.Source == cores.SourceCustom {
		a.auditCore(ctx, on, "use-failed", p.Core.ID, fmt.Sprintf("item %d: %v", item, err))
	}
}

// WatchCorePlay notes a game started on a core, waits for it to end and notes how it did. Run it
// in a goroutine once the game has started.
func (a *App) WatchCorePlay(sess *stream.Session, core *cores.Core, on host.On, item int64) {
	if core == nil {
		return
	}
	ctx := context.Background()
	a.NoteCorePlay(ctx, core.ID, item, 1, 0, 0, "")
	start := time.Now()
	<-sess.Done()
	fail := sess.Failure()
	failures := 0
	if fail != "" {
		failures = 1
		if core.Source == cores.SourceCustom {
			a.auditCore(ctx, on, "use-failed", core.ID, fmt.Sprintf("item %d, after %d seconds: %s", item, int(time.Since(start).Seconds()), fail))
		}
	}
	a.NoteCorePlay(ctx, core.ID, item, 0, failures, time.Since(start).Seconds(), fail)
}

// ---- a game's page: will it play? ----

// checkBIOS is one of a console's BIOS files, for a game's check.
type checkBIOS struct {
	File     string `json:"file"`
	For      string `json:"for"`
	Required bool   `json:"required"`
	Found    bool   `json:"found"`
	// Using is the file a core that takes any name will start this game with, and From where
	// the file was found, when it was found by its checksum.
	Using string `json:"using,omitempty"`
	From  string `json:"from,omitempty"`
}

// decryptedCopyPlays is what plays instead of an encrypted 3DS game (the server's library text).
const decryptedCopyPlays = "A copy you decrypted yourself, from a console you own, will play."

// encryptedMessage is why an encrypted 3DS game can't play, and what would.
func encryptedMessage() string {
	return strings.TrimRight(wittyplay.ErrEncrypted3DS.Error(), ". ") + ". " + decryptedCopyPlays
}

// CheckGame says what a game needs and whether this server has it, in time for its page to say
// so rather than the Play button finding out: GET /games/{id}/check's answer (ready, reason,
// message, bios, core, needsGpu, software, notice, runsOn, alternative, noBuild, emulator, file).
// A console this server doesn't know is {"ready": false, "reason": "unknown", "bios": []}.
func (a *App) CheckGame(ctx context.Context, it *service.Item, console *wittyplay.Console) map[string]any {
	if console == nil {
		return map[string]any{"ready": false, "reason": "unknown", "bios": []checkBIOS{}}
	}
	systemDir := a.SystemDir()
	path := gamePath(it)
	// As pressing Play does: files left in a BIOS folder beside the games count.
	if len(console.BIOS) > 0 {
		a.FindBIOSNear(ctx, path)
		for _, want := range console.Cores {
			_ = wittyplay.CollectBIOS(console, want.ID, path, systemDir)
		}
	}
	records := a.biosRecords()
	bios := make([]checkBIOS, 0, len(console.BIOS))
	for _, b := range console.BIOS {
		_, err := os.Stat(filepath.Join(systemDir, filepath.FromSlash(b.File)))
		e := checkBIOS{File: b.File, For: b.For, Required: b.Required, Found: err == nil}
		if e.Found {
			e.From = records[filepath.ToSlash(b.File)].From
		} else if b.Required {
			// A core that takes any file its rule accepts, under any name: the one it would use.
			if rule, use := a.biosInUse(ctx, console, it, systemDir); use != "" {
				e.Found, e.Using = true, use
				e.From = records[filepath.ToSlash(filepath.Join(rule.Folder, use))].From
			}
		}
		bios = append(bios, e)
	}
	out := map[string]any{"console": console.ID, "consoleName": console.Name, "bios": bios, "biosFolder": wittyplay.BIOSFolderName}
	if console.ID == wittyplay.TestConsole {
		out["ready"] = true
		return out
	}
	if console.Unavailable != "" && !a.CustomFor(ctx, console.ID) {
		out["ready"], out["reason"], out["message"] = false, "unavailable", console.Unavailable
		return out
	}
	// An encrypted copy can't play on any server, so that comes before what this one lacks, and
	// says the same as Play would.
	if console.ID == "3ds" {
		if info, err := wittyplay.Inspect3DS(path); err == nil && info.Encrypted {
			out["ready"], out["reason"], out["message"], out["encrypted"] = false, "encrypted", encryptedMessage(), true
			return out
		}
	}
	pick := a.PickCore(ctx, console, it.ID)
	if pick.Core == nil {
		// No core plays it here: a standalone emulator may.
		offers := a.ConsoleOffers(ctx)[console.ID]
		if e := ReadyEmulator(offers); e != nil {
			out["ready"], out["emulator"] = true, map[string]any{"id": e.ID, "name": e.Name, "version": e.Version}
			return out
		}
		if len(console.Cores) == 0 {
			out["ready"], out["reason"], out["message"] = false, "emulator", StandaloneWords(console, offers)
			return out
		}
	}
	switch {
	case pick.Core != nil:
		out["ready"], out["core"] = true, map[string]any{"id": pick.Core.ID, "name": pick.Core.Name, "license": pick.Core.License}
		out["needsGpu"] = !pick.Software && a.coreDrawsOnGPU(*pick.Core)
		if pick.Software {
			// It plays, drawn by the processor: how it may run, before anyone presses Play.
			out["software"], out["notice"] = true, pick.Notice
		}
	case pick.NoGPU != "":
		// Not on this server, and why: the Play button would only be turned away. Where it would
		// play, for whoever runs the server.
		out["ready"], out["reason"], out["message"] = false, "gpu", gpuMessage(console.Name, pick.NoGPU)
		out["needsGpu"], out["gpuMissing"] = true, pick.NoGPU
		out["runsOn"] = consoleRunsOn(console)
	case pick.Blocked != nil:
		out["ready"], out["reason"], out["message"] = false, "bios", wittyplay.BIOSHelp(pick.Blocked.ID, console.Name)
		out["file"] = wittyplay.MissingBIOS(pick.Blocked.ID, systemDir)
	default:
		out["ready"], out["reason"], out["message"] = false, "core", "No core is installed for "+console.Name+"."
		out["runsOn"] = consoleRunsOn(console)
		if alt := a.installableCore(ctx, console); alt != nil {
			out["alternative"] = alt
			out["message"] = "No core is installed for " + console.Name + ". " + alt.Name + " would play it on this server."
		} else {
			// Not one of its cores is published for this platform: installing can't help here.
			out["noBuild"] = true
			out["message"] = "No core for " + console.Name + " is published for " + platformWords(runtime.GOOS, runtime.GOARCH) + ", so its games can't play on this server."
		}
	}
	return out
}

// consoleRunsOn is where a console's games play: each platform where one of its cores is
// published and can draw, with the best way it draws there.
func consoleRunsOn(console *wittyplay.Console) []platformSupport {
	rank := map[string]int{drawsGPU: 3, drawsCPU: 3, drawsCPUFallback: 2}
	var out []platformSupport
	for _, p := range buildPlatforms {
		best := platformSupport{OS: p.OS, Arch: p.Arch}
		for _, c := range console.Cores {
			has, known := coreBuildFor(c.ID, p.GOOS, p.GOARCH)
			if known && !has {
				continue
			}
			d, note := drawsOn(c.ID, wittyplay.Traits(c.ID).GPU, p.GOOS, "")
			if rank[d] > rank[best.Draws] {
				best.Build, best.Draws, best.Note = true, d, note
			}
		}
		if best.Build {
			out = append(out, withAPI(best))
		}
	}
	return out
}

// installableCore is the first core on offer for a console that's published for this server and
// can draw here: what installing would make play.
func (a *App) installableCore(ctx context.Context, console *wittyplay.Console) *coreAlternative {
	noGPU, asked := "", false
	for _, c := range console.Cores {
		if has, known := coreBuildFor(c.ID, runtime.GOOS, runtime.GOARCH); known && !has {
			continue
		}
		gpuCore := wittyplay.Traits(c.ID).GPU
		if gpuCore && !asked {
			noGPU, asked = a.GPUMissing(ctx), true
		}
		if d, _ := drawsHere(c.ID, gpuCore, noGPU); d == drawsNone {
			continue
		}
		return &coreAlternative{ID: c.ID, Name: c.Name, Console: console.Name}
	}
	return nil
}
