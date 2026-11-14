// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// The consoles this server plays, as every client lists them (GET /games/consoles, whose answer
// the apps read: its shape stays exactly the server's), how a client can connect to a game
// (GET /games/modes), the standalone emulators set up for a game (GET /games/{id}/emulators), and
// what a client needs to lay out a console's games: its era, years, media, capabilities and how
// its games stand on a shelf. Moved from the server's api_games.go, games_standalone.go,
// game_traits.go and api_games_emulators.go.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/consoles", w(handleGameConsoles))
		mux.Handle("GET /games/modes", w(handleGameModes))
		mux.Handle("GET /games/{id}/emulators", w(handleGameEmulators))
	})
	// options.go's hooks: which consoles rewind by default, and the core a console's games use.
	rewindByDefault = consoleRewinds
	gameCore = func(ctx context.Context, a *App, consoleID string, item int64) (string, string, bool) {
		if consoleID == wittyplay.TestConsole {
			return "test", ipc.TestCore, true
		}
		c := console(consoleID)
		if c == nil {
			return "", "", false
		}
		core := a.Cores().ForConsole(c, a.PreferredCore(ctx, c, item))
		if core == nil {
			return "", "", false
		}
		// Asking a core what it offers loads it, which the same promise as playing it covers.
		if core.Source == cores.SourceCustom && a.CheckCustomCore(ctx, core) != nil {
			return core.ID, "", false
		}
		return core.ID, core.Path, true
	}
}

// ConsoleView is a console with what this server has for it.
type ConsoleView struct {
	wittyplay.Console
	Installed []cores.Core `json:"installed"`
	NeedsGPU  bool         `json:"needsGpu,omitempty"` // every core on offer draws with a graphics card
	GPUCores  []string     `json:"gpuCores,omitempty"`
	// For an administrator: approved custom cores assigned to it, and which core it uses unless a
	// game says otherwise.
	Custom      []cores.Core `json:"customCores,omitempty"`
	DefaultCore string       `json:"defaultCore,omitempty"`
	Ready       bool         `json:"ready"`
	// When it isn't ready: why, as a word (unavailable, core, gpu or emulator) and in words for
	// people. GPUMissing is the reason this server can't draw it, with reason gpu.
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	GPUMissing string `json:"gpuMissing,omitempty"`
	// Software: the processor draws its games, and Notice says what that means for how they run.
	Software bool   `json:"software,omitempty"`
	Notice   string `json:"notice,omitempty"`
	// SoftwareWhy, with Software: no-card (this server has none games can use) or card-fault (it
	// has one, and the core draws wrongly on it on this system).
	SoftwareWhy string `json:"softwareWhy,omitempty"`
	// PlaysOn is the core Play starts its games on, unless a game has its own; BIOSMissing, with
	// none, is the BIOS file the core it wants is missing.
	PlaysOn     string `json:"playsOn,omitempty"`
	BIOSMissing string `json:"biosMissing,omitempty"`
	// CoreSupport is, for each core on offer, the server platforms it runs on and how it draws on
	// each, and here. CoreAbout is what each is good at beside the console's others.
	CoreSupport map[string]coreSupport `json:"coreSupport,omitempty"`
	CoreAbout   map[string]coreNote    `json:"coreAbout,omitempty"`
	Pad         wittyplay.PadLayout    `json:"pad"`
	// Count: with ?library=<id>, how many games that library holds for this console.
	Count int `json:"count,omitempty"`
	// Era, years, how its games come and what it can do.
	Era          string               `json:"era,omitempty"`
	Launched     int                  `json:"launched,omitempty"`
	Ended        int                  `json:"ended,omitempty"`
	Media        string               `json:"media,omitempty"`
	Capabilities *ConsoleCapabilities `json:"capabilities,omitempty"`
	Shelf        *ShelfLook           `json:"shelf,omitempty"` // its case and cartridge, for the games home
	// For Settings, Consoles & cores: BIOS files in place, what's stopping it from starting if
	// that's a missing BIOS, and how many games every library holds for it.
	BiosFound  []string       `json:"biosFound,omitempty"`
	BiosFiles  []biosFileView `json:"biosFiles,omitempty"` // in the system folder, and where each came from
	Blocked    string         `json:"blocked,omitempty"`
	TotalGames int            `json:"totalGames,omitempty"`
	// LogoName is the name to ask the logo service for, kind "console".
	LogoName string `json:"logoName,omitempty"`
	// Emulators are the standalone emulators set up to play it here, and Standalone the one Play
	// starts when no core plays it.
	Emulators  []rpc.Offer `json:"standaloneEmulators,omitempty"`
	Standalone string      `json:"standalone,omitempty"`
}

