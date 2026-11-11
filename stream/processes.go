// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import "github.com/wittywilla/wittyplay/ipc"

// ProcessIDs names what's running for a game, for tools that watch or test it.
type ProcessIDs struct {
	Runner      int `json:"runner"`
	Video       int `json:"video"`
	Audio       int `json:"audio"`
	VideoStarts int `json:"videoStarts"`
}

// RawFrame is a copy of the newest picture to leave the core, for comparing with what a
// viewer sees.
func (s *Session) RawFrame() (ipc.VideoHeader, []byte) {
	s.frameMu.Lock()
	defer s.frameMu.Unlock()
	if s.frameFresh {
		return s.frameHdr, append([]byte(nil), s.frame...)
	}
	return s.shownHdr, append([]byte(nil), s.shown...)
}

// Processes says what's running for this game right now. Anything not running is 0.
func (s *Session) Processes() ProcessIDs {
	var out ProcessIDs
	if s.cmd != nil && s.cmd.Process != nil {
		out.Runner = s.cmd.Process.Pid
	}
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if s.h264 != nil && s.h264.cmd.Process != nil {
		out.Video = s.h264.cmd.Process.Pid
	}
	if s.opus != nil && s.opus.cmd != nil && s.opus.cmd.Process != nil {
		out.Audio = s.opus.cmd.Process.Pid
	}
	out.VideoStarts = s.videoStarts
	return out
}
