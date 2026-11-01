package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// chaos ends each process in turn while a game is being played, and sees what becomes of
// it. A fault passes if a moving picture comes back without anyone doing anything, and
// nothing that was saved has gone.
func chaos(args []string) error {
	fs := flag.NewFlagSet("chaos", flag.ExitOnError)
	f := addRigFlags(fs)
	child := fs.Bool("server-child", false, "be the server the last test ends: it prints what it started and plays until killed")
	_ = fs.Parse(args)
	if *child {
		return chaosChild(f)
	}
	r, err := f.start(url0())
	if err != nil {
		return err
	}
	defer r.close()
	failed := 0
	check := func(name string, ok bool, note string) {
		word := "PASS"
		if !ok {
			word = "FAIL"
			failed++
		}
		fmt.Printf("%s  %-34s %s\n", word, name, note)
	}
	if !r.waitForFrames(60 * time.Second) {
		return fmt.Errorf("no picture to begin with")
	}

	// Sound and video encoders end by themselves.
	gap, ok := r.killAndTime(func() int { return r.session().Processes().Video })
	check("the video encoder is killed", ok, fmt.Sprintf("picture back after %s, encoder restarted", gap))
	before := r.session().Processes().Audio
	if before != 0 {
		_ = killPID(before)
		time.Sleep(3 * time.Second)
		after := r.session().Processes().Audio
		check("the sound encoder is killed", after != 0 && after != before, fmt.Sprintf("was %d, now %d", before, after))
	} else {
		check("the sound encoder is killed", false, "there wasn't one: this FFmpeg has no Opus")
	}

	// The browser goes away and comes back: a closed tab, or a crash.
	if f.browser != "" {
		r.stopBrowser()
		paused := false
		for end := time.Now().Add(20 * time.Second); !paused && time.Now().Before(end); {
			time.Sleep(200 * time.Millisecond)
			paused = r.session().Stats().Paused
		}
		err := r.startBrowser()
		ok := err == nil && r.waitForNewFrames(30*time.Second)
		check("the browser is killed", ok && paused, fmt.Sprintf("the game paused meanwhile: %v, and carried on when it came back", paused))
	}

	// A network that goes away for a while, as when a laptop changes networks.
	if r.relay != nil {
		for _, secs := range []int{2, 8} {
			beat, _ := r.beatNow()
			r.relay.blocked.Store(true)
			time.Sleep(time.Duration(secs) * time.Second)
			r.relay.blocked.Store(false)
			t := time.Now()
			ok := r.waitForNewFrames(30 * time.Second)
			now, _ := r.beatNow()
			check(fmt.Sprintf("the network is out for %d s", secs), ok, fmt.Sprintf("picture back %s after it returned; reconnects %d to %d", time.Since(t).Round(100*time.Millisecond), beat.Reconnects, now.Reconnects))
		}
	}

	// The runner dies. What was written stays, and the game can start again from it.
	saves := r.pressStartThenWaitForSave()
	sess := r.session()
	runner := sess.Processes().Runner
	_ = killPID(runner)
	select {
	case <-sess.Done():
		check("the runner is killed", true, "the game ended and the player is told why")
	case <-time.After(15 * time.Second):
		check("the runner is killed", false, "the session never noticed")
	}
	b, _ := os.ReadFile(filepath.Join(f.game.save, "save.srm"))
	var kept uint32
	if len(b) >= 4 {
		kept = binary.LittleEndian.Uint32(b)
	}
	check("its battery save survives", saves > 0 && kept >= saves-1 && kept > 0, fmt.Sprintf("the game had counted %d presses; the file says %d", saves, kept))
	if err := r.startGame(); err != nil {
		check("the game starts again", false, err.Error())
	} else {
		if f.browser != "" {
			r.stopBrowser()
			_ = r.startBrowser()
		}
		ok := r.waitForNewFrames(30 * time.Second)
		check("the game starts again", ok, "picture back on a new session")
		c := make(chan struct{})
		go func() { r.session().Stop(); close(c) }()
		<-c
	}
	r.stopBrowser()

	// The server dies, and nothing it started may be left behind.
	self, _ := os.Executable()
	ch := exec.Command(self, append([]string{"chaos", "-server-child"}, args...)...)
	var out bytes.Buffer
	ch.Stdout = &out
	ch.Stderr = &out
	if err := ch.Start(); err != nil {
		return err
	}
	var kids stream.ProcessIDs
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if i := strings.Index(out.String(), "PIDS "); i >= 0 {
			line := out.String()[i+5:]
			if j := strings.IndexByte(line, '\n'); j > 0 {
				_ = json.Unmarshal([]byte(line[:j]), &kids)
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = ch.Process.Kill()
	_ = ch.Wait()
	time.Sleep(3 * time.Second)
	check("the server is killed", kids.Runner != 0 && !alive(kids.Runner), fmt.Sprintf("its runner %d is alive: %v", kids.Runner, alive(kids.Runner)))

	if failed > 0 {
		return fmt.Errorf("%d of them failed", failed)
	}
	return nil
}

// chaosChild is a server for the last test to kill.
func chaosChild(f *rigFlags) error {
	f.browser = ""
	r, err := f.start(nil)
	if err != nil {
		return err
	}
	defer r.close()
	b, _ := json.Marshal(r.session().Processes())
	fmt.Printf("PIDS %s\n", b)
	time.Sleep(10 * time.Minute)
	return nil
}

func url0() map[string][]string { return map[string][]string{"every": {"3600"}} }

// killAndTime ends a process and says how long the picture was stopped for.
func (r *rig) killAndTime(pid func() int) (time.Duration, bool) {
	p := pid()
	if p == 0 {
		return 0, false
	}
	start := time.Now()
	if err := killPID(p); err != nil {
		return 0, false
	}
	time.Sleep(100 * time.Millisecond)
	before, _ := r.beatNow()
	for time.Since(start) < 20*time.Second {
		now, _ := r.beatNow()
		if now.Frames > before.Frames+5 {
			return time.Since(start).Round(10 * time.Millisecond), pid() != p
		}
		time.Sleep(20 * time.Millisecond)
	}
	return time.Since(start), false
}

func (r *rig) waitForFrames(limit time.Duration) bool { return r.waitForNewFrames(limit) }

// waitForNewFrames waits for the page to say the picture is moving.
func (r *rig) waitForNewFrames(limit time.Duration) bool {
	before, _ := r.beatNow()
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		now, ago := r.beatNow()
		if now.Frames > before.Frames+10 && ago < time.Second {
			return true
		}
		// A new browser counts from nothing.
		if now.Frames < before.Frames {
			before = now
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// pressStartThenWaitForSave uses the test core's counter of Start presses, which is its
// battery save, and waits for one to reach the disc. It says how many there have been.
func (r *rig) pressStartThenWaitForSave() uint32 {
	sess := r.session()
	var count uint32
	send := func(buttons uint16) {
		msg := make([]byte, ipc.InputSize)
		binary.LittleEndian.PutUint16(msg[1:], buttons)
		sess.Input(msg)
	}
	for i := 0; i < 5; i++ {
		send(1 << engine.ButtonStart)
		time.Sleep(100 * time.Millisecond)
		send(0)
		time.Sleep(100 * time.Millisecond)
		count++
	}
	// Battery saves go out every half a minute when something has changed.
	path := filepath.Join(r.f.game.save, "save.srm")
	end := time.Now().Add(40 * time.Second)
	for time.Now().Before(end) {
		if b, err := os.ReadFile(path); err == nil && len(b) >= 4 && binary.LittleEndian.Uint32(b) >= count {
			return binary.LittleEndian.Uint32(b)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return count
}

func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

var _ = strconv.Itoa