// ConsoleViews are the consoles this server plays, with what it has for each: everything about
// them an administrator may see (hideCustom takes out what nobody else may).
func (a *App) ConsoleViews(ctx context.Context) []ConsoleView {
	all := a.Cores().List()
	byID := map[string]cores.Core{}
	for _, c := range all {
		byID[c.ID] = c
	}
	testing := testingGames()
	out := []ConsoleView{}
	prefs := a.PreferredCores(ctx)
	customOn := a.customCoresOn(ctx)
	var gpuWhy *string // asked once, and only if a console needs to know
	askGPU := func() string {
		if gpuWhy == nil {
			why := a.GPUMissing(ctx)
			gpuWhy = &why
		}
		return *gpuWhy
	}
	offers := a.ConsoleOffers(ctx)
	for _, c := range wittyplay.All() {
		if c.Hidden && !testing {
			continue
		}
		v := ConsoleView{Console: c, Installed: []cores.Core{}, Pad: wittyplay.Pad(c.ID), LogoName: consoleLogoName(c)}
		addTraits(&v)
		if v.Cores == nil {
			v.Cores = []wittyplay.Core{}
		}
		v.CoreSupport = map[string]coreSupport{}
		for _, want := range c.Cores {
			if got, ok := byID[want.ID]; ok {
				v.Installed = append(v.Installed, got)
			}
			gpuCore := wittyplay.Traits(want.ID).GPU
			noGPU := ""
			if gpuCore {
				v.GPUCores = append(v.GPUCores, want.ID)
				noGPU = askGPU()
			} else if gpuWhy != nil {
				noGPU = *gpuWhy
			}
			v.CoreSupport[want.ID] = coreSupportFor(want.ID, gpuCore, noGPU)
			if n := coreNoteFor(want.ID); n != nil {
				if v.CoreAbout == nil {
					v.CoreAbout = map[string]coreNote{}
				}
				v.CoreAbout[want.ID] = *n
			}
		}
		v.NeedsGPU = len(c.Cores) > 0 && len(v.GPUCores) == len(c.Cores)
		for _, got := range all {
			if got.Source == cores.SourceCustom && slices.Contains(got.Consoles, c.ID) {
				v.Custom = append(v.Custom, got)
				if !slices.ContainsFunc(v.Installed, func(x cores.Core) bool { return x.ID == got.ID }) {
					v.Installed = append(v.Installed, got)
				}
			}
		}
		v.DefaultCore = prefs[c.ID]
		// An administrator assigning a core has answered "not playable yet" for themselves.
		if len(v.Custom) > 0 {
			v.Unavailable = ""
		}
		if wittyplay.IsCustomConsole(c.ID) && !customOn && len(v.Installed) == 0 {
			v.Unavailable = "Custom cores are turned off on this server."
		}
		v.Ready = v.Unavailable == "" && (len(v.Installed) > 0 || c.ID == wittyplay.TestConsole)
		// Every core it could play on draws on a graphics card, and this server has none it can
		// use: the processor draws, where a core can have it, and otherwise its games can't play here.
		if c.ID != wittyplay.TestConsole && v.Unavailable == "" && a.drawsOnlyOnGPU(&v) {
			switch why := askGPU(); {
			case why == "":
				// A card, but the core it would start on draws wrongly on it on this system, and no
				// other installed core draws right on it (see PickCore).
				if v.Ready && len(v.Installed) > 0 && drawsWrongHere(v.Installed[0]) != "" && cpuOptions(v.Installed[0]) != nil &&
					(v.DefaultCore != "" || keepsOnCPU[v.Installed[0].ID] || rightOnTheCard(v.Installed, v.Installed[0], a.coreDrawsOnGPU) == nil) {
					v.Software, v.Notice = true, cardFaultNotice()
				}
			case canDrawOnCPU(&v):
				if v.Ready { // with nothing installed, a core it could be is still needed first
					v.Software, v.Notice = true, softwareNotice
				}
			default:
				v.Ready, v.Reason, v.Message, v.GPUMissing = false, "gpu", gpuMessage(c.Name, why), why
			}
		}
		switch v.Notice {
		case cardFaultNotice():
			v.SoftwareWhy = "card-fault"
		case softwareNotice:
			v.SoftwareWhy = "no-card"
		}
		// The core Play starts a game on, where the game has no core of its own, and the BIOS file
		// that core is missing, if any: what "What this server plays" says.
		if v.Ready && len(v.Installed) > 0 && c.ID != wittyplay.TestConsole {
			pick := a.PickCore(ctx, &c, 0)
			switch {
			case pick.Core != nil:
				v.PlaysOn = pick.Core.ID
			case pick.Blocked != nil:
				v.BIOSMissing = wittyplay.MissingBIOS(pick.Blocked.ID, a.SystemDir())
			}
		}
		if v.Ready || len(c.Cores) == 0 || len(offers[c.ID]) > 0 {
			applyStandalone(&v, offers[c.ID])
		}
		switch {
		case v.Ready || v.Reason != "":
		case v.Unavailable != "":
			v.Reason, v.Message = "unavailable", v.Unavailable
		default:
			v.Reason, v.Message = "core", "No core is installed for "+c.Name+"."
		}
		out = append(out, v)
	}
	return out
}

