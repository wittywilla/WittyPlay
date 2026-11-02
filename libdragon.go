package wittyplay

import (
	"bytes"
	"io"
	"os"
)

// Most new Nintendo 64 homebrew is built with libdragon, whose games boot from its own code and
// run their own programs on the console's graphics processor, where a commercial game runs
// Nintendo's. Mupen64Plus-Next can't run them: on its fast CPU setting, it ends the whole
// process with exit(1) while compiling their code, and on the slower ones it never makes a
// frame. ParaLLEl N64 can, but only with its software renderer and the real graphics processor
// emulated: a graphics plugin guessing at what's being drawn makes the game crash.
const (
	LibdragonCore = "parallel_n64"

	LibdragonNeedsCore = "This is homebrew built with libdragon, which Mupen64Plus-Next can't run. ParaLLEl N64 can: an admin can install it in Settings, Consoles & cores."
	LibdragonNotice    = "This homebrew runs on ParaLLEl N64's accurate, slower settings. Some newer homebrew asks for more than this emulator can do, and stops on an error screen of its own."
)

// libdragonMark is in libdragon's boot code, which is the part of a file between 0x40 and 0x1000.
var libdragonMark = []byte("Libdragon IPL3")

// IsLibdragon says whether a Nintendo 64 file boots from libdragon's own code, whichever of the
// three byte orders it was dumped in. Nothing here decides for a file it can't read.
func IsLibdragon(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 0x1000)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	head = head[:n]
	if len(head) < 4 {
		return false
	}
	switch {
	case head[0] == 0x37 && head[1] == 0x80: // two bytes swapped
		for i := 0; i+1 < len(head); i += 2 {
			head[i], head[i+1] = head[i+1], head[i]
		}
	case head[0] == 0x40 && head[1] == 0x12: // four bytes reversed
		for i := 0; i+3 < len(head); i += 4 {
			head[i], head[i+1], head[i+2], head[i+3] = head[i+3], head[i+2], head[i+1], head[i]
		}
	case head[0] != 0x80:
		return false
	}
	return bytes.Contains(head, libdragonMark)
}

// LibdragonOptions are what ParaLLEl N64 takes to run such a game. Nothing here needs a graphics
// card. The cached interpreter takes about twice the CPU of the recompiler (12 seconds against 7
// in 20 of a test suite's menus), and is what shows a game's own error where the recompiler
// shows a black screen for good. cxd4 is in every build of this core.
func LibdragonOptions() map[string]string {
	return map[string]string{
		"parallel-n64-gfxplugin": "angrylion",
		"parallel-n64-rspplugin": "cxd4",
		"parallel-n64-cpucore":   "cached_interpreter",
	}
}
