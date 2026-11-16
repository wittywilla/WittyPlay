// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// A console shot on white: its photo is served with the white taken away and the credit says so,
// until an administrator turns that off, when it's served as it was downloaded.
func TestConsolePhotoBackgroundRemoved(t *testing.T) {
	m := image.NewNRGBA(image.Rect(0, 0, 60, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 60; x++ {
			c := color.NRGBA{255, 255, 255, 255}
			if x >= 15 && x < 45 && y >= 10 && y < 30 {
				c = color.NRGBA{90, 90, 100, 255}
			}
			m.SetNRGBA(x, y, c)
		}
	}
	var pic bytes.Buffer
	if err := png.Encode(&pic, m); err != nil {
		t.Fatal(err)
	}
	src := &fakeWikimedia{pic: pic.Bytes()}
	was := photoSource
	photoSource = func() consolePhotoSource { return src }
	t.Cleanup(func() { photoSource = was })
	f := newGameFixture(t)
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{"enabled":true}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"cutout":true`) {
		t.Fatalf("opting in: %d %s", w.Code, w.Body)
	}
	w := callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"changed":"background removed"`) || !strings.Contains(w.Body.String(), "cut=true") {
		t.Fatalf("photo: %d %s", w.Code, w.Body)
	}
	corner := func() uint8 {
		w := callRaw(t, f.h, "GET", "/console-photos/snes/image", 3, false, nil)
		if w.Code != 200 {
			t.Fatalf("image: %d", w.Code)
		}
		img, err := png.Decode(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		return color.NRGBAModel.Convert(img.At(1, 1)).(color.NRGBA).A
	}
	if a := corner(); a != 0 {
		t.Errorf("the white corner's alpha is %d, want 0", a)
	}
	// A viewer can't change it; an administrator can, and then the photo is as downloaded.
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 3, false, []byte(`{"cutout":false}`)); w.Code != 403 {
		t.Fatalf("a viewer changed it: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{"cutout":false}`)); w.Code != 200 || !strings.Contains(w.Body.String(), `"cutout":false`) || !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatalf("turning it off: %d %s", w.Code, w.Body)
	}
	if a := corner(); a != 255 {
		t.Errorf("with backgrounds kept, the corner's alpha is %d, want 255", a)
	}
	if w := callRaw(t, f.h, "GET", "/console-photos/snes", 3, false, nil); strings.Contains(w.Body.String(), "background removed") {
		t.Errorf("the credit still says the background was removed: %s", w.Body)
	}
	if src.asked.Load() != 1 {
		t.Errorf("Wikimedia was asked %d times, want once", src.asked.Load())
	}
}

// The NES starts on the NES's own photo; choosing the Famicom fetches that one instead, and the
// answer says which versions there are. A console with no versions says none, and none can be chosen.
func TestConsolePhotoVersions(t *testing.T) {
	src := &fakeWikimedia{pic: pngBytes(t)}
	was := photoSource
	photoSource = func() consolePhotoSource { return src }
	t.Cleanup(func() { photoSource = was })
	f := newGameFixture(t)
	callRaw(t, f.h, "PUT", "/games/console-photos", 1, true, []byte(`{"enabled":true}`))
	w := callRaw(t, f.h, "GET", "/console-photos/nes", 3, false, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"variant":"nes"`) || !strings.Contains(w.Body.String(), `"label":"Famicom"`) {
		t.Fatalf("NES: %d %s", w.Code, w.Body)
	}
	if fa, _ := f.a.readConsolePhoto(f.ctx, "nes"); !sameFile(fa.File, "NES-Console-Set.jpg") {
		t.Errorf("kept %q, want the NES's photo", fa.File)
	}
	if w := callRaw(t, f.h, "PUT", "/console-photos/nes/variant", 3, false, []byte(`{"variant":"famicom"}`)); w.Code != 403 {
		t.Fatalf("a viewer chose: %d", w.Code)
	}
	if w := callRaw(t, f.h, "PUT", "/console-photos/nes/variant", 1, true, []byte(`{"variant":"saturn"}`)); w.Code != 400 {
		t.Fatalf("a version the NES hasn't: %d", w.Code)
	}
	w = callRaw(t, f.h, "PUT", "/console-photos/nes/variant", 1, true, []byte(`{"variant":"famicom"}`))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"variant":"famicom"`) {
		t.Fatalf("choosing the Famicom: %d %s", w.Code, w.Body)
	}
	if fa, _ := f.a.readConsolePhoto(f.ctx, "nes"); !sameFile(fa.File, "Nintendo-Famicom-Console-Set-FL.jpg") {
		t.Errorf("kept %q, want the Famicom's photo", fa.File)
	}
	if src.asked.Load() != 2 {
		t.Errorf("asked %d times, want twice (once for each version)", src.asked.Load())
	}
	if w := callRaw(t, f.h, "GET", "/console-photos/n64", 3, false, nil); strings.Contains(w.Body.String(), "variants") {
		t.Errorf("the N64 has versions: %s", w.Body)
	}
	if w := callRaw(t, f.h, "PUT", "/console-photos/n64/variant", 1, true, []byte(`{"variant":"n64"}`)); w.Code != 400 {
		t.Errorf("a version of a console with none: %d", w.Code)
	}
}