// canDrawOnCPU says one of the cores a console could play on here can have the processor draw:
// those installed for it, or with none installed, those on offer.
func canDrawOnCPU(v *ConsoleView) bool {
	if len(v.Installed) == 0 {
		return slices.ContainsFunc(v.Cores, func(c wittyplay.Core) bool { return cpuDrawing[c.ID] != nil })
	}
	return slices.ContainsFunc(v.Installed, func(c cores.Core) bool { return cpuOptions(c) != nil })
}

// drawsOnlyOnGPU says every core a console could play on here draws on a graphics card: those
// installed for it, or with none installed, those on offer.
func (a *App) drawsOnlyOnGPU(v *ConsoleView) bool {
	if len(v.Installed) == 0 {
		return v.NeedsGPU
	}
	for _, c := range v.Installed {
		if !a.coreDrawsOnGPU(c) {
			return false
		}
	}
	return true
}

// hideCustom takes out what an administrator chose to add, for everyone else: which custom cores
// there are, their versions and hashes. Whether a console is ready stays.
func hideCustom(views []ConsoleView) {
	for i := range views {
		views[i].Custom, views[i].DefaultCore = nil, ""
		views[i].Installed = slices.DeleteFunc(views[i].Installed, func(c cores.Core) bool { return c.Source == cores.SourceCustom })
	}
}

// GET /games/consoles: every console, for anyone signed in. ?library=<id> counts that library's
// games for each; without it, each console's games across every games library this profile can see.
func handleGameConsoles(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	views := a.ConsoleViews(ctx)
	if !who(r).Admin {
		hideCustom(views)
	}
	if id, err := strconv.ParseInt(r.URL.Query().Get("library"), 10, 64); err == nil && id != 0 {
		counts := a.libraryCounts(ctx, host.For(r), id)
		for i := range views {
			views[i].Count = counts[views[i].ID]
		}
	} else if counts, err := a.Host.Counts(ctx, host.For(r), "games", "console"); err == nil {
		for i := range views {
			views[i].TotalGames = counts[views[i].ID]
		}
	}
	writeJSON(w, 200, views)
}

// libraryCounts counts one library's games by console, as this profile sees them. The host API
// counts across every library of a kind, so one library's are counted here, a page at a time.
func (a *App) libraryCounts(ctx context.Context, on host.On, library int64) map[string]int {
	out := map[string]int{}
	const page = 500
	for offset := 0; offset < 50*page; offset += page {
		items, err := a.Host.Items(ctx, on, host.Query{Library: library, Kind: "game", Offset: offset, Limit: page})
		if err != nil {
			return out
		}
		for _, it := range items {
			if c := itemConsole(&it); c != "" {
				out[c]++
			}
		}
		if len(items) < page {
			break
		}
	}
	return out
}

// GET /games/modes?client=browser says how a kind of client can connect to a game here.
func handleGameModes(a *App, w http.ResponseWriter, r *http.Request) {
	if err := a.Configure(r.Context()); err != nil { // so that hardware encoders count
		a.Log.WarnContext(r.Context(), "could not set up games", "cat", "games", "err", err)
	}
	client := gameClient(r.URL.Query().Get("client"))
	writeJSON(w, 200, map[string]any{"client": client, "modes": rpc.Mgr.Modes(client)})
}

// gameClient: only a browser says it is one, so a mode that needs a browser never reaches an app
// that hasn't heard of this field.
func gameClient(v string) string {
	if v == stream.ClientBrowser {
		return v
	}
	return stream.ClientNative
}

