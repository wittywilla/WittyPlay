// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package host is the program that runs one game: it reads commands on one
// stream, writes frames on another and paces the engine at its own frame rate.
// cmd/wittyplay-game-runner wraps it in a process; tests run it directly.
package host

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
)

// Open makes the engine a Load asks for.
type Open func(l ipc.Load, h engine.Handlers) (engine.Engine, error)

// How often a battery save is looked at. It's a comparison of a few hundred KB at most and is
// written only if it changed, and whatever hasn't been written when a machine dies is lost.
const saveRAMEvery = 5 * time.Second

// Run plays until told to quit or until in closes. Cores may keep thread-local
// state, so it locks a thread for itself.
func Run(in io.Reader, out io.Writer, open Open) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	r := &runner{out: out}
	r.pads.pressed = make(chan struct{}, 1)
	r.ctl = newControl(r)
	err := r.run(in, open)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = ipc.Write(out, ipc.MsgError, []byte(err.Error()))
	}
	return err
}

type runner struct {
	out    io.Writer
	outMu  sync.Mutex // the frame sender writes too, and messages must not interleave
	writeE error
	frames frameSender

	pads pads

	pointMu sync.Mutex
	point   engine.PointerState
	touches []engine.PointerState // every finger, under pointMu
	motion  [engine.MaxPorts]engine.MotionState
	leds    map[int]int

	audio []byte
	video []byte
	stop  bool
	fps   float64 // a new frame rate the game has asked for
	muted bool    // these frames are only to get a core ready, and nobody should see them
	ctl   *control

	rumble map[[2]int]int // what each motor was last told

	audioStep            int // how many samples make one, for a core far above any usable rate
	audioHeld            int
	audioSumL, audioSumR int64
	audioThin            []int16

	stamp      bool // draw the frame and input numbers into each picture
	frameSeq   uint32
	runStart   int64 // when the core began the frame it's on
	frameTimer *time.Timer
}

func (r *runner) write(t byte, parts ...[]byte) {
	if (r.ctl.dropVideo && (t == ipc.MsgVideo || t == ipc.MsgDupe)) || (r.ctl.dropAudio && t == ipc.MsgAudio) {
		return
	}
	r.send(t, parts...)
}

// A core may make sound at the speed of the chip it emulates, millions of samples a second.
// Nothing after this point takes that, and nobody can hear it, so it's averaged down here.
const audioRateLimit = 192000

// audioRate says what a core's sample rate comes to, and sets up anything that takes.
func (r *runner) audioRate(rate float64) float64 {
	r.audioStep = 1
	if rate > audioRateLimit {
		r.audioStep = int(rate / 48000)
		return rate / float64(r.audioStep)
	}
	return rate
}

// thinAudio averages each run of stereo samples into one. It can be given part of a run,
// and carries the rest over to the next call.
func (r *runner) thinAudio(s []int16) []int16 {
	out := r.audioThin[:0]
	for i := 0; i+1 < len(s); i += 2 {
		r.audioSumL += int64(s[i])
		r.audioSumR += int64(s[i+1])
		if r.audioHeld++; r.audioHeld == r.audioStep {
			n := int64(r.audioStep)
			out = append(out, int16(r.audioSumL/n), int16(r.audioSumR/n))
			r.audioSumL, r.audioSumR, r.audioHeld = 0, 0, 0
		}
	}
	r.audioThin = out
	return out
}

// timing describes the picture that's just been finished.
func (r *runner) timing() ipc.FrameTiming {
	r.frameSeq++
	in, at := r.pads.seen()
	return ipc.FrameTiming{Seq: r.frameSeq, InputSeq: in, InputAt: at, RunStart: r.runStart, RunEnd: time.Now().UnixNano()}
}

// sendFrame sends a picture with the timing that goes with it, in one go, so that
// nothing can come between them.
func (r *runner) sendFrame(t byte, tm ipc.FrameTiming, frame []byte) {
	var head [ipc.FrameTimingSize]byte
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.writeE == nil {
		r.writeE = ipc.Write(r.out, ipc.MsgTiming, tm.Append(head[:0]))
	}
	if r.writeE == nil {
		r.writeE = ipc.Write(r.out, t, frame)
	}
}

