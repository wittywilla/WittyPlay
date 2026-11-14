// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/appcontrol"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// The cores on this machine, and the settings page's view of them (moved from the server's
// api_games.go, core_choices.go and core_notes.go): GET /gaming, installing and removing a core,
// emptying the unpacked games, and choosing which core a console or a game plays on. Everything
// here is for administrators (auth admin in integration.json, as requireAdmin was).

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /gaming", w(handleGaming))
		mux.Handle("POST /gaming/cores/{id}/install", w(handleCoreInstall))
		mux.Handle("GET /gaming/cores/{id}/install", w(handleCoreInstallProgress))
		mux.Handle("DELETE /gaming/cores/{id}", w(handleCoreRemove))
		mux.Handle("DELETE /gaming/unpacked", w(handleUnpackedClear))
		mux.Handle("GET /gaming/consoles/{console}/core", w(handleConsoleCoreGet))
		mux.Handle("PUT /gaming/consoles/{console}/core", w(handleConsoleCore))
		mux.Handle("GET /gaming/games/{id}/core", w(handleGameCoreGet))
		mux.Handle("PUT /gaming/games/{id}/core", w(handleGameCorePut))
	})
	// The game is gone from WittyPlay's tables with the server's account... but an item's choices
	// and plays are about the game, not a person: they stay until the core goes.
}

// ---- the engine's pieces on this machine ----

// engine is what the routes keep between requests: the cores, custom cores being added, the
// graphics card check, the BIOS finder and the standalone emulators' offers. One for each App.
type engine struct {
	cores  *cores.Manager
	custom *customService

	gpuMu       sync.Mutex
	gpu         stream.GPUProbe
	gpuAt       time.Time // when the last answer came in
	gpuChecking bool

	bios   biosFinder
	offers standaloneOffers

	machineMu sync.Mutex
	machine   map[string]bool // the server's Hello: what the machine allows
	machineAt time.Time

	consolesOnce sync.Once // custom consoles made known, the first time anything asks
}

var (
	enginesMu sync.Mutex
	engines   = map[*App]*engine{}
)

// extraCoreDirs are folders where RetroArch and system packages keep cores; tests have none.
var extraCoreDirs = cores.DefaultExtra

func (a *App) eng() *engine {
	enginesMu.Lock()
	e := engines[a]
	if e == nil {
		coresDir := filepath.Join(a.Dir, "cores")
		system := filepath.Join(a.Dir, "system")
		extra := extraCoreDirs()
		if extra == nil {
			extra = []string{}
		}
		e = &engine{}
		e.cores = &cores.Manager{Dir: coresDir, System: system, Extra: extra, Client: fetchClient, Custom: a.customCoreList}
		e.custom = newCustomService(filepath.Join(a.Dir, "custom-cores"), system, append([]string{coresDir}, extra...), fetchClient)
		engines[a] = e
	}
	enginesMu.Unlock()
	e.consolesOnce.Do(func() { a.publishCustomConsoles(context.Background()) })
	return e
}

// Cores is the cores on this machine: those installed here, RetroArch's, and approved custom
// cores while custom cores are on.
func (a *App) Cores() *cores.Manager { return a.eng().cores }

// SystemDir is the folder cores read BIOS files and their support files from (the server's
// <data>/gaming/system).
func (a *App) SystemDir() string {
	dir := filepath.Join(a.Dir, "system")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// testingGames: the built-in test console is only offered when the server says it's testing.
func testingGames() bool { return testCore.Load() || os.Getenv("WMS_GAMING_TESTCORE") == "1" }

// testCore: the server offers the built-in test console (its host hello's machine.gamingTestCore:
// a developer's server, or one started with WMS_GAMING_TESTCORE=1). The variable alone is for
// WittyPlay's own tests.
var testCore atomic.Bool

// unpackedCache is the unpacked games cache, which the play group's archives set: GET /gaming
// shows how much it holds, and DELETE /gaming/unpacked empties it. nil while there's none.
var unpackedCache func(*App) interface {
	Used() int64
	Clear()
}

// fetchClient fetches what an administrator asks for by address (a core to install, or one to
// add from a URL). It refuses anything private when it connects, because a name can resolve to
// anything, and has no proxy, which would only hide that: the server's SafeTransport with no
// proxy, as customFetchClient was.
var fetchClient = &http.Client{Timeout: 15 * time.Minute, Transport: &http.Transport{
	DialContext: (&net.Dialer{Timeout: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		h, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(h)
		if ip == nil || !onTheInternet(ip) {
			return fmt.Errorf("%s isn't on the internet", h)
		}
		return nil
	}}).DialContext,
	TLSHandshakeTimeout: 30 * time.Second,
}}