// GET /games/{id}/emulators lists the standalone emulators set up to play this game. It's what a
// game's page asks before offering "Play with ...", so it never fails for a game the person can
// see: with the emulators off or none installed, the list is empty. Starting one is the play
// group's (POST /games/{id}/play/emulator).
func handleGameEmulators(a *App, w http.ResponseWriter, r *http.Request) {
	it := gameFromRoute(a, w, r)
	if it == nil {
		return
	}
	c := console(itemConsole(it))
	if c == nil || itemConsole(it) == "rig" {
		writeErr(w, 404, "not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := rpc.Docker.Offers(ctx, c.ID, a.SystemDir())
	if out == nil {
		out = []rpc.Offer{}
	}
	writeJSON(w, 200, map[string]any{"emulators": out})
}

// gameFromRoute is the game a route's {id} names, if the person may see it: the server has
// checked it (the route's "item") and says what it is; otherwise the server is asked. It answers
// 404 itself.
func gameFromRoute(a *App, w http.ResponseWriter, r *http.Request) *service.Item {
	id, ok := pathInt(r, "id")
	if ok {
		if it := who(r).Item; it != nil && it.ID == id {
			if it.Kind == "game" {
				return it
			}
		} else if it, err := a.visibleGame(r.Context(), r, id); err == nil {
			return it
		}
	}
	writeErr(w, 404, "not found")
	return nil
}

// ---- standalone emulators (the server's games_standalone.go) ----

// Consoles that standalone emulators play. GameCube and Wii have a core as well; Xbox, PS3 and
// Wii U have only standalone emulators, and the Switch only an emulator program its owner
// installed. Such a console is ready when one of its emulators is set up and ready here, and Play
// on its games starts that emulator.

// standaloneOffers keeps which emulators are set up for each console for a few seconds.
type standaloneOffers struct {
	mu   sync.Mutex
	at   time.Time
	last map[string][]rpc.Offer
}

const standaloneTTL = 5 * time.Second

// ConsoleOffers are the standalone emulators set up for each console here; empty with them off.
func (a *App) ConsoleOffers(ctx context.Context) map[string][]rpc.Offer {
	o := &a.eng().offers
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.last != nil && time.Since(o.at) < standaloneTTL {
		return o.last
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out := rpc.Docker.ConsoleOffers(cctx, a.SystemDir())
	if out == nil {
		out = map[string][]rpc.Offer{}
	}
	o.last, o.at = out, time.Now()
	return out
}

// ReadyEmulator is the first emulator ready to play a console here, or nil.
func ReadyEmulator(offers []rpc.Offer) *rpc.Offer {
	for i := range offers {
		if offers[i].Ready {
			return &offers[i]
		}
	}
	return nil
}

// StandaloneWords says what a console with no core needs, when nothing is set up to play it.
func StandaloneWords(c *wittyplay.Console, offers []rpc.Offer) string {
	for _, o := range offers {
		if o.Why != "" {
			return o.Why
		}
	}
	if len(c.Emulators) == 0 {
		return c.Name + " games play in an emulator program you install yourself. An administrator can add one in Settings, Standalone emulators, Your own emulator programs."
	}
	if c.ID == "switch" || c.ID == "switch2" {
		return c.Name + " games play in an emulator this server never downloads: an administrator installs it from its own project and adds it in Settings, Standalone emulators, with keys and firmware from their own console."
	}
	return c.Name + " games play in a standalone emulator. An administrator can install one in Settings, Standalone emulators."
}

// applyStandalone marks a console view ready when an emulator plays it here and no core does.
func applyStandalone(v *ConsoleView, offers []rpc.Offer) {
	v.Emulators = offers
	if v.Ready {
		return
	}
	if e := ReadyEmulator(offers); e != nil {
		v.Ready, v.Reason, v.Message, v.Standalone = true, "", "", e.ID
		return
	}
	if len(v.Cores) == 0 {
		v.Reason, v.Message = "emulator", StandaloneWords(&v.Console, offers)
	}
}

// consoleLogoName is the name to ask the logo service for, kind "console": what the server's
// metadata calls it (library.ConsoleLogoName), or its name.
func consoleLogoName(c wittyplay.Console) string {
	if n, ok := consoleLogoNames[c.ID]; ok {
		return n
	}
	return c.Name
}

var consoleLogoNames = map[string]string{
	"nes": "Nintendo Entertainment System", "snes": "Super Nintendo Entertainment System", "gb": "Game Boy", "gba": "Game Boy Advance",
	"md": "Sega Genesis", "sms": "Master System", "gg": "Game Gear", "ps1": "PlayStation", "ps2": "PlayStation 2", "psp": "PlayStation Portable",
	"pce": "TurboGrafx-16", "a2600": "Atari 2600", "a7800": "Atari 7800", "lynx": "Atari Lynx", "ws": "WonderSwan", "ngp": "Neo Geo Pocket",
	"vb": "Virtual Boy", "n64": "Nintendo 64", "nds": "Nintendo DS", "3ds": "Nintendo 3DS", "dc": "Dreamcast",
}

// ---- what a client lays out a console's games with (the server's game_traits.go) ----

// What every client needs to know about a console to lay out its games and build its game menu:
// its era and years for the shelves and the Eras page, how its games come (cartridge, handheld
// cartridge, disc), and its capabilities: screens, input, media, play and picture. A client builds
// its menus from these and leaves out what a console doesn't have, so a new console needs a row
// here, not new screens. Port counts, accessories and options are what the usual core for a
// console offers; the session says what the running core really has.

// Eras, oldest first. Clients name them.
const (
	eraFirst   = "first"    // before the 8-bit consoles
	era8Bit    = "8-bit"    // NES, Master System
	era16Bit   = "16-bit"   // SNES, Mega Drive
	eraEarly3D = "early-3d" // the first consoles drawn in 3D
	eraDisc    = "disc"     // the disc consoles after them
	eraPocket  = "pocket"   // handhelds, of any year
)

// How a console's games come, which sets the shape of their spines on a shelf.
const (
	mediaCart   = "cart"
	mediaPocket = "pocket"
	mediaDisc   = "disc"
)

// ConsoleCapabilities is everything a game menu, a player and a controller screen build from.
type ConsoleCapabilities struct {
	Screens ScreenCaps  `json:"screens"`
	Input   InputCaps   `json:"input"`
	Media   MediaCaps   `json:"media"`
	Play    PlayCaps    `json:"play"`
	Picture PictureCaps `json:"picture"`
}

// ScreenCaps: one screen, or two (DS, 3DS), and how two can be arranged.
type ScreenCaps struct {
	Count int `json:"count"`
	// Touch is the screen that takes touch, counted from 1, or 0 for none.
	Touch int `json:"touch,omitempty"`
	// Layouts a client can offer, in order: stacked, side, big (one big, the other small),
	// single (one at a time).
	Layouts []string `json:"layouts,omitempty"`
	// Options are how each core sets those layouts, by core ID.
	Options map[string]ScreenOptions `json:"options,omitempty"`
	// Frame is where each screen is in a frame of the stacked layout the core is started in, as
	// fractions of the frame, so a client can draw each where the person placed it (Edit layout).
	Frame []ScreenRect `json:"frame,omitempty"`
}

// ScreenRect is one screen's part of a frame.
type ScreenRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// ScreenOptions: the core option that picks a layout, and the value for each of ours, as it is and
// with the two screens swapped. A core that swaps with an option of its own names that too.
type ScreenOptions struct {
	Layout     string               `json:"layout"`
	Values     map[string][2]string `json:"values"`
	Swap       string               `json:"swap,omitempty"`
	SwapValues *[2]string           `json:"swapValues,omitempty"`
}

// InputCaps: what a player can hold. The console's own buttons are in its pad
// (GET /consoles/{id}/controls).
type InputCaps struct {
	Ports          int  `json:"ports"`
	Multitap       int  `json:"multitap,omitempty"` // how many players with an adapter
	Sticks         int  `json:"sticks,omitempty"`
	AnalogTriggers bool `json:"analogTriggers,omitempty"`
	Touch          bool `json:"touch,omitempty"` // a touch screen: pointer states go to it
	LightGun       bool `json:"lightGun,omitempty"`
	Mouse          bool `json:"mouse,omitempty"`
	Motion         bool `json:"motion,omitempty"`
	Keyboard       bool `json:"keyboard,omitempty"`
	Mic            bool `json:"mic,omitempty"`
}

// MediaCaps: discs, memory cards, BIOS and region.
type MediaCaps struct {
	Discs       bool `json:"discs,omitempty"` // games can come on several discs, changed from the game menu
	MemoryCards int  `json:"memoryCards,omitempty"`
	// BIOS is none, optional or required, from the console's list of BIOS files.
	BIOS string `json:"bios"`
	// Region is the core option that picks the console's region, by core ID.
	Region map[string]string `json:"region,omitempty"`
}

// PlayCaps: what a game menu can offer while playing.
type PlayCaps struct {
	Rumble bool `json:"rumble,omitempty"`
	// RewindByDefault: rewind is on for these games unless someone turns it off. A console's
	// states have to be small enough to keep 20 seconds of them in the memory an
	// administrator allows (gamingRewindMB).
	RewindByDefault bool `json:"rewindByDefault,omitempty"`
}

// PictureCaps: the picture's shape and the Look it starts with.
type PictureCaps struct {
	Aspect  string `json:"aspect"`
	Integer bool   `json:"integer,omitempty"` // whole-pixel scaling suits it
	// Look is the display preset a console starts with until someone chooses: Clean,
	// Scanlines or Handheld LCD (GET /games/settings/schema, presets).
	Look string `json:"look"`
}

// consoleTraits is one console's row.
type consoleTraits struct {
	Era      string
	Launched int
	Ended    int
	Media    string
	Caps     ConsoleCapabilities
}

const (
	lookClean     = "Clean"
	lookScanlines = "Scanlines"
	lookLCD       = "Handheld LCD"
)

// The two-screen layouts, and how each core sets them.
var (
	twoScreenLayouts = []string{"stacked", "side", "big", "single"}
	melonDSScreens   = ScreenOptions{
		Layout: "melonds_screen_layout",
		Values: map[string][2]string{
			"stacked": {"Top/Bottom", "Bottom/Top"},
			"side":    {"Left/Right", "Right/Left"},
			"big":     {"Hybrid Top", "Hybrid Bottom"},
			"single":  {"Top Only", "Bottom Only"},
		},
	}
	azaharScreens = ScreenOptions{
		Layout: "citra_layout_option",
		Values: map[string][2]string{
			"stacked": {"default", "default"},
			"side":    {"side_by_side", "side_by_side"},
			"big":     {"large_screen", "large_screen"},
			"single":  {"single_screen", "single_screen"},
		},
		Swap:       "citra_swap_screen",
		SwapValues: &[2]string{"Top", "Bottom"},
	}
)

func caps(ports int, look, aspect string, integer bool) ConsoleCapabilities {
	return ConsoleCapabilities{
		Screens: ScreenCaps{Count: 1},
		Input:   InputCaps{Ports: ports},
		Picture: PictureCaps{Aspect: aspect, Integer: integer, Look: look},
	}
}

// Years are when each first went on sale and when it stopped being made, roughly: they place
// a console on the Eras page, nothing more.
var consoleTraitsTable = map[string]consoleTraits{
	"a2600": {Era: eraFirst, Launched: 1977, Ended: 1992, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn)},
	"a7800": {Era: era8Bit, Launched: 1986, Ended: 1992, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn)},
	"nes": {Era: era8Bit, Launched: 1983, Ended: 1995, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Input.LightGun = 4, true
	})},
	"sms": {Era: era8Bit, Launched: 1985, Ended: 1992, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn, func(c *ConsoleCapabilities) {
		c.Input.LightGun = true
	})},
	"snes": {Era: era16Bit, Launched: 1990, Ended: 2003, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Input.LightGun, c.Input.Mouse = 5, true, true
	})},
	"md": {Era: era16Bit, Launched: 1988, Ended: 1997, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Input.LightGun, c.Input.Mouse = 4, true, true
	})},
	"pce": {Era: era16Bit, Launched: 1987, Ended: 1994, Media: mediaCart, Caps: withCaps(caps(2, lookScanlines, "4:3", true), rewindOn, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Media.Discs = 5, true
	})},
	"gb":   {Era: eraPocket, Launched: 1989, Ended: 2003, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "10:9", true), rewindOn)},
	"gba":  {Era: eraPocket, Launched: 2001, Ended: 2010, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "3:2", true), rewindOn, rumble)},
	"gg":   {Era: eraPocket, Launched: 1990, Ended: 1997, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "10:9", true), rewindOn)},
	"lynx": {Era: eraPocket, Launched: 1989, Ended: 1995, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "160:102", true), rewindOn)},
	"ws":   {Era: eraPocket, Launched: 1999, Ended: 2003, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "14:9", true), rewindOn)},
	"ngp":  {Era: eraPocket, Launched: 1998, Ended: 2001, Media: mediaPocket, Caps: withCaps(caps(1, lookLCD, "20:19", true), rewindOn)},
	"vb":   {Era: eraPocket, Launched: 1995, Ended: 1996, Media: mediaPocket, Caps: withCaps(caps(1, lookClean, "384:224", true), rewindOn)},
	"n64": {Era: eraEarly3D, Launched: 1996, Ended: 2002, Media: mediaCart, Caps: withCaps(caps(4, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks = 1
	})},
	"ps1": {Era: eraEarly3D, Launched: 1994, Ended: 2006, Media: mediaDisc, Caps: withCaps(caps(2, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Input.Sticks, c.Input.LightGun, c.Input.Mouse = 8, 2, true, true
		c.Media.Discs, c.Media.MemoryCards = true, 2
		c.Media.Region = map[string]string{"pcsx_rearmed": "pcsx_rearmed_region"}
	})},
	"dc": {Era: eraDisc, Launched: 1998, Ended: 2001, Media: mediaDisc, Caps: withCaps(caps(4, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.AnalogTriggers, c.Input.LightGun, c.Input.Mouse, c.Input.Keyboard = 1, true, true, true, true
		c.Media.Discs = true
	})},
	"ps2": {Era: eraDisc, Launched: 2000, Ended: 2013, Media: mediaDisc, Caps: withCaps(caps(2, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Multitap, c.Input.Sticks = 8, 2
		c.Media.Discs, c.Media.MemoryCards = true, 2
	})},
	"psp": {Era: eraPocket, Launched: 2004, Ended: 2014, Media: mediaPocket, Caps: withCaps(caps(1, lookClean, "16:9", false), func(c *ConsoleCapabilities) {
		c.Input.Sticks = 1
	})},
	"nds": {Era: eraPocket, Launched: 2004, Ended: 2014, Media: mediaPocket, Caps: withCaps(caps(1, lookClean, "4:3", true), func(c *ConsoleCapabilities) {
		c.Screens = ScreenCaps{Count: 2, Touch: 2, Layouts: twoScreenLayouts, Options: map[string]ScreenOptions{"melonds": melonDSScreens},
			Frame: []ScreenRect{{0, 0, 1, 0.5}, {0, 0.5, 1, 0.5}}} // Top/Bottom, no gap: the two halves
		c.Input.Touch, c.Input.Mic = true, true
	})},
	"3ds": {Era: eraPocket, Launched: 2011, Ended: 2020, Media: mediaPocket, Caps: withCaps(caps(1, lookClean, "5:3", false), func(c *ConsoleCapabilities) {
		c.Screens = ScreenCaps{Count: 2, Touch: 2, Layouts: twoScreenLayouts, Options: map[string]ScreenOptions{"azahar": azaharScreens},
			Frame: []ScreenRect{{0, 0, 1, 0.5}, {0.1, 0.5, 0.8, 0.5}}} // the default layout: the bottom screen (320) centred under the top (400)
		c.Input.Touch, c.Input.Sticks, c.Input.Motion, c.Input.Mic = true, 1, true, true
		c.Media.Region = map[string]string{"azahar": "citra_region_value"}
	})},
	// Played by Dolphin's core or a standalone emulator (games_standalone.go). The Switch is a
	// home console here for its shelf, and a pocket one only in how it's held.
	"gc": {Era: eraDisc, Launched: 2001, Ended: 2007, Media: mediaDisc, Caps: withCaps(caps(4, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.AnalogTriggers = 2, true
		c.Media.Discs, c.Media.MemoryCards = true, 2
	})},
	"wii": {Era: eraDisc, Launched: 2006, Ended: 2013, Media: mediaDisc, Caps: withCaps(caps(4, lookClean, "16:9", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.Motion, c.Input.LightGun = 1, true, true
		c.Media.Discs = true
	})},
	"wiiu": {Era: eraDisc, Launched: 2012, Ended: 2017, Media: mediaDisc, Caps: withCaps(caps(1, lookClean, "16:9", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.Touch, c.Input.Motion, c.Input.Mic = 2, true, true, true
		c.Media.Discs = true
	})},
	"ps3": {Era: eraDisc, Launched: 2006, Ended: 2017, Media: mediaDisc, Caps: withCaps(caps(2, lookClean, "16:9", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.Motion = 2, true
		c.Media.Discs = true
	})},
	"xbox": {Era: eraDisc, Launched: 2001, Ended: 2006, Media: mediaDisc, Caps: withCaps(caps(4, lookClean, "4:3", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.AnalogTriggers = 2, true
		c.Media.Discs = true
	})},
	"switch": {Era: eraDisc, Launched: 2017, Media: mediaCart, Caps: withCaps(caps(1, lookClean, "16:9", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.Touch, c.Input.Motion = 2, true, true
	})},
	"switch2": {Era: eraDisc, Launched: 2025, Media: mediaCart, Caps: withCaps(caps(1, lookClean, "16:9", false), rumble, func(c *ConsoleCapabilities) {
		c.Input.Sticks, c.Input.Touch, c.Input.Motion = 2, true, true
	})},
}