// send writes whatever it's given. A frame a graphics card drew comes from another
// goroutine, already past the check write makes, so it starts here.
func (r *runner) send(t byte, parts ...[]byte) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.writeE == nil {
		r.writeE = ipc.Write(r.out, t, parts...)
	}
}

func (r *runner) writeErr() error {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	return r.writeE
}

// frameSender sends the frames a graphics card draws. They run to several megabytes,
// and a game mustn't wait while one crosses the pipe, or wait at all if the server
// has fallen behind: the newest frame replaces one still waiting to go, the same
// as on the server's side, so no queue can build up and add delay.
type frameSender struct {
	mu      sync.Mutex
	wake    *sync.Cond
	next    []byte // ready to go
	spare   []byte
	timing  ipc.FrameTiming
	waiting bool
	running bool
	closed  bool
}

// offer takes a frame built by fill, which is given a buffer to reuse.
func (f *frameSender) offer(r *runner, tm ipc.FrameTiming, fill func(buf []byte) []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return
	}
	f.next = fill(f.next[:0])
	f.timing = tm
	f.waiting = true
	if !f.running {
		f.running = true
		f.wake = sync.NewCond(&f.mu)
		go f.loop(r)
	}
	f.wake.Signal()
}

func (f *frameSender) loop(r *runner) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for {
		for !f.waiting && !f.closed {
			f.wake.Wait()
		}
		if f.closed {
			return
		}
		frame, tm := f.next, f.timing
		f.next, f.spare = f.spare, nil
		f.waiting = false
		f.mu.Unlock()
		r.sendFrame(ipc.MsgVideo, tm, frame)
		f.mu.Lock()
		f.spare = frame
	}
}

func (f *frameSender) stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.wake != nil {
		f.wake.Signal()
	}
}

func (r *runner) event(e ipc.Event) {
	b, _ := json.Marshal(e)
	r.write(ipc.MsgEvent, b)
}

func (r *runner) handlers() engine.Handlers {
	return r.ctl.wrap(engine.Handlers{
		Video: func(data []byte, w, h, stride, format int) {
			row := w * engine.BytesPerPixel(format)
			if r.muted || r.ctl.dropVideo || w <= 0 || h <= 0 || row > stride || len(data) < stride*h {
				return
			}
			r.video = ipc.VideoHeader{Width: w, Height: h, Format: format}.Append(r.video[:0])
			for y := 0; y < h; y++ {
				r.video = append(r.video, data[y*stride:y*stride+row]...)
			}
			tm := r.timing()
			if r.stamp {
				stampFrame(r.video[ipc.VideoHeaderSize:], w, h, format, tm)
			}
			r.sendFrame(ipc.MsgVideo, tm, r.video)
		},
		VideoFlipped: func(data []byte, w, h, stride, format int) {
			row := w * engine.BytesPerPixel(format)
			if r.muted || r.ctl.dropVideo || w <= 0 || h <= 0 || row > stride || len(data) < stride*h {
				return
			}
			tm := r.timing()
			r.frames.offer(r, tm, func(buf []byte) []byte {
				buf = ipc.VideoHeader{Width: w, Height: h, Format: format}.Append(buf)
				for y := h - 1; y >= 0; y-- {
					buf = append(buf, data[y*stride:y*stride+row]...)
				}
				if r.stamp {
					stampFrame(buf[ipc.VideoHeaderSize:], w, h, format, tm)
				}
				return buf
			})
		},
		Dupe: func() {
			if !r.muted && !r.ctl.dropVideo {
				r.sendFrame(ipc.MsgDupe, r.timing(), nil)
			}
		},
		Audio: func(s []int16) {
			if r.audioStep > 1 {
				s = r.thinAudio(s)
			}
			for _, v := range s {
				r.audio = binary.LittleEndian.AppendUint16(r.audio, uint16(v))
			}
		},
		Input: r.pads.poll,
		Pointer: func() engine.PointerState {
			r.pointMu.Lock()
			defer r.pointMu.Unlock()
			return r.point
		},
		Touches: func() []engine.PointerState {
			r.pointMu.Lock()
			defer r.pointMu.Unlock()
			return append([]engine.PointerState(nil), r.touches...)
		},
		MotionOffered: func() { r.event(ipc.Event{Type: "motion", Port: -1, Offered: true}) },
		MotionWanted: func(port int, accel, gyro bool, hz int) {
			r.event(ipc.Event{Type: "motion", Port: port, Accel: accel, Gyro: gyro, Hz: min(max(hz, 0), 1000), Offered: true})
		},
		Motion: func(port int) engine.MotionState {
			r.pointMu.Lock()
			defer r.pointMu.Unlock()
			if port < 0 || port >= engine.MaxPorts {
				return engine.MotionState{}
			}
			return r.motion[port]
		},
		LED: func(index, state int) {
			// As rumble: cores may say it every frame, and only a change is news.
			if r.leds == nil {
				r.leds = map[int]int{}
			}
			if old, ok := r.leds[index]; (ok && old == state) || index < 0 || index > 255 {
				return
			}
			r.leds[index] = state
			r.event(ipc.Event{Type: "led", Index: index, State: state})
		},
		Rumble: func(port, effect, strength int) {
			// Cores repeat this every frame, and most of it says nothing new.
			k := [2]int{port, effect}
			if r.rumble == nil {
				r.rumble = map[[2]int]int{}
			}
			if r.rumble[k] == strength {
				return
			}
			r.rumble[k] = strength
			r.event(ipc.Event{Type: "rumble", Port: port, Effect: effect, Strength: strength})
		},
		Message: func(text string) { r.event(ipc.Event{Type: "message", Text: text}) },
		Log: func(level int, text string) {
			if level >= 2 {
				r.event(ipc.Event{Type: "log", Level: level, Text: text})
			}
		},
		Geometry: func(w, h int, aspect float64) {
			r.event(ipc.Event{Type: "geometry", Width: w, Height: h, Aspect: aspect})
		},
		Rotation: func(n int) { r.event(ipc.Event{Type: "rotation", Turns: n}) },
		AVInfo: func(a engine.AVInfo) {
			r.fps = a.FPS
			a.SampleRate = r.audioRate(a.SampleRate)
			r.event(ipc.Event{Type: "avinfo", Width: a.BaseWidth, Height: a.BaseHeight, Aspect: a.Aspect, FPS: a.FPS, Rate: a.SampleRate})
		},
		Shutdown: func() { r.event(ipc.Event{Type: "shutdown"}); r.stop = true },
	})
}

