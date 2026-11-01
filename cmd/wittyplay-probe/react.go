package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// react says how many frames a game takes to show a press, on a screen that's otherwise
// still. It's the part of the delay that's the game's own, which only run-ahead can change.
func react(args []string) error {
	fs := flag.NewFlagSet("react", flag.ExitOnError)
	g := addGameFlags(fs)
	at := fs.Float64("at", 8, "seconds into the game to press, on a still screen")
	button := fs.String("button", "start", "the button")
	ahead := fs.Int("run-ahead", 0, "frames of run-ahead")
	_ = fs.Parse(args)
	id, ok := buttonNames[*button]
	if !ok {
		return fmt.Errorf("no such button: %s", *button)
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
	body, _ := json.Marshal(load)
	if err := ipc.Write(rn.Stdin, ipc.CmdLoad, body); err != nil {
		return err
	}
	rd := ipc.NewReader(rn.Stdout)
	var start time.Time
	var last []byte
	pressed, frames := false, 0
	for {
		t, p, err := rd.Next()
		if err != nil {
			return err
		}
		switch t {
		case ipc.MsgReady:
			_ = ipc.Write(rn.Stdin, ipc.CmdPause, []byte{0})
			if *ahead > 0 {
				b, _ := json.Marshal(ipc.Control{RunAhead: ahead})
				_ = ipc.Write(rn.Stdin, ipc.CmdControl, b)
			}
			start = time.Now()
		case ipc.MsgError:
			return fmt.Errorf("runner: %s", p)
		case ipc.MsgVideo, ipc.MsgDupe:
			var data []byte
			if t == ipc.MsgVideo {
				_, d, err := ipc.ParseVideo(p)
				if err != nil {
					return err
				}
				data = d
			} else {
				data = last
			}
			if pressed {
				frames++
				if !bytes.Equal(data, last) {
					fmt.Printf("the picture changed %d frames after the press\n", frames)
					_ = ipc.Write(rn.Stdin, ipc.CmdQuit, []byte{0})
					return nil
				}
				if frames > 300 {
					return fmt.Errorf("nothing changed in 300 frames")
				}
			} else if !start.IsZero() && time.Since(start).Seconds() > *at {
				msg := make([]byte, ipc.InputSize)
				binary.LittleEndian.PutUint16(msg[1:], 1<<id)
				_ = ipc.Write(rn.Stdin, ipc.CmdInput, msg)
				pressed = true
			}
			last = append(last[:0], data...)
		}
	}
}
