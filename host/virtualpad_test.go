// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package host

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/testcore"
)

// virtualPad is testdata/virtual-pad.json: the states the web player sends for a virtual gamepad
// pressed through the browser's Gamepad API (web/tests/virtualpad.test.ts checks that it does).
type virtualPad struct {
	Controls json.RawMessage `json:"controls"`
	Steps    []struct {
		Say     string   `json:"say"`
		Sends   string   `json:"sends"`
		Buttons []string `json:"buttons"`
		Frames  int      `json:"frames"`
	} `json:"steps"`
	Game struct {
		StartPresses uint32 `json:"startPresses"`
		LED          []int  `json:"led"`
		Rumble       []int  `json:"rumble"`
		Moves        string `json:"moves"`
	} `json:"game"`
}

func readVirtualPad(t *testing.T) virtualPad {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "virtual-pad.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f virtualPad
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// The web side of the test plays the NES's controls as the server sends them; if the console
// table changes, the fixture has to change with it.
func TestVirtualPadControlsAreTheServers(t *testing.T) {
	f := readVirtualPad(t)
	id := "nes"
	want, _ := json.Marshal(map[string]any{
		"console": id, "pad": wittyplay.Pad(id), "controls": wittyplay.Controls(id), "targets": wittyplay.Targets(id),
		"defaults": map[string]any{"gamepad": wittyplay.DefaultGamepadFor(id), "keyboard": wittyplay.DefaultKeyboardFor(id), "remote": wittyplay.DefaultRemote(), "options": wittyplay.DefaultOptions(id)},
	})
	var got, exp any
	if err := json.Unmarshal(f.Controls, &got); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(want, &exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("testdata/virtual-pad.json's controls aren't what GET /consoles/nes/controls sends now:\n%s", want)
	}
}

// The states a virtual pad sent through the web player, played into the game runner with the
// test core: the game counts each Start, lights its LED while Select is held, rumbles while A
// is, and the D-pad the left stick steered moves its square.
func TestVirtualPadPlaysTheGame(t *testing.T) {
	f := readVirtualPad(t)
	retro := map[string]int{"b": engine.ButtonB, "select": engine.ButtonSelect, "start": engine.ButtonStart, "up": engine.ButtonUp, "down": engine.ButtonDown, "left": engine.ButtonLeft, "right": engine.ButtonRight, "a": engine.ButtonA}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(inR, outW, func(_ ipc.Load, h engine.Handlers) (engine.Engine, error) { return testcore.New(h), nil })
		outW.Close()
	}()
	type msg struct {
		t byte
		p []byte
	}
	msgs := make(chan msg, 256)
	go func() {
		defer close(msgs)
		rd := ipc.NewReader(outR)
		for {
			typ, p, err := rd.Next()
			if err != nil {
				return
			}
			msgs <- msg{typ, append([]byte(nil), p...)}
		}
	}()
	rom := filepath.Join(t.TempDir(), "game")
	if err := os.WriteFile(rom, []byte("a test game"), 0o644); err != nil {
		t.Fatal(err)
	}
	load, _ := json.Marshal(ipc.Load{Core: ipc.TestCore, ROM: rom})
	send := func(c byte, p []byte) {
		if err := ipc.Write(inW, c, p); err != nil {
			t.Fatal(err)
		}
	}
	send(ipc.CmdLoad, load)

	var leds, rumbles []int
	var ram []byte
	square := -1 // the left edge of the test core's square in the newest picture
	// next takes what the runner says until it has shown n more pictures.
	next := func(n int) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for n > 0 {
			select {
			case m, ok := <-msgs:
				if !ok {
					t.Fatal("the runner stopped")
				}
				switch m.t {
				case ipc.MsgError:
					t.Fatalf("the runner failed: %s", m.p)
				case ipc.MsgVideo:
					if x := squareAt(t, m.p); x >= 0 {
						square = x
					}
					n--
				case ipc.MsgDupe:
					n--
				case ipc.MsgEvent:
					var e ipc.Event
					_ = json.Unmarshal(m.p, &e)
					switch e.Type {
					case "led":
						leds = append(leds, e.State)
					case "rumble":
						if e.Port == 0 && e.Effect == 0 {
							rumbles = append(rumbles, e.Strength)
						}
					}
				case ipc.MsgSaveRAM:
					ram = m.p
				}
			case <-deadline:
				t.Fatal("the game stopped showing pictures")
			}
		}
	}
	// Waiting for the first picture also waits for the game to have loaded.
	for m := range msgs {
		if m.t == ipc.MsgError {
			t.Fatalf("the runner failed: %s", m.p)
		}
		if m.t == ipc.MsgReady {
			break
		}
	}
	send(ipc.CmdPause, []byte{0})
	next(3)

	var startX int
	for _, s := range f.Steps {
		b, err := hex.DecodeString(s.Sends)
		if err != nil || len(b) != ipc.InputSizeFull {
			t.Fatalf("%s: %q isn't a %d-byte state", s.Say, s.Sends, ipc.InputSizeFull)
		}
		port, buttons, _, _, _, _, ok := ipc.ParseInput(b)
		var want uint16
		for _, n := range s.Buttons {
			want |= 1 << retro[n]
		}
		if !ok || port != 0 || buttons != want {
			t.Fatalf("%s: the runner reads player %d holding %016b, want player 1 holding %v", s.Say, port+1, buttons, s.Buttons)
		}
		if len(s.Buttons) == 1 && s.Buttons[0] == f.Game.Moves {
			startX = square
		}
		send(ipc.CmdInput, b)
		next(max(s.Frames, 2))
		if len(s.Buttons) == 1 && s.Buttons[0] == f.Game.Moves && square <= startX {
			t.Errorf("%s: the square stayed at x=%d", s.Say, square)
		}
	}
	send(ipc.CmdQuit, []byte{0})
	for m := range msgs {
		if m.t == ipc.MsgSaveRAM {
			ram = m.p
		}
	}
	inW.Close()
	<-done

	if len(ram) < 4 || binary.LittleEndian.Uint32(ram) != f.Game.StartPresses {
		t.Errorf("the game counted %v presses of Start, want %d", ram, f.Game.StartPresses)
	}
	if !reflect.DeepEqual(leds, f.Game.LED) {
		t.Errorf("the LED went %v with Select, want %v", leds, f.Game.LED)
	}
	if !reflect.DeepEqual(rumbles, f.Game.Rumble) {
		t.Errorf("the pad rumbled %v with A, want %v", rumbles, f.Game.Rumble)
	}
}

// squareAt finds the test core's white square, below the row of button lights; -1 if the
// picture isn't one it can read.
func squareAt(t *testing.T, p []byte) int {
	h, data, err := ipc.ParseVideo(p)
	if err != nil || h.Format != engine.FormatXRGB8888 || len(data) < h.Width*h.Height*4 {
		return -1
	}
	for y := 20; y < h.Height; y++ {
		row := data[y*h.Width*4:]
		for x := 0; x < h.Width; x++ {
			if px := row[x*4:]; px[0] == 0xff && px[1] == 0xff && px[2] == 0xff {
				return x
			}
		}
	}
	return -1
}