type command struct {
	t byte
	p []byte
}

// plugInDevices gives each port the kind of controller the console prefers, when the
// core offers it, and returns the choices for the player to show.
func plugInDevices(eng engine.Engine, prefer []string) []ipc.PortDevices {
	d, ok := eng.(engine.Devices)
	if !ok {
		return nil
	}
	var out []ipc.PortDevices
	for _, port := range d.ControllerPorts() {
		p := ipc.PortDevices{Port: port.Port, Current: port.Current}
		for _, t := range port.Types {
			p.Types = append(p.Types, ipc.DeviceType{ID: t.ID, Name: t.Name})
		}
	search:
		for _, want := range prefer {
			for _, t := range port.Types {
				if strings.Contains(strings.ToLower(t.Name), want) {
					p.Current = t.ID
					d.SetDevice(port.Port, t.ID)
					break search
				}
			}
		}
		out = append(out, p)
	}
	return out
}

func (r *runner) run(in io.Reader, open Open) error {
	cmds := make(chan command, 64)
	readErr := make(chan error, 1)
	go func() {
		rd := ipc.NewReader(in)
		for {
			t, p, err := rd.Next()
			if err != nil {
				readErr <- err
				close(cmds)
				return
			}
			if t == ipc.CmdInput {
				// Input skips the queue: the newest state is all that matters.
				if port, b, lx, ly, rx, ry, ok := ipc.ParseInput(p); ok && port < engine.MaxPorts {
					l2, r2 := ipc.ParseTriggers(p)
					r.pads.set(port, engine.PadState{Buttons: b, LX: lx, LY: ly, RX: rx, RY: ry, L2: l2, R2: r2}, ipc.InputStamp(p))
				}
				continue
			}
			if t == ipc.CmdPointer {
				if x, y, pressed, ok := ipc.ParsePointer(p); ok {
					r.pointMu.Lock()
					r.point = engine.PointerState{X: x, Y: y, Pressed: pressed}
					r.touches = r.touches[:0]
					if pressed {
						r.touches = append(r.touches, r.point)
					}
					r.pointMu.Unlock()
				}
				continue
			}
			if t == ipc.CmdTouches {
				if list, ok := ipc.ParseTouches(p); ok {
					r.pointMu.Lock()
					r.touches = r.touches[:0]
					for _, f := range list {
						if f.Pressed {
							r.touches = append(r.touches, engine.PointerState{X: f.X, Y: f.Y, Pressed: true})
						}
					}
					// The pointer stays where the first finger is, or was.
					if len(r.touches) > 0 {
						r.point = r.touches[0]
					} else {
						r.point.Pressed = false
					}
					r.pointMu.Unlock()
				}
				continue
			}
			if t == ipc.CmdMotion {
				if port, accel, gyro, ok := ipc.ParseMotion(p); ok && port < engine.MaxPorts {
					r.pointMu.Lock()
					r.motion[port] = engine.MotionState{Accel: accel, Gyro: gyro}
					r.pointMu.Unlock()
				}
				continue
			}
			cmds <- command{t, append([]byte(nil), p...)}
		}
	}()

	// The first command says what to play. No core is loaded before this, so
	// the server has had time to box this process in.
	first, ok := <-cmds
	if !ok {
		return <-readErr
	}
	if first.t == ipc.CmdProbe {
		return r.probe(first.p, open)
	}
	if first.t != ipc.CmdLoad {
		return fmt.Errorf("expected a load command, got %d", first.t)
	}
	var load ipc.Load
	if err := json.Unmarshal(first.p, &load); err != nil {
		return err
	}
	r.stamp = load.Stamp
	h := r.handlers()
	if load.Trace {
		h.Trace = func(text string) { r.event(ipc.Event{Type: "trace", Text: text}) }
	}
	eng, err := open(load, h)
	if err != nil {
		return err
	}
	defer eng.Close()
	r.ctl.eng = eng
	if load.Probe {
		r.ctl.sendOptions()
		return r.writeE
	}
	av, err := eng.LoadGame(load.ROM)
	if err != nil {
		return err
	}
	if av.FPS <= 0 || av.FPS > 240 {
		av.FPS = 60
	}
	av.SampleRate = r.audioRate(av.SampleRate)
	info := eng.SystemInfo()
	renderer := ""
	if hw, ok := eng.(interface{ Renderer() string }); ok {
		renderer = hw.Renderer()
	}
	discs, _ := eng.(engine.Discs)
	disc, discCount := 0, 0
	if discs != nil {
		disc, discCount = discs.Disc()
	}
	ready, _ := json.Marshal(ipc.Ready{Name: info.Name, Version: info.Version, Width: av.BaseWidth, Height: av.BaseHeight, Aspect: av.Aspect, FPS: av.FPS, SampleRate: av.SampleRate, Disc: disc, Discs: discCount, Renderer: renderer,
		Devices: plugInDevices(eng, load.PreferDevices)})
	r.ctl.sendOptions()
	r.write(ipc.MsgReady, ready)

	gameFPS := av.FPS
	frame := framePeriod(gameFPS, r.ctl.displayHz)
	r.ctl.frameTime = frame
	next := time.Now()
	// A state can't always be restored before a core has run. Some cores say so, some
	// refuse, and some accept it and then start the game from the beginning on the
	// first frame anyway. So a state that arrives before the first frame waits for a
	// couple of frames, which nobody sees.
	const earlyStateFrames = 2
	var settle int
	var heldState []byte
	framesRun := 0
	paused := true // until the server has a viewer
	var lastSave []byte
	lastSaveAt := time.Now()

	// A state still waiting to be restored is where the game really is. Saving the
	// freshly started machine instead would overwrite someone's progress when they
	// leave before the first frame.
	snapshot := func() []byte {
		if heldState != nil {
			return heldState
		}
		return eng.SaveState()
	}

	handle := func(c command) (quit bool) {
		switch c.t {
		case ipc.CmdSaveRAM:
			if ram := eng.SaveRAM(); ram != nil {
				copy(ram, c.p)
				lastSave = append(lastSave[:0], ram...)
			}
		case ipc.CmdState:
			switch {
			case framesRun < earlyStateFrames:
				heldState, settle = c.p, earlyStateFrames-framesRun
				r.muted = true
			case !eng.LoadState(c.p):
				r.event(ipc.Event{Type: "statefail"})
			}
			r.ctl.stateChanged()
		case ipc.CmdSaveState:
			r.write(ipc.MsgState, c.p, snapshot())
		case ipc.CmdReset:
			eng.Reset()
			r.ctl.stateChanged()
		case ipc.CmdDisc:
			if discs != nil && len(c.p) > 0 {
				if !discs.ChangeDisc(int(c.p[0])) {
					r.event(ipc.Event{Type: "message", Text: "That disc could not be loaded."})
				}
				i, n := discs.Disc()
				r.event(ipc.Event{Type: "disc", Index: i, Count: n})
			}
		case ipc.CmdControl, ipc.CmdOption, ipc.CmdCheats:
			r.ctl.command(c.t, c.p)
		case ipc.CmdPause:
			paused = len(c.p) > 0 && c.p[0] != 0
			next = time.Now()
		case ipc.CmdDevice:
			if d, ok := eng.(engine.Devices); ok && len(c.p) == ipc.DeviceSize {
				d.SetDevice(int(c.p[0]), binary.LittleEndian.Uint32(c.p[1:]))
			}
		case ipc.CmdQuit:
			if len(c.p) > 0 && c.p[0] != 0 {
				var id [4]byte
				r.write(ipc.MsgState, id[:], snapshot())
			}
			if ram := eng.SaveRAM(); ram != nil {
				r.write(ipc.MsgSaveRAM, ram)
			}
			return true
		}
		return false
	}

	defer r.frames.stop()
	for !r.stop {
		if err := r.writeErr(); err != nil {
			return err
		}
		if paused && !r.ctl.wantsFrame() {
			c, ok := <-cmds
			if !ok {
				return <-readErr
			}
			if handle(c) {
				return nil
			}
			continue
		}
	drain:
		for {
			select {
			case c, ok := <-cmds:
				if !ok {
					return <-readErr
				}
				if handle(c) {
					return nil
				}
			default:
				break drain
			}
		}
		if paused && !r.ctl.wantsFrame() {
			continue
		}
		r.pads.looked()
		r.runStart = time.Now().UnixNano()
		r.ctl.runFrame()
		framesRun++
		r.pads.frameDone()
		if settle > 0 {
			if settle--; settle == 0 {
				if !eng.LoadState(heldState) {
					r.event(ipc.Event{Type: "statefail"})
				}
				heldState = nil
				r.muted = false
				r.audio = r.audio[:0]
			}
		}
		if r.fps > 0 && r.fps <= 240 {
			gameFPS = r.fps
			r.fps = 0
		}
		// The game's own rate, or the screen's when it's close: see ipc.FramePeriod.
		if f := framePeriod(gameFPS, r.ctl.displayHz); f != frame {
			frame = f
			r.ctl.frameTime = frame
		}
		if len(r.audio) > 0 {
			r.write(ipc.MsgAudio, r.audio)
			r.audio = r.audio[:0]
		}
		if time.Since(lastSaveAt) > saveRAMEvery {
			lastSaveAt = time.Now()
			if ram := eng.SaveRAM(); ram != nil && !bytes.Equal(ram, lastSave) {
				lastSave = append(lastSave[:0], ram...)
				r.write(ipc.MsgSaveRAM, ram)
			}
		}
		now := time.Now()
		if paused { // a frame advanced by hand keeps to no schedule
			next = now
			continue
		}
		step := time.Duration(float64(frame) / r.ctl.pace())
		next = next.Add(step + r.ctl.shift)
		r.ctl.shift = 0
		r.ctl.lag = 0
		if d := next.Sub(now); d > 0 {
			r.waitForFrame(d, step)
		} else if r.ctl.lag = -d; d < -5*frame {
			next = now // too far behind to catch up: the machine is overloaded
		}
	}
	// The game asked to shut down, so nothing should be lost.
	if ram := eng.SaveRAM(); ram != nil {
		r.write(ipc.MsgSaveRAM, ram)
	}
	return r.writeErr()
}

