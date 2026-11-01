package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// frames plays a game with the real runner and writes what the core draws to PNG
// files, before any encoder sees it. It answers one question about a fault in the
// picture: is it already there when the frame leaves the core?
func frames(args []string) error {
	fs := flag.NewFlagSet("frames", flag.ExitOnError)
	g := addGameFlags(fs)
	seconds := fs.Float64("seconds", 60, "how long to play")
	every := fs.Float64("every", 5, "seconds between pictures")
	out := fs.String("out", "", "folder for the pictures")
	press := fs.String("press", "", "buttons to press, as name@seconds, comma separated: start@40,a@45")
	state := fs.String("state", "", "a save state to start from")
	optionsOut := fs.String("options-out", "", "write the options the core offers to this file")
	_ = fs.Parse(args)
	if *out == "" {
		return fmt.Errorf("-out is needed")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	presses, err := parsePresses(*press)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	load, runnerArgs := g.load()
	if err := os.MkdirAll(g.save, 0o755); err != nil {
		return err
	}
	rn, err := stream.StartRunner(g.runner, runnerArgs, g.save, g.memoryMB(), log)
	if err != nil {
		return err
	}
	defer rn.Release()
	defer rn.Cmd.Process.Kill()
	go func() {
		b := make([]byte, 4096)
		for {
			n, err := rn.Stderr.Read(b)
			if n > 0 && g.verbose {
				os.Stderr.Write(b[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	body, _ := json.Marshal(load)
	if err := ipc.Write(rn.Stdin, ipc.CmdLoad, body); err != nil {
		return err
	}
	rd := ipc.NewReader(rn.Stdout)
	start := time.Now()
	nextShot := 0.0
	shot := 0
	made := 0
	sawReady := false
	lastCount := time.Now()
	for time.Since(start).Seconds() < *seconds {
		t, p, err := rd.Next()
		if err != nil {
			return fmt.Errorf("the runner stopped: %w", err)
		}
		switch t {
		case ipc.MsgReady:
			fmt.Println("ready:", string(p))
			sawReady = true
			if *state != "" {
				b, err := os.ReadFile(*state)
				if err != nil {
					return err
				}
				if err := ipc.Write(rn.Stdin, ipc.CmdState, b); err != nil {
					return err
				}
			}
			_ = ipc.Write(rn.Stdin, ipc.CmdPause, []byte{0})
			start = time.Now()
		case ipc.MsgError:
			return fmt.Errorf("runner: %s", p)
		case ipc.MsgEvent:
			var e ipc.Event
			if json.Unmarshal(p, &e) == nil {
				switch e.Type {
				case "message", "statefail":
					fmt.Println("core says:", e.Type, e.Text)
				case "options":
					if *optionsOut != "" {
						if b, err := json.MarshalIndent(e.Options, "", " "); err == nil {
							_ = os.WriteFile(*optionsOut, b, 0o644)
						}
					}
				}
			}
		case ipc.MsgVideo:
			hdr, data, err := ipc.ParseVideo(p)
			if err != nil {
				return err
			}
			made++
			now := time.Since(start).Seconds()
			if sawReady && now >= nextShot {
				name := filepath.Join(*out, fmt.Sprintf("raw-%03d-%ds-%dx%d.png", shot, int(now), hdr.Width, hdr.Height))
				if err := writePNG(name, hdr, data); err != nil {
					return err
				}
				shot++
				nextShot = now + *every
				fmt.Printf("%6.1fs %s (%.1f frames a second)\n", now, filepath.Base(name), float64(made)/time.Since(lastCount).Seconds())
				made, lastCount = 0, time.Now()
			}
		}
		for len(presses) > 0 && time.Since(start).Seconds() >= presses[0].at {
			b := presses[0]
			presses = presses[1:]
			state := make([]byte, ipc.InputSizeFull)
			state[0] = 0
			if b.down {
				binary.LittleEndian.PutUint16(state[1:], 1<<b.button)
			}
			_ = ipc.Write(rn.Stdin, ipc.CmdInput, state)
		}
	}
	_ = ipc.Write(rn.Stdin, ipc.CmdQuit, []byte{0})
	return nil
}

type press struct {
	at     float64
	button int
	down   bool
}

var buttonNames = map[string]int{"b": engine.ButtonB, "y": engine.ButtonY, "select": engine.ButtonSelect, "start": engine.ButtonStart,
	"up": engine.ButtonUp, "down": engine.ButtonDown, "left": engine.ButtonLeft, "right": engine.ButtonRight, "a": engine.ButtonA, "x": engine.ButtonX}

// parsePresses reads name@seconds. Each press is held for a fifth of a second.
func parsePresses(s string) ([]press, error) {
	var out []press
	if s == "" {
		return nil, nil
	}
	for _, part := range strings.Split(s, ",") {
		name, at, ok := strings.Cut(part, "@")
		b, known := buttonNames[name]
		v, err := strconv.ParseFloat(at, 64)
		if !ok || !known || err != nil {
			return nil, fmt.Errorf("can't read the press %q", part)
		}
		out = append(out, press{at: v, button: b, down: true}, press{at: v + 0.2, button: b})
	}
	return out, nil
}

func writePNG(name string, hdr ipc.VideoHeader, data []byte) error {
	img := stream.ToRGBA(hdr, data)
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

// gameFlags say what to play.
type gameFlags struct {
	runner, core, rom, system, save, console, coreID, resolution, options string
	mem                                                                   int64
	verbose                                                               bool
}

func addGameFlags(fs *flag.FlagSet) *gameFlags {
	g := &gameFlags{}
	fs.StringVar(&g.runner, "runner", "", "the wittyplay-host program")
	fs.StringVar(&g.core, "core", ipc.TestCore, "a core file, or the built-in test core")
	fs.StringVar(&g.rom, "rom", "", "the game")
	fs.StringVar(&g.system, "system", "", "the BIOS folder")
	fs.StringVar(&g.save, "save", "", "a folder for saves, used by nothing else")
	fs.StringVar(&g.console, "console", "", "the console, as in the console table")
	fs.StringVar(&g.coreID, "core-id", "", "the core's id: its options come from here")
	fs.StringVar(&g.resolution, "resolution", "", "internal resolution, 1 to 4; empty for the console's default")
	fs.StringVar(&g.options, "options", "", "core options, key=value;key=value, replacing the defaults")
	fs.Int64Var(&g.mem, "memory-mb", 0, "the runner's memory limit; 0 for what the server uses")
	fs.BoolVar(&g.verbose, "v", false, "show what the runner prints")
	return g
}

func (g *gameFlags) memoryMB() int64 {
	if g.mem > 0 {
		return g.mem
	}
	if g.coreID != "" && wittyplay.Traits(g.coreID).GPU {
		return 8192
	}
	return 2048
}

func (g *gameFlags) load() (ipc.Load, []string) {
	var args []string
	opts := map[string]string{}
	assets := ""
	if g.coreID != "" {
		args = wittyplay.RunnerArgs(g.coreID)
		opts = wittyplay.StartOptions(g.console, g.coreID, g.resolution, g.system)
		assets = wittyplay.Traits(g.coreID).Assets
	}
	if g.options != "" {
		for _, kv := range strings.Split(g.options, ";") {
			k, v, _ := strings.Cut(kv, "=")
			opts[k] = v
		}
	}
	return ipc.Load{Core: g.core, ROM: g.rom, System: g.system, Save: g.save, Options: opts, Assets: assets}, args
}