// notInternet are ranges that aren't the internet, beyond those the standard library names.
var notInternet = []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fec0::/10"}

// onTheInternet is what a fetched address has to be, including inside the IPv6 addresses that
// carry an IPv4 one for a gateway to unwrap.
func onTheInternet(ip net.IP) bool {
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		switch {
		case v6[0] == 0x00 && v6[1] == 0x64 && v6[2] == 0xff && v6[3] == 0x9b:
			ip = net.IP(v6[12:16])
		case v6[0] == 0x20 && v6[1] == 0x02:
			ip = net.IP(v6[2:6])
		case v6[0] == 0x20 && v6[1] == 0x01 && v6[2] == 0 && v6[3] == 0:
			return false // Teredo
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, s := range notInternet {
		if _, n, err := net.ParseCIDR(s); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

// isAdmin lets only an administrator through. The server checks it first (auth admin); this says
// so again, so a change to integration.json can't quietly open a route up.
func isAdmin(w http.ResponseWriter, r *http.Request) bool {
	if id := who(r); !id.Admin {
		writeErr(w, 403, "admin only")
		return false
	}
	return true
}

// ---- GET /gaming ----

// handleGaming is the settings page's view of what this server can do.
func handleGaming(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	ctx := r.Context()
	runner, _ := a.Runner(ctx)
	runnerName := ""
	if runner != "" {
		runnerName = strings.TrimSuffix(filepath.Base(runner), ".exe")
	}
	enc := []string{}
	if tools, err := a.Host.Tools(ctx); err == nil {
		for _, n := range []string{"libx264", "libopenh264", "libvpx", "libopus"} {
			if tools.Encoders[n] {
				enc = append(enc, n)
			}
		}
		for _, e := range hardwareEncoders(tools) {
			if e.Working {
				enc = append(enc, e.Name)
			}
		}
	}
	views := a.ConsoleViews(ctx)
	counts, _ := a.Host.Counts(ctx, host.For(r), "games", "console")
	systemDir := a.SystemDir()
	a.sweepBIOS(false) // what's found shows the next time this is asked
	records := a.biosRecords()
	for i := range views {
		v := &views[i]
		v.TotalGames = counts[v.ID]
		for _, b := range v.BIOS {
			if _, err := os.Stat(filepath.Join(systemDir, filepath.FromSlash(b.File))); err == nil || (b.Required && biosRuleMet(&v.Console, systemDir)) {
				v.BiosFound = append(v.BiosFound, b.File)
			}
		}
		v.BiosFiles = biosFilesFor(v.ID, systemDir, records, defaultBIOS(&v.Console, systemDir))
		if len(v.Installed) > 0 && v.Reason != "gpu" { // a BIOS wouldn't help
			console := v.Console
			if core, blocked := a.Cores().Playable(&console, v.DefaultCore, systemDir); core == nil && blocked != nil {
				v.Blocked = wittyplay.MissingBIOS(blocked.ID, systemDir)
			}
		}
	}
	var unpacked int64
	if unpackedCache != nil {
		if c := unpackedCache(a); c != nil {
			unpacked = c.Used()
		}
	}
	writeJSON(w, 200, map[string]any{
		"runner": runnerName, "encoders": enc, "consoles": views, "cores": a.Cores().List(), "canDownload": cores.CanDownload(),
		"coresDir": a.Cores().Dir, "systemDir": systemDir, "running": rpc.Mgr.Count(), "unpackedBytes": unpacked, "gpu": a.GPUStatus(ctx), "biosFolder": wittyplay.BIOSFolderName,
	})
}

// handleUnpackedClear empties the unpacked games cache, apart from what's being played.
func handleUnpackedClear(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	var used int64
	if unpackedCache != nil {
		if c := unpackedCache(a); c != nil {
			c.Clear()
			used = c.Used()
		}
	}
	writeJSON(w, 200, map[string]any{"unpackedBytes": used})
}

// ---- installing and removing ----

var coreIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// installCore installs a core from libretro's build server; tests stand in for it.
var installCore = func(ctx context.Context, m *cores.Manager, id string) (*cores.Core, error) { return m.Install(ctx, id) }

// POST /gaming/cores/{id}/install
func handleCoreInstall(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if !coreIDRe.MatchString(id) || !cores.Offered(id) {
		writeErr(w, 404, "this server does not offer that core")
		return
	}
	// Not published for this platform: said plainly, with what to use instead, rather than
	// found out from the download server's 404.
	if has, known := coreBuildFor(id, runtime.GOOS, runtime.GOARCH); known && !has {
		writeJSON(w, 409, noBuildRefusal(offeredCoreName(id), a.alternativeCore(r.Context(), id)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	c, err := installCore(ctx, a.Cores(), id)
	if err != nil {
		a.Log.WarnContext(r.Context(), "could not install a core", "cat", "games", "core", id, "err", err)
		if strings.Contains(err.Error(), "404") {
			// The table said it was there; the download server says not now.
			out := noBuildRefusal(offeredCoreName(id), a.alternativeCore(r.Context(), id))
			out["error"] = offeredCoreName(id) + " isn't on the download server for " + platformWords(runtime.GOOS, runtime.GOARCH) + " right now."
			if alt, ok := out["alternative"].(*coreAlternative); ok {
				out["error"] = out["error"].(string) + " " + alt.Name + " plays " + alt.Console + " games here instead."
			}
			writeJSON(w, 502, out)
			return
		}
		writeErr(w, 502, "The core could not be installed: "+err.Error())
		return
	}
	// The cores offered come unsigned from libretro's build server, and Windows' Smart App Control
	// wouldn't let one load: taken out again, with the reason, rather than left to fail a game.
	if runtime.GOOS == "windows" {
		lib := filepath.Join(a.Cores().Dir, id+"_libretro.dll")
		if err := appcontrol.CheckInstall(lib, "this core", "Signed game cores are planned; until then, games can't start on a PC with Smart App Control on."); err != nil {
			_ = a.Cores().Remove(id)
			a.Log.WarnContext(r.Context(), "core refused: Smart App Control", "cat", "games", "core", id)
			writeErr(w, 409, err.Error())
			return
		}
	}
	a.Log.InfoContext(r.Context(), "core installed", "cat", "games", "core", id, "sha256", c.SHA256)
	writeJSON(w, 200, c)
}

// GET /gaming/cores/{id}/install: how far an install of this core has got, while one is under
// way: bytes received and the download's size (0 when the download server didn't say).
func handleCoreInstallProgress(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if !coreIDRe.MatchString(id) {
		writeErr(w, 404, "not found")
		return
	}
	got, total, ok := a.Cores().InstallProgress(id)
	writeJSON(w, 200, map[string]any{"installing": ok, "received": got, "total": total})
}

// DELETE /gaming/cores/{id}
func handleCoreRemove(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if !coreIDRe.MatchString(id) {
		writeErr(w, 404, "not found")
		return
	}
	if err := a.Cores().Remove(id); err != nil {
		writeErr(w, 404, "this core isn't one this server installed")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- what each core is good at (the server's core_notes.go) ----

// What a core is chosen for, beside its console's other cores.
const (
	strengthAccuracy = "accuracy" // closest to the console, for more of the processor
	strengthSpeed    = "speed"    // lightest on the server, for slower machines
	strengthBalanced = "balanced" // between the two
)

// coreNote is one core's line, for choosing between a console's cores: in the core picker and
// in GET /games/consoles (coreAbout). Every core in corebuilds.txt has one (TestEveryCoreHasANote).
type coreNote struct {
	// Strength is accuracy, speed or balanced, beside its console's other cores; "" for a
	// console's only core.
	Strength string `json:"strength,omitempty"`
	About    string `json:"about"`
	// BIOS, when the core won't start without one of these files of the owner's own and its traits
	// don't already say so. Any one of them will do.
	BIOS []string `json:"-"`
}

var psxBIOS = []string{"scph5501.bin", "scph5500.bin", "scph5502.bin"}

var coreNotes = map[string]coreNote{
	// NES
	"fceumm":   {strengthBalanced, "Fast and compatible, with the widest support for unusual cartridges and the Famicom Disk System.", nil},
	"nestopia": {strengthAccuracy, "Accurate, and plays nearly every game, the Famicom Disk System too.", nil},
	"mesen":    {strengthAccuracy, "The most accurate, and plays every game. Uses more of the processor.", nil},
	"quicknes": {strengthSpeed, "The lightest, for slow servers. Plays the well-known games, not unusual cartridges.", nil},
	// Super Nintendo
	"mednafen_supafaust":    {strengthSpeed, "Fast and accurate for nearly every game, spread over several processor cores.", nil},
	"bsnes2014_performance": {strengthBalanced, "bsnes's lighter profile from 2014: accurate, and easy on most servers.", nil},
	"mesen-s":               {strengthAccuracy, "Very accurate, and the one that plays Satellaview games (with BS-X.bin).", nil},
	"bsnes":                 {strengthAccuracy, "Today's bsnes, the reference for accuracy, and the heaviest on the processor.", nil},
	// Game Boy, Game Boy Color
	"gambatte": {strengthAccuracy, "Very accurate, and light.", nil},
	"sameboy":  {strengthAccuracy, "The most accurate, with Super Game Boy borders and colours.", nil},
	"mgba":     {strengthBalanced, "Accurate and fast, with the most features.", nil},
	"gearboy":  {strengthSpeed, "Light and fast, with good compatibility.", nil},
	// Game Boy Advance
	"vba_next": {strengthSpeed, "Fast, for slower servers, and less accurate.", nil},
	"vbam":     {strengthBalanced, "VisualBoyAdvance-M: long-tested, with good compatibility.", nil},
	"gpsp":     {strengthSpeed, "The lightest, for low-power servers, and the least accurate. Has a BIOS of its own built in.", nil},
	// Mega Drive
	"blastem":    {strengthAccuracy, "Very accurate.", nil},
	"clownmdemu": {strengthBalanced, "A newer, accurate core.", nil},
	// Master System, Game Gear
	"gearsystem": {strengthAccuracy, "Accurate, for Master System, Game Gear and SG-1000.", nil},
	"smsplus":    {strengthSpeed, "Light and fast.", nil},
	// PlayStation
	"pcsx_rearmed":    {strengthSpeed, "Fast and light, and plays most games without a BIOS file.", nil},
	"mednafen_psx":    {strengthAccuracy, "The most accurate, drawn by the processor at the console's own size.", psxBIOS},
	"swanstation":     {strengthBalanced, "Fast, and sharper: drawn on a graphics card at higher resolutions.", psxBIOS},
	"mednafen_psx_hw": {strengthAccuracy, "Beetle PSX's accuracy, drawn on a graphics card at higher resolutions.", psxBIOS},
	// PC Engine
	"mednafen_pce_fast":   {strengthSpeed, "Fast, and plays CD games with your System Card file.", nil},
	"mednafen_pce":        {strengthAccuracy, "The full Beetle PCE: more accurate, and plays SuperGrafx games too.", nil},
	"mednafen_supergrafx": {strengthBalanced, "Made for the SuperGrafx, and plays PC Engine games as well.", nil},
	// Atari
	"stella2014":    {strengthSpeed, "Stella from 2014: light, and plays nearly every game.", nil},
	"stella":        {strengthAccuracy, "Today's Stella: the most accurate, with the newest fixes.", nil},
	"prosystem":     {"", "Plays Atari 7800 games, with the start-up screen when you add its BIOS.", nil},
	"handy":         {strengthSpeed, "Light, and plays most games; better with the Lynx's boot ROM.", nil},
	"mednafen_lynx": {strengthAccuracy, "Mednafen's version of Handy: more accurate.", []string{"lynxboot.img"}},
	// Handhelds
	"mednafen_wswan": {"", "Accurate WonderSwan and WonderSwan Color.", nil},
	"mednafen_ngp":   {strengthAccuracy, "Accurate Neo Geo Pocket and Color.", nil},
	"race":           {strengthSpeed, "Light and fast, with good compatibility.", nil},
	"mednafen_vb":    {"", "Accurate Virtual Boy.", nil},
	// Nintendo 64
	"mupen64plus_next": {strengthBalanced, "Plays most games, and sharper at higher resolutions on a graphics card.", nil},
	"parallel_n64":     {strengthAccuracy, "Its own graphics plugins, for games Mupen64Plus-Next draws wrongly. Plays libdragon homebrew too.", nil},
	// Nintendo DS
	"melonds": {strengthAccuracy, "Accurate, with the DS's own start-up menu when you add its firmware.", nil},
	"desmume": {strengthBalanced, "Long-tested, and plays most games.", nil},
	// Nintendo 3DS
	"azahar":   {strengthAccuracy, "Carries on from Citra, and plays the most games.", nil},
	"panda3ds": {"", "A newer core, which plays fewer games.", nil},
	// GameCube and Wii
	"dolphin": {strengthAccuracy, "Dolphin, as a core: plays GameCube and Wii games on the server's graphics card.", nil},
	// PlayStation 2
	"pcsx2": {strengthAccuracy, "LRPS2, made from PCSX2: plays the most games. Needs a BIOS from your console.", nil},
	"play":  {strengthBalanced, "Play!: needs no BIOS, and plays fewer games.", nil},
	// PSP, Dreamcast
	"ppsspp":  {"", "Plays nearly every PSP game, sharper at higher resolutions on a graphics card.", nil},
	"flycast": {"", "Plays Dreamcast games, sharper at higher resolutions on a graphics card.", nil},
}

// coreNoteFor is a core's line, or nothing for a core this table doesn't know, such as a custom one.
func coreNoteFor(id string) *coreNote {
	if n, ok := coreNotes[id]; ok {
		return &n
	}
	return nil
}

// ---- the core picker (the server's core_choices.go) ----

// The core picker: every core that can play a console, installed or not, with what each is good
// at, where it runs and how it draws, whether it needs a BIOS, and whether it can be installed
// here. One answer for a console's default (GET /gaming/consoles/{console}/core) and for one game
// (GET /gaming/games/{id}/core). Choosing one that isn't installed installs it first (POST
// /gaming/cores/{id}/install), then sets it with the PUT beside each GET, which only takes an
// installed core.

// coreChoice is one core in the picker.
type coreChoice struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	License       string `json:"license"`
	NonCommercial bool   `json:"nonCommercial,omitempty"`
	// Installed: it's on this server, and Source says from where (installed, retroarch, system or
	// custom). CanInstall: it isn't, and installing it here would work.
	Installed  bool   `json:"installed"`
	Source     string `json:"source,omitempty"`
	CanInstall bool   `json:"canInstall,omitempty"`
	// Recommended is the core this server would pick for the console: the first in its list that's
	// published for this platform and can draw here.
	Recommended bool `json:"recommended,omitempty"`
	// Strength and About: what it's good at (coreNotes). Neither is there for a custom core.
	Strength string `json:"strength,omitempty"`
	About    string `json:"about,omitempty"`
	// Support is where it runs and how it draws, here and on every platform, as in
	// GET /games/consoles. Not for a custom core.
	Support *coreSupport `json:"support,omitempty"`
	// BIOS, when it won't start without a file of the owner's own: any one of Files, and whether
	// this server has one.
	BIOS *coreBIOSNeed `json:"bios,omitempty"`
	// For one game: how the core has gone on it.
	Starts    int     `json:"starts,omitempty"`
	Failures  int     `json:"failures,omitempty"`
	Seconds   float64 `json:"seconds,omitempty"`
	LastError string  `json:"lastError,omitempty"`
	Best      bool    `json:"best,omitempty"`
}

type coreBIOSNeed struct {
	Files []string `json:"files"`
	Found bool     `json:"found"`
}

// coreChoicesView is the picker's answer.
type coreChoicesView struct {
	Console string `json:"console"`
	// Choice is what was chosen at this level: the game's own core, or the console's default; ""
	// for none. ConsoleDefault, for a game, is its console's.
	Choice         string `json:"choice"`
	ConsoleDefault string `json:"consoleDefault,omitempty"`
	// Effective is what plays now, after a missing BIOS or graphics card is taken into account;
	// "" when nothing would.
	Effective   string       `json:"effective"`
	CanDownload bool         `json:"canDownload"`
	Choices     []coreChoice `json:"choices"`
}

// coreChoices lists a console's cores for the picker; itemID is 0 for the console's default.
func (a *App) coreChoices(ctx context.Context, console *wittyplay.Console, itemID int64) coreChoicesView {
	systemDir := a.SystemDir()
	out := coreChoicesView{Console: console.ID, CanDownload: cores.CanDownload(), Choices: []coreChoice{}}
	prefs := a.PreferredCores(ctx)
	if itemID != 0 {
		out.Choice, out.ConsoleDefault = a.gameCoreChoice(ctx, itemID), prefs[console.ID]
	} else {
		out.Choice = prefs[console.ID]
	}
	if pick := a.PickCore(ctx, console, itemID); pick.Core != nil {
		out.Effective = pick.Core.ID
	}
	all := a.Cores().List()
	have := map[string]cores.Core{}
	for _, c := range all {
		if !c.Unapproved {
			have[c.ID] = c
		}
	}
	noGPU, asked := "", false
	recommended := false
	for _, want := range console.Cores {
		gpuCore := wittyplay.Traits(want.ID).GPU
		if gpuCore && !asked {
			noGPU, asked = a.GPUMissing(ctx), true
		}
		support := coreSupportFor(want.ID, gpuCore, noGPU)
		c := coreChoice{ID: want.ID, Name: want.Name, License: want.License, Support: &support}
		if got, ok := have[want.ID]; ok {
			c.Installed, c.Source = true, got.Source
		}
		playsHere := support.Here.Build && support.Here.Draws != drawsNone
		c.CanInstall = !c.Installed && out.CanDownload && playsHere && cores.Offered(want.ID)
		if playsHere && !recommended {
			c.Recommended, recommended = true, true
		}
		if n := coreNoteFor(want.ID); n != nil {
			c.Strength, c.About = n.Strength, n.About
		}
		c.BIOS = coreBIOS(want.ID, systemDir)
		out.Choices = append(out.Choices, c)
	}
	// An administrator's own cores for this console, which are installed by being added.
	for _, c := range all {
		if c.Source != cores.SourceCustom || c.Unapproved || !slices.Contains(c.Consoles, console.ID) || consoleListsCore(console, c.ID) {
			continue
		}
		out.Choices = append(out.Choices, coreChoice{ID: c.ID, Name: c.Name, License: c.License, NonCommercial: c.NonCommercial, Installed: true, Source: c.Source})
	}
	if itemID != 0 {
		options := make([]gameCoreOption, len(out.Choices))
		for i, c := range out.Choices {
			options[i].ID = c.ID
		}
		a.markBestCore(ctx, itemID, options)
		for i, o := range options {
			c := &out.Choices[i]
			c.Starts, c.Failures, c.Seconds, c.LastError, c.Best = o.Starts, o.Failures, o.Seconds, o.LastError, o.Best && c.Installed
		}
	}
	return out
}

// coreBIOS is the BIOS a core won't start without, from its traits or its note here, and
// whether this server has one; nil for a core that starts without.
func coreBIOS(id, systemDir string) *coreBIOSNeed {
	if rule := wittyplay.Traits(id).RequiredBIOS; rule != nil {
		return &coreBIOSNeed{Files: []string{rule.Example}, Found: wittyplay.MissingBIOS(id, systemDir) == ""}
	}
	n := coreNoteFor(id)
	if n == nil || len(n.BIOS) == 0 {
		return nil
	}
	need := &coreBIOSNeed{Files: n.BIOS}
	for _, f := range n.BIOS {
		if st, err := os.Stat(filepath.Join(systemDir, filepath.FromSlash(f))); err == nil && st.Mode().IsRegular() {
			need.Found = true
			break
		}
	}
	return need
}

func consoleListsCore(c *wittyplay.Console, id string) bool {
	for _, x := range c.Cores {
		if x.ID == id {
			return true
		}
	}
	return false
}

// coreFitsConsole: a core from the table for this console that's here, or an approved custom
// core assigned to it.
func (a *App) coreFitsConsole(console *wittyplay.Console, id string) bool {
	for _, c := range a.Cores().List() {
		if c.ID != id || c.Unapproved {
			continue
		}
		if c.Source == cores.SourceCustom {
			return slices.Contains(c.Consoles, console.ID) || consoleListsCore(console, id)
		}
		return consoleListsCore(console, id)
	}
	return false
}

func (a *App) gameCoreChoice(ctx context.Context, itemID int64) string {
	var id string
	_ = a.DB.QueryRowContext(ctx, `SELECT core_id FROM game_core_choice WHERE item_id = ?`, itemID).Scan(&id)
	return id
}

// GET /gaming/consoles/{console}/core: the console's cores for the picker, and its default.
func handleConsoleCoreGet(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	c := console(r.PathValue("console"))
	if c == nil {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, a.coreChoices(r.Context(), c, 0))
}

// PUT /gaming/consoles/{console}/core {core}: the core a console plays with unless a game says
// otherwise. An empty core goes back to the first one here.
func handleConsoleCore(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	c := console(r.PathValue("console"))
	if c == nil {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Core string `json:"core"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	if b.Core != "" && !a.coreFitsConsole(c, b.Core) {
		writeErr(w, 400, "that core isn't available for this console")
		return
	}
	prefs := a.PreferredCores(ctx)
	if b.Core == "" {
		delete(prefs, c.ID)
	} else {
		prefs[c.ID] = b.Core
	}
	if err := a.SetConfig(ctx, settingCores, prefs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.auditCore(ctx, host.For(r), "console-default", b.Core, c.ID)
	writeJSON(w, 200, map[string]any{"core": b.Core})
}

// gameCoreOption is a core that could play a game, and how it has gone so far.
type gameCoreOption struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Source        string  `json:"source"`
	License       string  `json:"license"`
	NonCommercial bool    `json:"nonCommercial,omitempty"`
	Starts        int     `json:"starts"`
	Failures      int     `json:"failures"`
	Seconds       float64 `json:"seconds"`
	LastError     string  `json:"lastError,omitempty"`
	Best          bool    `json:"best,omitempty"`
	// Here is how it draws on this server, for the cores this server offers.
	Here *platformSupport `json:"here,omitempty"`
}

// GET /gaming/games/{id}/core: what would play this game, and what else could: choices for the
// picker, and options, the installed ones.
func handleGameCoreGet(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	it := gameFromRoute(a, w, r)
	if it == nil {
		return
	}
	ctx := r.Context()
	options := []gameCoreOption{}
	c := wittyplay.ByID(it.Facets["console"])
	if c != nil {
		noGPU, asked := "", false
		for _, k := range a.Cores().List() {
			if !k.Unapproved && a.coreFitsConsole(c, k.ID) {
				o := gameCoreOption{ID: k.ID, Name: k.Name, Source: k.Source, License: k.License, NonCommercial: k.NonCommercial}
				if k.Source != cores.SourceCustom {
					gpuCore := wittyplay.Traits(k.ID).GPU
					if gpuCore && !asked {
						noGPU, asked = a.GPUMissing(ctx), true
					}
					here := coreSupportFor(k.ID, gpuCore, noGPU).Here
					o.Here = &here
				}
				options = append(options, o)
			}
		}
		a.markBestCore(ctx, it.ID, options)
	}
	out := map[string]any{"choice": a.gameCoreChoice(ctx, it.ID), "effective": "", "options": options, "choices": []coreChoice{}}
	if c != nil {
		v := a.coreChoices(ctx, c, it.ID)
		out["effective"], out["consoleDefault"], out["choices"], out["canDownload"], out["console"] = v.Effective, v.ConsoleDefault, v.Choices, v.CanDownload, v.Console
	}
	writeJSON(w, 200, out)
}

// markBestCore fills in how each core has done on this game, and marks the one that has done
// best: nothing has gone wrong with it, and it has been played the longest. A few seconds' play
// says nothing, so nothing is marked until there's a minute of evidence.
func (a *App) markBestCore(ctx context.Context, item int64, options []gameCoreOption) {
	rows, err := a.DB.QueryContext(ctx, `SELECT core_id, starts, failures, seconds, last_error FROM game_core_plays WHERE item_id = ?`, item)
	if err != nil {
		return
	}
	defer rows.Close()
	stats := map[string]gameCoreOption{}
	for rows.Next() {
		var id string
		var o gameCoreOption
		if rows.Scan(&id, &o.Starts, &o.Failures, &o.Seconds, &o.LastError) == nil {
			stats[id] = o
		}
	}
	best, score := -1, 0.0
	for i := range options {
		o := stats[options[i].ID]
		options[i].Starts, options[i].Failures, options[i].Seconds, options[i].LastError = o.Starts, o.Failures, o.Seconds, o.LastError
		if o.Starts == 0 || o.Seconds < 60 {
			continue
		}
		rate := 1 - float64(o.Failures)/float64(o.Starts)
		if rate < 0 {
			rate = 0
		}
		if v := rate * o.Seconds; v > score {
			best, score = i, v
		}
	}
	if best >= 0 {
		options[best].Best = true
	}
}

// PUT /gaming/games/{id}/core {core}: this game's own core; empty for the console's.
func handleGameCorePut(a *App, w http.ResponseWriter, r *http.Request) {
	if !isAdmin(w, r) {
		return
	}
	it := gameFromRoute(a, w, r)
	if it == nil {
		return
	}
	var b struct {
		Core string `json:"core"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	var err error
	if b.Core == "" {
		_, err = a.DB.ExecContext(ctx, `DELETE FROM game_core_choice WHERE item_id = ?`, it.ID)
	} else {
		c := wittyplay.ByID(it.Facets["console"])
		if c == nil || !a.coreFitsConsole(c, b.Core) {
			writeErr(w, 400, "that core isn't available for this game's console")
			return
		}
		_, err = a.DB.ExecContext(ctx, `INSERT INTO game_core_choice(item_id, core_id) VALUES(?,?) ON CONFLICT(item_id) DO UPDATE SET core_id = excluded.core_id`, it.ID, b.Core)
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.auditCore(ctx, host.For(r), "game-core", b.Core, fmt.Sprintf("item %d", it.ID))
	writeJSON(w, 200, map[string]any{"core": b.Core})
}
