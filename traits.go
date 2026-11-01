package wittyplay

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// What a core needs beyond its file. Licences are in the console table; this is
// how a core is run. Every option here is only applied when the core offers that
// exact choice, so a core that changes its options falls back to its own default
// rather than failing.

// CoreTraits describes one core.
type CoreTraits struct {
	// GPU cores draw with a graphics card. They count against the GPU limit and
	// their runner gets what a graphics driver needs. docs/GAMING.md has the list.
	GPU bool
	// Demanding cores can't play at low priority on a machine doing anything else.
	// Everything drawn on a graphics card is, as well as these.
	Demanding bool
	// Assets is the name of the core's support files on the build server, for
	// cores that won't run without them. They go in the system folder.
	Assets string
	// RequiredBIOS is set when the core can't start without a BIOS file.
	RequiredBIOS *BIOSRule
	// SavesIn is a folder under the system folder where the core keeps saves, such as
	// memory cards. A running core can't write to the system folder, which everyone
	// shares, so this core gets a system folder of its own in the player's save folder.
	SavesIn string
	// SharedMemory is set when the core can't run without making files in /dev/shm, as a
	// core that maps its recompiled code twice can't.
	SharedMemory bool
	// LargeAddressSpace is set when the core reserves far more address space than a game
	// normally gets: reservations, not memory, which stays limited as for any other game.
	LargeAddressSpace bool
	// OwnHome is set when the core won't start without a home folder it can write to. It gets
	// its save folder as one, which is where what it keeps there belongs.
	OwnHome bool
	// CacheIn is a folder under the system folder where the core keeps what it has worked out,
	// such as compiled shaders, kept between games for a core with a system folder of its own.
	CacheIn string
	// Options are always passed. Each one makes the core use what this runner has.
	Options map[string]string
	// Threaded are options for a machine with threads to spare, of four or more.
	Threaded map[string]string
	// Resolution gives the options for an internal resolution, 1 to 4 times native.
	Resolution func(times int) map[string]string
}

// BIOSRule says where a core looks for its BIOS and what one looks like, so the
// server can say exactly what is missing and find one left beside the games.
type BIOSRule struct {
	Folder  string         // under the system folder
	Example string         // a file name to mention
	For     string         // who it's from, for the message
	Match   *regexp.Regexp // file names, lower case
	MinSize int64
	MaxSize int64
	// Option is the core option naming which BIOS to use, when it finds several.
	Option string
}

// One choice per resolution multiple, from a list of what the core offers.
func pick(key string, choices ...string) func(int) map[string]string {
	return func(times int) map[string]string {
		if times < 1 {
			times = 1
		}
		if times > len(choices) {
			times = len(choices)
		}
		return map[string]string{key: choices[times-1]}
	}
}

// The file name and size of a PlayStation 2 BIOS image, whichever console it came from.
var ps2BIOS = &BIOSRule{
	Folder:  "pcsx2/bios",
	Example: "scph39001.bin",
	For:     "a PlayStation 2 you own",
	Match:   regexp.MustCompile(`^(scph.*|ps2.*bios.*|(.*[^a-z])?bios([^a-z].*)?)\.(bin|rom0|rom)$`),
	Option:  "pcsx2_bios",
	MinSize: 4 << 20,
	MaxSize: (4 << 20) + (1 << 20),
}

var traits = map[string]CoreTraits{
	"mupen64plus_next": {GPU: true, Options: map[string]string{"mupen64plus-rdp-plugin": "gliden64", "mupen64plus-rsp-plugin": "hle"},
		Resolution: pick("mupen64plus-43screensize", "640x480", "1280x960", "1920x1440", "2560x1920")},
	"parallel_n64": {GPU: true, Options: map[string]string{"parallel-n64-gfxplugin": "gliden64"},
		Resolution: pick("parallel-n64-screensize", "640x480", "1280x960", "1920x1440", "2880x2160")},
	"pcsx2": {GPU: true, Assets: "LRPS2", RequiredBIOS: ps2BIOS, SavesIn: "pcsx2/memcards", CacheIn: "pcsx2/cache", SharedMemory: true,
		// Waiting for the graphics card at every read-back, and running VU1 on the game's own
		// thread, held a real game to 41 frames a second where it can do 60. The core's own
		// database still turns VU1's thread off for the games known to hang with it.
		Options:    map[string]string{"pcsx2_renderer": "OpenGL", "pcsx2_hw_download_mode": "Unsynchronized"},
		Threaded:   map[string]string{"pcsx2_mtvu": "enabled"},
		Resolution: pick("pcsx2_upscale_multiplier", "1x (Native)", "2x", "4x", "4x")},
	// Play! keeps its settings and memory cards under the home folder, and stops if it
	// can't make one.
	"play": {GPU: true, OwnHome: true},
	// PPSSPP lays out the PSP's memory through shared memory too, and can't boot without it.
	"ppsspp": {GPU: true, Assets: "PPSSPP", SharedMemory: true, Options: map[string]string{"ppsspp_backend": "opengl"},
		Resolution: pick("ppsspp_internal_resolution", "480x272", "960x544", "1440x816", "1920x1088")},
	// Left to itself, Flycast keeps memory cards in the system folder, which a running core
	// can't write to, so nothing a game saved to one would last. These are in the save folder.
	// It also lays out the console's memory through shared memory, where it can.
	"flycast": {GPU: true, SharedMemory: true, Options: map[string]string{"reicast_alpha_sorting": "per-triangle (normal)", "reicast_per_content_vmus": "All VMUs"},
		Resolution: pick("reicast_internal_resolution", "640x480", "1280x960", "1920x1440", "2560x1920")},
	// SwanStation lays out the PlayStation's memory through shared memory, and can't start
	// without it.
	"swanstation": {GPU: true, SharedMemory: true, Options: map[string]string{"swanstation_GPU_Renderer": "OpenGL"},
		Resolution: pick("swanstation_GPU_ResolutionScale", "1", "2", "3", "4")},
	"mednafen_psx_hw": {GPU: true, Options: map[string]string{"beetle_psx_hw_renderer": "hardware_gl"},
		Resolution: pick("beetle_psx_hw_internal_resolution", "1x(native)", "2x", "4x", "4x")},
	// The DS cores read the stylus from the pointer device. Their software renderers
	// are all a DS needs, so these need no graphics card at all. melonDS's fast memory
	// faults on purpose, in a way this runner's process can't recover from. It reserves
	// its address space for that whether it's on or not.
	"melonds": {Demanding: true, LargeAddressSpace: true, SharedMemory: true, Options: map[string]string{"melonds_touch_mode": "Touch", "melonds_screen_layout": "Top/Bottom", "melonds_jit_fast_memory": "disabled", "melonds_threaded_renderer": "enabled"}},
	"desmume": {Demanding: true, Options: map[string]string{"desmume_pointer_type": "touch", "desmume_pointer_mouse": "enabled", "desmume_screens_layout": "top/bottom", "desmume_opengl_mode": "disabled"}},
	"azahar": {GPU: true, Options: map[string]string{"citra_enable_touch_touchscreen": "enabled", "citra_enable_mouse_touchscreen": "disabled", "citra_enable_touch_pointer_timeout": "disabled", "citra_layout_option": "default"},
		Resolution: pick("citra_resolution_factor", "1", "2", "3", "4")},
	"panda3ds": {GPU: true},
}

