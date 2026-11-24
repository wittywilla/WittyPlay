// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"net"
	"os"
	"sync"
)

// behindBridge says this runs in a container on its own private network (Docker's usual bridge),
// where WebRTC can only offer that network's address and a port no one published: no browser
// gets through, and trying first only delays the WebSocket, which uses the server's own port.
// A container on the host's network sees the host's docker0; one that sees no docker0 is bridged.
// It's a variable so tests can say either.
var behindBridge = sync.OnceValue(func() bool {
	if !inContainer() {
		return false
	}
	ifs, err := net.Interfaces()
	if err != nil {
		return false
	}
	for _, i := range ifs {
		if i.Name == "docker0" {
			return false
		}
	}
	return true
})

func inContainer() bool {
	if os.Getenv("WMS_DOCKER") == "1" {
		return true
	}
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

// webRTCReachable: WebRTC can be offered. Behind a bridge it can once the server was told the
// address browsers reach it at and a UDP port published at the same number (Settings, Games).
func (c Config) webRTCReachable() bool {
	return c.LoopbackOnly || !behindBridge() || len(c.PublicIPs) > 0 && c.UDPPort > 0
}

// BehindBridge is behindBridge for the log: why games come by WebSocket here.
func BehindBridge() bool { return behindBridge() }
