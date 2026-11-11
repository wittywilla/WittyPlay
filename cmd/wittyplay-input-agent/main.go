// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Command wittyplay-input-agent runs inside a standalone emulator's container and turns a player's
// input from WittyPlay into key and mouse events on the container's X display. See package
// inputagent for what it does and doesn't do. It's built into the container images, not shipped
// with the module.
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

func main() {
	port := os.Getenv("WITTYPLAY_INPUT_PORT")
	secret := os.Getenv("WITTYPLAY_INPUT_TOKEN")
	w, _ := strconv.Atoi(os.Getenv("WITTYPLAY_WIDTH"))
	h, _ := strconv.Atoi(os.Getenv("WITTYPLAY_HEIGHT"))
	if port == "" || secret == "" {
		fmt.Fprintln(os.Stderr, "wittyplay-input-agent needs WITTYPLAY_INPUT_PORT and WITTYPLAY_INPUT_TOKEN")
		os.Exit(2)
	}
	if w <= 0 || h <= 0 {
		w, h = 1280, 720
	}
	// Cleared once read: the emulator it starts beside doesn't need it.
	_ = os.Unsetenv("WITTYPLAY_INPUT_TOKEN")
	l, err := net.Listen("tcp", ":"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := inputagent.Serve(ctx, l, secret, &inputagent.XDoTool{Width: w, Height: h}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