func withCaps(c ConsoleCapabilities, more ...func(*ConsoleCapabilities)) ConsoleCapabilities {
	for _, f := range more {
		f(&c)
	}
	return c
}

func rewindOn(c *ConsoleCapabilities) { c.Play.RewindByDefault = true }
func rumble(c *ConsoleCapabilities)   { c.Play.Rumble = true }

// traitsFor gives a console's row, or nil for one this table doesn't know (a custom console
// takes the row of the console whose pad it copies, if any).
func traitsFor(c wittyplay.Console) *consoleTraits {
	if t, ok := consoleTraitsTable[c.ID]; ok {
		return &t
	}
	if c.PadLike != "" {
		if t, ok := consoleTraitsTable[c.PadLike]; ok {
			return &t
		}
	}
	return nil
}

// biosNeed sums up a console's BIOS list as none, optional or required.
func biosNeed(c wittyplay.Console) string {
	need := "none"
	for _, b := range c.BIOS {
		if b.Required {
			return "required"
		}
		need = "optional"
	}
	return need
}

// addTraits fills in a console view's era, years, media and capabilities.
func addTraits(v *ConsoleView) {
	t := traitsFor(v.Console)
	if t == nil {
		return
	}
	v.Era, v.Launched, v.Ended, v.Media = t.Era, t.Launched, t.Ended, t.Media
	c := t.Caps
	c.Media.BIOS = biosNeed(v.Console)
	if v.Pad.Pointer {
		c.Input.Touch = true
	}
	v.Capabilities = &c
	v.Shelf = shelfLookFor(v.Console)
}