// framePeriod is how long a frame lasts, for a game of this rate shown on a screen of that one:
// the screen's, when it's within ipc.DisplayPaceLimit of the game's own.
func framePeriod(gameFPS, displayHz float64) time.Duration {
	if gameFPS <= 0 {
		gameFPS = 60
	}
	rate := gameFPS
	if displayHz > 0 && math.Abs(displayHz/gameFPS-1) <= ipc.DisplayPaceLimit {
		rate = displayHz
	}
	return time.Duration(float64(time.Second) / rate)
}

// waitForFrame sleeps until the next frame is due. A button pressed in the meantime ends
// the wait, because the next frame is the one that will show it, and every moment spent
// here is delay nobody can get back. Frames stay on the same schedule afterwards, so the
// game keeps its speed: this frame comes a little early, and the gap after it is that much
// longer. Once a frame has come early, nothing brings the one after it forward as well.
func (r *runner) waitForFrame(d, step time.Duration) {
	if r.ctl.noEarly || d > step || r.ctl.pace() != 1 {
		time.Sleep(d)
		return
	}
	if r.frameTimer == nil {
		r.frameTimer = time.NewTimer(d)
	} else {
		r.frameTimer.Reset(d)
	}
	select {
	case <-r.frameTimer.C:
	case <-r.pads.pressed:
		r.frameTimer.Stop()
	}
}

