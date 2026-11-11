// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Command wittyplay-input-agent runs beside a standalone emulator, in its container or its
// local sandbox. It turns input from WittyPlay into key and mouse events on the X display, and
// passes on a request to encode the picture at another bitrate. In the local sandbox, which has
// no network, it also carries the picture and sound out through a socket. See package
// inputagent for what it does and doesn't do. It's built into the container images; for the
// local sandbox it sits beside the module.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

func main() {
	port := os.Getenv("WITTYPLAY_INPUT_PORT")
	secret := os.Getenv("WITTYPLAY_INPUT_TOKEN")
	io := os.Getenv("WITTYPLAY_IO")
	w, _ := strconv.Atoi(os.Getenv("WITTYPLAY_WIDTH"))
	h, _ := strconv.Atoi(os.Getenv("WITTYPLAY_HEIGHT"))
	if (port == "" && io == "") || secret == "" {
		fmt.Fprintln(os.Stderr, "wittyplay-input-agent needs WITTYPLAY_INPUT_TOKEN and WITTYPLAY_INPUT_PORT or WITTYPLAY_IO")
		os.Exit(2)
	}
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	keys, err := inputagent.ParseKeys(os.Getenv("WITTYPLAY_KEYS"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wittyplay-input-agent: the key map is unreadable, so its own is used:", err)
	}
	// Cleared once read: the emulator it starts beside doesn't need it.
	_ = os.Unsetenv("WITTYPLAY_INPUT_TOKEN")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	sink := &inputagent.XDoTool{Width: w, Height: h}
	agent := func() *inputagent.Agent { return &inputagent.Agent{Sink: sink, Keys: &keys, Bitrate: setBitrate} }
	if io != "" {
		// The local sandbox: one socket for everything, tried again if WittyPlay isn't there yet.
		video, _ := strconv.Atoi(os.Getenv("WITTYPLAY_RTP_VIDEO"))
		audio, _ := strconv.Atoi(os.Getenv("WITTYPLAY_RTP_AUDIO"))
		for ctx.Err() == nil {
			if err := inputagent.Bridge(ctx, filepath.Join(io, "host.sock"), secret, video, audio, agent()); err != nil {
				fmt.Fprintln(os.Stderr, "wittyplay-input-agent:", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
		}
		return
	}
	l, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := inputagent.ServeWith(ctx, l, secret, agent); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// setBitrate has the entrypoint's encoder start again at another bitrate: the number goes in a
// file the entrypoint's loop reads, and the running FFmpeg is asked to stop. The picture
// freezes for a moment while the new one starts; WittyPlay asks only when the connection needs
// it.
func setBitrate(kbps int) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || kbps < 300 || kbps > 50000 {
		return
	}
	tmp := filepath.Join(dir, "kbps.tmp")
	if os.WriteFile(tmp, []byte(strconv.Itoa(kbps)+"\n"), 0o600) != nil || os.Rename(tmp, filepath.Join(dir, "kbps")) != nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, "encoder.pid"))
	if err != nil {
		return
	}
	if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Signal(syscall.SIGTERM)
		}
	}
}