// consoleRewinds: the consoles whose games rewind unless someone says otherwise (options.go's
// rewindByDefault, which this sets).
func consoleRewinds(console string) bool {
	if t, ok := consoleTraitsTable[console]; ok {
		return t.Caps.Play.RewindByDefault
	}
	// Only a custom console (its ID starts custom-) can borrow another's row.
	if !strings.HasPrefix(console, "custom-") {
		return false
	}
	if c := wittyplay.ByID(console); c != nil {
		if t := traitsFor(*c); t != nil {
			return t.Caps.Play.RewindByDefault
		}
	}
	return false
}

// ShelfLook is how a console's games stand on a shelf: its case, and its cartridge or disc, as
// shapes a client draws itself around the game's own cover. They follow the console's real forms
// in proportion and material (a cardboard box, a plastic clamshell, a CD jewel case, a DVD-style
// keep case, a cartridge, a card, a disc), never a maker's banners, logos, colours or exact outline.
type ShelfLook struct {
	Case ShelfShape `json:"case"`
	Cart ShelfShape `json:"cart"`
}

// ShelfShape: Kind is what it is; Aspect its front's width over height; Height how tall it stands
// beside the others of its kind (a full-size box or keep case is 1, a handheld's box about 0.6).
type ShelfShape struct {
	Kind   string  `json:"kind"`
	Aspect float64 `json:"aspect"`
	Height float64 `json:"height"`
}

