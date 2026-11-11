// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
)

// Runner is a started runner process, boxed in the way every game's is. Nothing
// has been loaded yet: it waits for a load command on Stdin.
type Runner struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	box    func()
}

// StartRunner starts one runner with its sandbox on. Sessions start theirs here,
// and so does cmd/wittyplay-check, so what is tested is what the server runs.
func StartRunner(program string, args []string, saveDir string, memoryMB int64, log *slog.Logger) (*Runner, error) {
	all := append(append([]string(nil), args...), "-save", saveDir, "-memory-mb", strconv.FormatInt(memoryMB, 10))
	rn, private, err := startRunner(program, all, saveDir, memoryMB, true, log)
	if err != nil && private {
		// Some systems allow a namespace and then refuse what a private folder takes.
		log.Info("sandbox: a private shared memory folder isn't possible here; using the system's", "err", err)
		rn, _, err = startRunner(program, all, saveDir, memoryMB, false, log)
	}
	return rn, err
}

func startRunner(program string, all []string, saveDir string, memoryMB int64, tryPrivate bool, log *slog.Logger) (*Runner, bool, error) {
	cmd := exec.Command(program, all...)
	cmd.Dir = saveDir
	cmd.Env = runnerEnv()
	if slices.Contains(all, "-own-home") {
		// A core that keeps its files under the home folder has that in its save folder,
		// the one place it can write and where its saves belong.
		cmd.Env = append(cmd.Env, "HOME="+saveDir)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, false, err
	}
	prepareSandbox(cmd, saveDir, log)
	private := tryPrivate && slices.Contains(all, "-shared-memory") && privateSharedMemory(cmd, log)
	err = cmd.Start()
	sandboxStarted(cmd)
	if err != nil {
		return nil, private, fmt.Errorf("starting the game runner: %w", err)
	}
	return &Runner{Cmd: cmd, Stdin: stdin, Stdout: stdout, Stderr: stderr, box: lockDown(cmd, memoryMB, log)}, private, nil
}

// Release frees the sandbox's resources once the runner has gone.
func (r *Runner) Release() {
	if r.box != nil {
		r.box()
	}
}
