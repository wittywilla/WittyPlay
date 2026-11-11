// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
)

// What a game sends back to the hands holding it, and what it asks of them: rumble, lights,
// and motion. The server's docs/GAMING-STREAM.md has the messages.

// Rumble: libretro's two motors, and how long a client keeps one going with nothing new. While a
// motor is on, it's said again every rumbleRenew, so a lost connection or a stuck client stops
// within rumbleHold rather than shaking for good.
const (
	rumbleHold  = 300 * time.Millisecond
	rumbleRenew = 200 * time.Millisecond
)

var motorNames = [2]string{"strong", "weak"}

// MotionWant is a player whose motion the game reads now, and how often.
type MotionWant struct {
	Port  int  `json:"port"`
	Accel bool `json:"accel"`
	Gyro  bool `json:"gyro"`
	Hz    int  `json:"hz"`
}

// LEDState is one of the game's lights.
type LEDState struct {
	Index int `json:"index"`
	State int `json:"state"`
}

type rumbleEvent struct {
	Type     string `json:"type"`
	Port     int    `json:"port"`
	Effect   int    `json:"effect"`
	Motor    string `json:"motor"`
	Strength int    `json:"strength"`
	Ms       int    `json:"ms"`
}

// hapticState is what a session keeps of these, under viewMu.
type hapticState struct {
	rumbling      map[[2]int]int // port, motor: strength, for motors that are on
	rumbleOnce    sync.Once
	motionOffered bool
	motionWant    map[int]MotionWant
	leds          map[int]int
}

// noteRumble takes what the core says a motor is doing now, and passes it on.
func (s *Session) noteRumble(port, effect, strength int) {
	if port < 0 || port >= MaxPlayers || effect < 0 || effect > 1 {
		return
	}
	strength = min(max(strength, 0), 0xffff)
	s.viewMu.Lock()
	if s.rumbling == nil {
		s.rumbling = map[[2]int]int{}
	}
	k := [2]int{port, effect}
	if strength > 0 {
		s.rumbling[k] = strength
	} else {
		delete(s.rumbling, k)
	}
	paused := s.paused
	s.viewMu.Unlock()
	if strength > 0 {
		s.rumbleOnce.Do(func() { go s.rumbleLoop() })
	}
	if !paused || strength == 0 {
		s.sendRumble(port, effect, strength)
	}
}

// sendRumble tells the device holding a player, or every device when nobody holds it.
func (s *Session) sendRumble(port, effect, strength int) {
	ms := 0
	if strength > 0 {
		ms = int(rumbleHold / time.Millisecond)
	}
	b, _ := json.Marshal(rumbleEvent{"rumble", port, effect, motorNames[effect], strength, ms})
	s.viewMu.Lock()
	holder := s.seatOf[port]
	s.viewMu.Unlock()
	if holder != nil {
		holder.sendEvent(b)
		return
	}
	s.broadcastEvent(b)
}

// rumbleLoop says again what's still on, so that it lasts. A paused game is quiet: its motors
// stop at once, and pick up where they were when it carries on.
func (s *Session) rumbleLoop() {
	t := time.NewTicker(rumbleRenew)
	defer t.Stop()
	wasPaused := false
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		s.viewMu.Lock()
		paused := s.paused
		on := make(map[[2]int]int, len(s.rumbling))
		for k, v := range s.rumbling {
			on[k] = v
		}
		s.viewMu.Unlock()
		for k, v := range on {
			switch {
			case paused && !wasPaused:
				s.sendRumble(k[0], k[1], 0)
			case !paused:
				s.sendRumble(k[0], k[1], v)
			}
		}
		wasPaused = paused
	}
}

// noteMotion takes what the core says about motion: that it can read it at all, or which
// player's it wants now.
func (s *Session) noteMotion(e ipc.Event) {
	var out map[string]any
	s.viewMu.Lock()
	s.motionOffered = true
	if e.Port >= 0 && e.Port < MaxPlayers {
		if s.motionWant == nil {
			s.motionWant = map[int]MotionWant{}
		}
		w := MotionWant{Port: e.Port, Accel: e.Accel, Gyro: e.Gyro, Hz: min(max(e.Hz, 0), 60)}
		if w.Hz == 0 && (w.Accel || w.Gyro) {
			w.Hz = 60
		}
		if !w.Accel && !w.Gyro {
			delete(s.motionWant, e.Port)
		} else {
			s.motionWant[e.Port] = w
		}
		out = map[string]any{"type": "motion", "offered": true, "port": w.Port, "accel": w.Accel, "gyro": w.Gyro, "hz": w.Hz}
	} else {
		out = map[string]any{"type": "motion", "offered": true}
	}
	s.viewMu.Unlock()
	b, _ := json.Marshal(out)
	s.broadcastEvent(b)
}

// noteLED passes on a light the game has set.
func (s *Session) noteLED(index, state int) {
	if index < 0 || index > 255 {
		return
	}
	s.viewMu.Lock()
	if s.leds == nil {
		s.leds = map[int]int{}
	}
	s.leds[index] = state
	s.viewMu.Unlock()
	b, _ := json.Marshal(map[string]any{"type": "led", "index": index, "state": state})
	s.broadcastEvent(b)
}

// motionWantLocked lists the players whose motion the game reads now, for a new connection.
func (s *Session) motionWantLocked() []MotionWant {
	out := []MotionWant{}
	for _, w := range s.motionWant {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func (s *Session) ledsLocked() []LEDState {
	out := []LEDState{}
	for i, st := range s.leds {
		out = append(out, LEDState{i, st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

// portsLocked is how many players this game takes: four, or more where the core describes more
// controller ports, as a multitap gives.
func (s *Session) portsLocked() int {
	n := 4
	for _, d := range s.info.Devices {
		n = max(n, d.Port+1)
	}
	return min(n, MaxPlayers)
}