// Case kinds: box (cardboard), clamshell (plastic, cartridge era), jewel (CD), keep (DVD-style).
// Cartridge kinds: cart, card (HuCard, DS and 3DS cards), disc.
var shelfLooks = map[string]ShelfLook{
	"a2600":   {Case: ShelfShape{"box", 0.72, 1}, Cart: ShelfShape{"cart", 0.85, 0.75}},
	"a7800":   {Case: ShelfShape{"box", 0.72, 1}, Cart: ShelfShape{"cart", 0.85, 0.75}},
	"nes":     {Case: ShelfShape{"box", 0.71, 1}, Cart: ShelfShape{"cart", 0.89, 1}},
	"sms":     {Case: ShelfShape{"clamshell", 0.71, 1}, Cart: ShelfShape{"cart", 0.8, 0.85}},
	"snes":    {Case: ShelfShape{"box", 1.39, 0.68}, Cart: ShelfShape{"cart", 1.18, 0.82}},
	"md":      {Case: ShelfShape{"clamshell", 0.71, 1}, Cart: ShelfShape{"cart", 0.83, 0.88}},
	"pce":     {Case: ShelfShape{"jewel", 1, 0.66}, Cart: ShelfShape{"card", 1.58, 0.45}},
	"gb":      {Case: ShelfShape{"box", 0.96, 0.6}, Cart: ShelfShape{"cart", 0.88, 0.5}},
	"gba":     {Case: ShelfShape{"box", 0.96, 0.6}, Cart: ShelfShape{"cart", 1.6, 0.45}},
	"gg":      {Case: ShelfShape{"clamshell", 0.75, 0.75}, Cart: ShelfShape{"cart", 1.28, 0.5}},
	"lynx":    {Case: ShelfShape{"box", 0.96, 0.6}, Cart: ShelfShape{"card", 1.4, 0.45}},
	"ws":      {Case: ShelfShape{"box", 0.96, 0.6}, Cart: ShelfShape{"cart", 1.4, 0.45}},
	"ngp":     {Case: ShelfShape{"box", 0.96, 0.6}, Cart: ShelfShape{"cart", 1.3, 0.45}},
	"vb":      {Case: ShelfShape{"box", 1.39, 0.68}, Cart: ShelfShape{"cart", 0.9, 0.7}},
	"n64":     {Case: ShelfShape{"box", 1.39, 0.68}, Cart: ShelfShape{"cart", 1.12, 0.78}},
	"ps1":     {Case: ShelfShape{"jewel", 1, 0.66}, Cart: ShelfShape{"disc", 1, 0.66}},
	"dc":      {Case: ShelfShape{"jewel", 1, 0.66}, Cart: ShelfShape{"disc", 1, 0.66}},
	"ps2":     {Case: ShelfShape{"keep", 0.71, 1}, Cart: ShelfShape{"disc", 1, 0.66}},
	"psp":     {Case: ShelfShape{"keep", 0.58, 0.95}, Cart: ShelfShape{"disc", 1, 0.45}},
	"nds":     {Case: ShelfShape{"keep", 0.91, 0.72}, Cart: ShelfShape{"card", 0.94, 0.45}},
	"3ds":     {Case: ShelfShape{"keep", 0.91, 0.72}, Cart: ShelfShape{"card", 0.94, 0.45}},
	"gc":      {Case: ShelfShape{"keep", 0.72, 0.95}, Cart: ShelfShape{"disc", 1, 0.45}},
	"wii":     {Case: ShelfShape{"keep", 0.71, 1}, Cart: ShelfShape{"disc", 1, 0.66}},
	"wiiu":    {Case: ShelfShape{"keep", 0.71, 1}, Cart: ShelfShape{"disc", 1, 0.66}},
	"ps3":     {Case: ShelfShape{"keep", 0.87, 0.9}, Cart: ShelfShape{"disc", 1, 0.66}},
	"xbox":    {Case: ShelfShape{"keep", 0.71, 1}, Cart: ShelfShape{"disc", 1, 0.66}},
	"switch":  {Case: ShelfShape{"keep", 0.62, 0.9}, Cart: ShelfShape{"card", 0.72, 0.4}},
	"switch2": {Case: ShelfShape{"keep", 0.62, 0.9}, Cart: ShelfShape{"card", 0.72, 0.4}},
}

func shelfLookFor(c wittyplay.Console) *ShelfLook {
	if l, ok := shelfLooks[c.ID]; ok {
		return &l
	}
	if l, ok := shelfLooks[c.PadLike]; ok && c.PadLike != "" {
		return &l
	}
	return nil
}
