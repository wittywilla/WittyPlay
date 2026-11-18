// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package ipc

import (
	"testing"

	"github.com/wittywilla/wittyplay/engine"
)

// An NV12 frame is a byte of luma a pixel and half that again of chroma, at a width that's a
// multiple of 4 and an even height; anything else is refused.
func TestParseVideoNV12(t *testing.T) {
	frame := func(w, h, n int) []byte {
		return append(VideoHeader{Width: w, Height: h, Format: engine.FormatNV12}.Append(nil), make([]byte, n)...)
	}
	if _, data, err := ParseVideo(frame(8, 4, 8*4+8*2)); err != nil || len(data) != 48 {
		t.Fatalf("a whole frame: %v, %d bytes", err, len(data))
	}
	for _, c := range []struct{ w, h, n int }{{8, 4, 8 * 4 * 4}, {6, 4, 6*4 + 6*2}, {8, 3, 8*3 + 8}} {
		if _, _, err := ParseVideo(frame(c.w, c.h, c.n)); err == nil {
			t.Errorf("%dx%d with %d bytes should be refused", c.w, c.h, c.n)
		}
	}
}