// pads holds the players' input. A press and its release can arrive together, in a
// short tap or a bunched-up packet, and a game only looks once a frame. So a button
// pressed since the last frame reads as held for one frame, however briefly it was.
type pads struct {
	mu      sync.Mutex
	state   [engine.MaxPorts]engine.PadState
	pending [engine.MaxPorts]uint16 // pressed since the last frame looked
	active  [engine.MaxPorts]uint16 // what this frame is keeping pressed
	snap    [engine.MaxPorts]engine.PadState

	stamp    uint32 // the newest numbered state to arrive, and when
	stampAt  int64
	polled   uint32 // the newest the core has looked at, and when that one arrived
	polledAt int64

	pressed chan struct{} // something changed that the next frame should show
}

func (p *pads) set(port int, st engine.PadState, stamp uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	changed := st.Buttons != p.state[port].Buttons
	p.pending[port] |= st.Buttons &^ p.state[port].Buttons
	p.state[port] = st
	if stamp != 0 {
		p.stamp, p.stampAt = stamp, time.Now().UnixNano()
	}
	if changed {
		select {
		case p.pressed <- struct{}{}:
		default:
		}
	}
}

// looked is for just before a frame starts: anything pressed before now, it will see.
func (p *pads) looked() {
	select {
	case <-p.pressed:
	default:
	}
}