// Traits says how to run a core. A core we don't know gets GPU room, as it may
// draw with a graphics card and nothing says otherwise.
func Traits(id string) CoreTraits {
	if t, ok := traits[id]; ok {
		return t
	}
	if _, listed := CoreLicense(id); listed {
		return CoreTraits{} // a software-rendered core from the console table
	}
	return CoreTraits{GPU: true}
}

// RunnerArgs are what a runner is told about the core it will load, before it loads
// it. A core nobody knows gets the same as a demanding one.
func RunnerArgs(coreID string) []string {
	t := Traits(coreID)
	var out []string
	if t.GPU {
		out = append(out, "-gpu")
	}
	if t.GPU || t.Demanding {
		out = append(out, "-normal-priority")
	}
	if t.SharedMemory {
		out = append(out, "-shared-memory")
	}
	if t.LargeAddressSpace {
		out = append(out, "-large-address-space")
	}
	if t.OwnHome {
		out = append(out, "-own-home")
	}
	return out
}

// DefaultResolution is how many times native a console renders at with the
// setting on Automatic. Sharp enough to be worth the graphics card, small enough
// to stream well.
//
// PlayStation 2 stays native. Its games draw full-screen effects in small tiles, and the
// fixes that hold those together when upscaled are worked out per game by the core's own
// database, which doesn't always succeed: Gran Turismo 4 at 2x comes out in bands of the
// wrong colour with a grid over them. At native size no such seams exist, and a frame
// has a quarter of the pixels to draw, encode and send.
func DefaultResolution(consoleID string) int {
	switch consoleID {
	case "n64", "ps1", "psp", "dc":
		return 2
	}
	return 1
}

// StartOptions are the options to start a core with: what CoreOptions says for
// it, then what running on a graphics card takes. resolution is the setting: "" for
// the console's default, or "1" to "4". systemDir is where its BIOS files are.
func StartOptions(consoleID, coreID, resolution, systemDir string) map[string]string {
	t := Traits(coreID)
	out := CoreOptions(coreID)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range t.Options {
		out[k] = v
	}
	if runtime.NumCPU() >= 4 {
		for k, v := range t.Threaded {
			out[k] = v
		}
	}
	times, err := strconv.Atoi(resolution)
	if err != nil || times < 1 || times > 4 {
		times = DefaultResolution(consoleID)
	}
	if t.Resolution != nil {
		for k, v := range t.Resolution(times) {
			out[k] = v
		}
	}
	if r := t.RequiredBIOS; r != nil && r.Option != "" {
		if name := r.preferred(filepath.Join(systemDir, filepath.FromSlash(r.Folder))); name != "" {
			out[r.Option] = name
		}
	}
	return out
}

// preferred picks one BIOS when someone has a whole set: a core left to itself
// takes the first by name, which is often the oldest and least compatible. A late
// US first-generation model is the usual choice, and it runs every region's games.
func (r *BIOSRule) preferred(dir string) string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best, bestScore := "", -1
	for _, e := range ents {
		if e.IsDir() || !r.matches(dir, e) {
			continue
		}
		n, score := strings.ToLower(e.Name()), 0
		switch {
		case strings.Contains(n, "39001"):
			score = 3
		case strings.Contains(n, "usa"):
			score = 2
		case strings.Contains(n, "eur"):
			score = 1
		}
		if score > bestScore {
			best, bestScore = e.Name(), score
		}
	}
	return best
}

// IsBIOSFile says whether a file beside the games is firmware rather than a game,
// going by its name. A .bin of the right name in a game folder shouldn't be listed
// as something to play.
func IsBIOSFile(consoleID, name string) bool {
	lower := strings.ToLower(name)
	if c := ByID(consoleID); c != nil {
		for _, b := range c.BIOS {
			if strings.ToLower(b.File) == lower {
				return true
			}
		}
	}
	if consoleID == "ps2" {
		return ps2BIOS.Match.MatchString(lower)
	}
	return false
}
