// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"testing"

	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
)

func TestNV12Filter(t *testing.T) {
	if f := nv12Filter("h264_nvenc", "h264", 1); f != "null" {
		t.Errorf("NVENC at its own size needs nothing done: %q", f)
	}
	if f := nv12Filter("libx264", "h264", 1); f != "null" {
		t.Errorf("libx264 takes NV12 too: %q", f)
	}
	if f := nv12Filter("libvpx", "vp8", 1); f == "null" {
		t.Error("VP8 needs BT.601 and yuv420p")
	}
	if f := nv12Filter("h264_nvenc", "h264", 2); f == "null" {
		t.Error("a doubled frame needs scaling")
	}
}

// NV12 from the graphics card comes back to the colours it was made from, for pictures.
func TestNV12ToRGBA(t *testing.T) {
	// Red above, blue below, as the shader makes them (BT.709, limited range).
	w, h := 4, 2
	data := []byte{63, 63, 63, 63, 32, 32, 32, 32, 102, 240, 102, 240}
	img := toRGBA(ipc.VideoHeader{Width: w, Height: h, Format: engine.FormatNV12}, data)
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("top = %d,%d,%d, want red", r>>8, g>>8, b>>8)
	}
}
