// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// A runner whose core asked to be traced by its parent, as PCSX ReARMed does on a Mac, keeps
// running in the environment a runner gets: nothing signals it, so it never stops to wait for
// a parent that won't let it go on. The same child in an ordinary environment stops within a
// second, which is what froze PlayStation games on a Mac at "the game took too long to load".
func TestATracedRunnerKeepsRunning(t *testing.T) {
	if os.Getenv("WITTYPLAY_TRACED_CHILD") == "1" {
		tracedChild()
		return
	}
	run := func(env []string) (finished bool) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestATracedRunnerKeepsRunning$")
		cmd.Env = append(env, "WITTYPLAY_TRACED_CHILD=1")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err == nil {
				return true
			}
			// A stopped child is reported as an error, and is still there.
			_ = cmd.Process.Kill()
			t.Logf("the child: %v", err)
			return false
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return false
		}
	}
	if !run(runnerEnv()) {
		t.Fatal("a traced runner stopped in a runner's environment")
	}
	if testing.Short() {
		return
	}
	// What made it necessary: the Go runtime's own signals stop a traced process. If this Go
	// stops sending them, the setting is no longer needed, which isn't a failure.
	if run([]string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}) {
		t.Log("a traced child kept running without the setting too: this Go no longer needs it")
	}
}

// tracedChild does what PCSX ReARMed's retro_init does, then keeps the Go runtime busy long
// enough that it would preempt something.
func tracedChild() {
	if _, _, e := syscall.Syscall(syscall.SYS_PTRACE, 0 /* PT_TRACE_ME */, 0, 0); e != 0 {
		os.Exit(3)
	}
	stop := time.Now().Add(2 * time.Second)
	for i := 0; i < runtime.NumCPU(); i++ {
		go func() {
			for time.Now().Before(stop) {
				_ = make([]byte, 1<<16) // garbage, so the collector runs and stops the world
			}
		}()
	}
	var sink []byte
	for time.Now().Before(stop) {
		sink = make([]byte, 1<<20)
	}
	_ = sink
	os.Exit(0)
}
