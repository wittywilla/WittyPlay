// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package inputagent

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// The bridge is for the sandbox without Docker (sandbox/local), which has no network: only a
// loopback of its own that nothing outside can reach. One Unix socket in a folder both sides can
// see carries everything instead. The agent, inside, connects to it, says the session's secret,
// and then:
//
//   - sends up what the sandbox's FFmpeg sent to its loopback, a packet at a time: a kind byte
//     ('v' or 'a'), the packet's length as two bytes, high first, then the RTP packet;
//   - reads down the same frames Serve reads from TCP: a length byte and a message.
//
// The emulator can reach that socket too. Without the secret, which it's never given, it gets
// nothing; and what the secret unlocks is only what it could do anyway, since the picture is
// its own.

// Kinds of packet the agent sends up.
const (
	KindVideo = 'v'
	KindAudio = 'a'
)

// maxPacket is the largest RTP packet carried. FFmpeg is told to keep them to 1200 bytes.
const maxPacket = 1500

// Host is WittyPlay's end of the bridge.
type Host struct {
	Secret string
	// OnPacket hears each RTP packet. The slice is only good until it returns.
	OnPacket func(kind byte, pkt []byte)

	l    net.Listener
	mu   sync.Mutex
	conn net.Conn
}

// Listen starts the host's end on a Unix socket at path, which must not exist yet.
func (h *Host) Listen(path string) error {
	if len(h.Secret) < 32 {
		return errors.New("the bridge needs a secret of 32 characters or more")
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	h.l = l
	go h.accept()
	return nil
}

func (h *Host) accept() {
	for {
		c, err := h.l.Accept()
		if err != nil {
			return
		}
		go h.serve(c)
	}
}

func (h *Host) serve(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReaderSize(c, 4096)
	line, err := r.ReadSlice('\n')
	if err != nil || subtle.ConstantTimeCompare(line[:len(line)-1], []byte(h.Secret)) != 1 {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	h.mu.Lock()
	if h.conn != nil {
		h.conn.Close() // the agent started again: the newest one is the one to talk to
	}
	h.conn = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.conn == c {
			h.conn = nil
		}
		h.mu.Unlock()
	}()
	var head [3]byte
	buf := make([]byte, maxPacket)
	for {
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(head[1:]))
		if n == 0 || n > maxPacket || (head[0] != KindVideo && head[0] != KindAudio) {
			return
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return
		}
		if h.OnPacket != nil {
			h.OnPacket(head[0], buf[:n])
		}
	}
}

// Send passes one input message down. Input that can't be sent is dropped.
func (h *Host) Send(p []byte) {
	if len(p) == 0 || len(p) > MaxFrame {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	if _, err := h.conn.Write(append([]byte{byte(len(p))}, p...)); err != nil {
		h.conn.Close()
		h.conn = nil
	}
}

// Close ends the bridge.
func (h *Host) Close() {
	if h.l != nil {
		h.l.Close()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		h.conn.Close()
		h.conn = nil
	}
}

// Bridge is the agent's end: it connects to the socket at path, sends up what arrives on the two
// UDP ports of its own loopback, and applies the input that comes down. It returns when the
// connection ends or ctx does.
func Bridge(ctx context.Context, path, secret string, videoPort, audioPort int, agent *Agent) error {
	c, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return err
	}
	defer c.Close()
	defer agent.Release()
	if _, err := io.WriteString(c, secret+"\n"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wmu sync.Mutex
	up := func(kind byte, port int) error {
		u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
		if err != nil {
			return err
		}
		go func() { <-ctx.Done(); u.Close() }()
		go func() {
			defer cancel()
			buf := make([]byte, 3+maxPacket)
			for {
				n, err := u.Read(buf[3:])
				if err != nil {
					return
				}
				buf[0] = kind
				binary.BigEndian.PutUint16(buf[1:], uint16(n))
				wmu.Lock()
				_ = c.SetWriteDeadline(time.Now().Add(time.Second))
				_, err = c.Write(buf[:3+n])
				wmu.Unlock()
				if err != nil {
					return
				}
			}
		}()
		return nil
	}
	if err := up(KindVideo, videoPort); err != nil {
		return err
	}
	if err := up(KindAudio, audioPort); err != nil {
		return err
	}
	go func() { <-ctx.Done(); c.Close() }()
	r := bufio.NewReaderSize(c, 512)
	buf := make([]byte, MaxFrame)
	for {
		n, err := r.ReadByte()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if n == 0 || int(n) > MaxFrame {
			return errors.New("a bad frame")
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		_ = agent.Handle(buf[:n])
	}
}