// seen says which numbered state the core last looked at, and when it arrived.
func (p *pads) seen() (uint32, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polled, p.polledAt
}

// poll is what a core sees. It may look several times in one frame and sees the
// same presses each time.
func (p *pads) poll() *[engine.MaxPorts]engine.PadState {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.polled, p.polledAt = p.stamp, p.stampAt
	for i := range p.state {
		p.active[i] |= p.pending[i]
		p.pending[i] = 0
		p.snap[i] = p.state[i]
		p.snap[i].Buttons |= p.active[i]
	}
	return &p.snap
}

func (p *pads) frameDone() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active = [engine.MaxPorts]uint16{}
}

// probe test-loads a core and says what it reports, for a server deciding whether
// to accept it. No game is involved. The answer goes out before the core is closed,
// so one that can't shut down cleanly has still answered.
func (r *runner) probe(p []byte, open Open) error {
	var load ipc.Load
	if err := json.Unmarshal(p, &load); err != nil {
		return err
	}
	eng, err := open(load, engine.Handlers{})
	if err != nil {
		return err
	}
	defer eng.Close()
	info := eng.SystemInfo()
	out := ipc.Probe{Name: info.Name, Version: info.Version, Extensions: info.Extensions, NeedFullPath: info.NeedFullPath, BlockExtract: info.BlockExtract}
	if d, ok := eng.(engine.Prober); ok {
		more := d.ProbeDetails()
		out.NoGame, out.HWRender, out.Options = more.NoGame, more.HWRender, more.Options
	}
	b, err := json.Marshal(out)
	if err != nil {
		return err
	}
	r.write(ipc.MsgProbe, b)
	return r.writeE
}
