// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"

	"github.com/wittywilla/wittyplay/emulators/inputmap"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// A Mac: sandbox-exec around the emulator, ScreenCaptureKit for its window and sound, and
// CGEventPostToPid for its keys. Everything is reached through purego, as the game runner
// reaches OpenGL, so the module stays one program built without cgo.

func unsupported() string { return "" }

func platformMissing() []string {
	var out []string
	if _, err := os.Stat("/usr/bin/sandbox-exec"); err != nil {
		out = append(out, "sandbox-exec is missing from this Mac, and nothing else here can box an emulator in")
	}
	if err := loadMac(); err != nil {
		return append(out, "this Mac's capture frameworks couldn't be loaded: "+err.Error())
	}
	if !mac.screenAccess() {
		out = append(out, "macOS hasn't given this server permission to record the screen: in System Settings, Privacy & Security, Screen & System Audio Recording, turn it on for the program that runs the server (then start the server again)")
	}
	if !mac.postAccess() {
		out = append(out, "macOS hasn't given this server permission to send key presses: in System Settings, Privacy & Security, Accessibility, turn it on for the program that runs the server")
	}
	return out
}

func sandboxName() string { return "sandbox-exec" }

func memoryMB() int64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(n >> 20)
}

func hideConsole(*exec.Cmd) {}

func platformEncoders() []string { return []string{"x264", "videotoolbox"} }

// ---------------------------------------------------------------- the sandbox

// sbpl quotes text for a sandbox profile, so a folder's name can't end its string and add a
// rule of its own.
func sbpl(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// realPath is a path as the sandbox sees it: /tmp, /var and /etc are links on a Mac.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Profile is the sandbox an emulator runs in: no network but a port bound on loopback, no programs but its own, and
// writing only to its own save folder and the per-user cache folder macOS gives every program
// (where the graphics driver keeps compiled shaders); its temporary folder is in its save folder.
// It can read what any program here can, which a game's own file and its BIOS need.
func Profile(appDir, saveDir, cacheDir string) string {
	var b strings.Builder
	// No network, except binding a port on this machine's loopback: some emulators (Lichii's
	// UDP services) won't start without that, and a bound port reaches nothing by itself.
	b.WriteString(`(version 1)(allow default)(deny network*)(allow network-bind (local ip "localhost:*"))`)
	b.WriteString(`(deny process-exec*)(allow process-exec (subpath ` + sbpl(realPath(appDir)) + `))`)
	b.WriteString(`(deny file-write*)(allow file-write* (subpath ` + sbpl(realPath(saveDir)) + `)`)
	if cacheDir != "" {
		b.WriteString(` (subpath ` + sbpl(realPath(cacheDir)) + `)`)
	}
	b.WriteString(` (literal "/dev/null") (literal "/dev/zero") (regex #"^/dev/tty") (regex #"^/dev/ptmx"))`)
	return b.String()
}

type window struct {
	id   uint32
	x, y float64
	w, h float64
}

type box struct{ pgid int }

func (b *box) close()                      {}
func (b *box) started(cmd *exec.Cmd) error { b.pgid = cmd.Process.Pid; return nil }

func sandbox(cmd *exec.Cmd, pl plan) (*box, error) {
	const tool = "/usr/bin/sandbox-exec"
	if _, err := os.Stat(tool); err != nil {
		return nil, errors.New("sandbox-exec is missing")
	}
	cmd.Args = append([]string{tool, "-p", Profile(pl.AppDir, pl.SaveDir, userCacheDir()), cmd.Path}, cmd.Args[1:]...)
	cmd.Path = tool
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return &box{}, nil
}

var (
	cacheOnce sync.Once
	cacheDir  string
)

// userCacheDir is this account's cache folder (getconf DARWIN_USER_CACHE_DIR), where Metal
// keeps the shaders it compiled.
func userCacheDir() string {
	cacheOnce.Do(func() {
		if b, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_CACHE_DIR").Output(); err == nil {
			if d := strings.TrimSpace(string(b)); filepath.IsAbs(d) {
				cacheDir = d
			}
		}
	})
	return cacheDir
}

func systemEnv(plan) []string { return []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"} }

func suspend(cmd *exec.Cmd, _ *box, paused bool) error {
	if cmd.Process == nil {
		return errors.New("it isn't running")
	}
	sig := syscall.SIGCONT
	if paused {
		sig = syscall.SIGSTOP
	}
	return syscall.Kill(-cmd.Process.Pid, sig)
}

func terminate(cmd *exec.Cmd, _ *box) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func kill(cmd *exec.Cmd, _ *box) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// ---------------------------------------------------------------- the frameworks

type cgPoint struct{ X, Y float64 }
type cgRect struct{ X, Y, W, H float64 }

var mac struct {
	once sync.Once
	err  error

	screenAccess func() bool
	postAccess   func() bool

	windowList     func(option, relativeTo uint32) uintptr
	arrayCount     func(arr uintptr) int
	arrayAt        func(arr uintptr, i int) uintptr
	dictGet        func(dict, key uintptr) uintptr
	numberGet      func(num uintptr, kind int, out *int64) bool
	rectFromDict   func(dict uintptr, rect *cgRect) bool
	release        func(ref uintptr)
	keyEvent       func(source uintptr, key uint16, down bool) uintptr
	mouseEvent     func(source uintptr, kind uint32, at cgPoint, button uint32) uintptr
	postToPid      func(pid int32, ev uintptr)
	keyOwnerPID    uintptr
	keyNumber      uintptr
	keyBounds      uintptr
	keyLayer       uintptr
	sampleImage    func(sbuf uintptr) uintptr
	lockBase       func(pb uintptr, flags uint64) int32
	unlockBase     func(pb uintptr, flags uint64) int32
	baseAddress    func(pb uintptr) uintptr
	bytesPerRow    func(pb uintptr) uintptr
	pbWidth        func(pb uintptr) uintptr
	pbHeight       func(pb uintptr) uintptr
	audioList      func(sbuf uintptr, sizeNeeded uintptr, list unsafe.Pointer, listSize uintptr, alloc, memAlloc uintptr, flags uint32, block *uintptr) int32
	queueCreate    func(label *byte, attr uintptr) uintptr
	outputClass    objc.Class
	outputClassErr error
}

func loadMac() error {
	mac.once.Do(func() { mac.err = loadMacOnce() })
	return mac.err
}

func loadMacOnce() error {
	open := func(path string) (uintptr, error) { return purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW) }
	cg, err := open("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
	if err != nil {
		return err
	}
	cf, err := open("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	if err != nil {
		return err
	}
	cm, err := open("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
	if err != nil {
		return err
	}
	cv, err := open("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
	if err != nil {
		return err
	}
	if _, err := open("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit"); err != nil {
		return fmt.Errorf("ScreenCaptureKit (macOS 13 or later): %w", err)
	}
	sys, err := open("/usr/lib/libSystem.B.dylib")
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&mac.screenAccess, cg, "CGPreflightScreenCaptureAccess")
	purego.RegisterLibFunc(&mac.postAccess, cg, "CGPreflightPostEventAccess")
	purego.RegisterLibFunc(&mac.windowList, cg, "CGWindowListCopyWindowInfo")
	purego.RegisterLibFunc(&mac.rectFromDict, cg, "CGRectMakeWithDictionaryRepresentation")
	purego.RegisterLibFunc(&mac.keyEvent, cg, "CGEventCreateKeyboardEvent")
	purego.RegisterLibFunc(&mac.mouseEvent, cg, "CGEventCreateMouseEvent")
	purego.RegisterLibFunc(&mac.postToPid, cg, "CGEventPostToPid")
	purego.RegisterLibFunc(&mac.arrayCount, cf, "CFArrayGetCount")
	purego.RegisterLibFunc(&mac.arrayAt, cf, "CFArrayGetValueAtIndex")
	purego.RegisterLibFunc(&mac.dictGet, cf, "CFDictionaryGetValue")
	purego.RegisterLibFunc(&mac.numberGet, cf, "CFNumberGetValue")
	purego.RegisterLibFunc(&mac.release, cf, "CFRelease")
	purego.RegisterLibFunc(&mac.sampleImage, cm, "CMSampleBufferGetImageBuffer")
	purego.RegisterLibFunc(&mac.audioList, cm, "CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer")
	purego.RegisterLibFunc(&mac.lockBase, cv, "CVPixelBufferLockBaseAddress")
	purego.RegisterLibFunc(&mac.unlockBase, cv, "CVPixelBufferUnlockBaseAddress")
	purego.RegisterLibFunc(&mac.baseAddress, cv, "CVPixelBufferGetBaseAddress")
	purego.RegisterLibFunc(&mac.bytesPerRow, cv, "CVPixelBufferGetBytesPerRow")
	purego.RegisterLibFunc(&mac.pbWidth, cv, "CVPixelBufferGetWidth")
	purego.RegisterLibFunc(&mac.pbHeight, cv, "CVPixelBufferGetHeight")
	purego.RegisterLibFunc(&mac.queueCreate, sys, "dispatch_queue_create")
	for name, dst := range map[string]*uintptr{"kCGWindowOwnerPID": &mac.keyOwnerPID, "kCGWindowNumber": &mac.keyNumber, "kCGWindowBounds": &mac.keyBounds, "kCGWindowLayer": &mac.keyLayer} {
		p, err := purego.Dlsym(cg, name)
		if err != nil {
			return err
		}
		*dst = *(*uintptr)(cptr(p)) // a CFStringRef the framework exports
	}
	return nil
}

// ---------------------------------------------------------------- its window

const (
	listOnScreenOnly       = 1 << 0
	listExcludeDesktopElts = 1 << 4
	cfNumberSInt64         = 4
)

// findWindow waits for the emulator's largest ordinary window. Window numbers, owners and sizes
// need no permission to read; only what's in them does.
func findWindow(ctx context.Context, pid int) (window, error) {
	if err := loadMac(); err != nil {
		return window{}, err
	}
	for {
		if w, ok := largestWindow(pid); ok {
			return w, nil
		}
		select {
		case <-ctx.Done():
			return window{}, errors.New("it opened no window")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func largestWindow(pid int) (window, bool) {
	arr := mac.windowList(listOnScreenOnly|listExcludeDesktopElts, 0)
	if arr == 0 {
		return window{}, false
	}
	defer mac.release(arr)
	var best window
	found := false
	num := func(dict, key uintptr) int64 {
		var v int64
		if n := mac.dictGet(dict, key); n != 0 {
			mac.numberGet(n, cfNumberSInt64, &v)
		}
		return v
	}
	for i := range mac.arrayCount(arr) {
		d := mac.arrayAt(arr, i)
		if num(d, mac.keyOwnerPID) != int64(pid) || num(d, mac.keyLayer) != 0 {
			continue
		}
		var r cgRect
		if b := mac.dictGet(d, mac.keyBounds); b == 0 || !mac.rectFromDict(b, &r) {
			continue
		}
		if r.W < 160 || r.H < 120 || r.W*r.H <= best.w*best.h {
			continue
		}
		best = window{id: uint32(num(d, mac.keyNumber)), x: r.X, y: r.Y, w: r.W, h: r.H}
		found = true
	}
	return best, found
}

// ---------------------------------------------------------------- keys and the pointer

type macSink struct {
	pid  int32
	win  *window
	down bool
}

func newSink(pid int, win window, _, _ int) inputagent.Sink {
	w := win
	return &macSink{pid: int32(pid), win: &w}
}

func (s *macSink) Key(name string, down bool) error {
	code, ok := inputmap.HostKey("darwin", name)
	if !ok {
		return fmt.Errorf("no Mac key for %q", name)
	}
	ev := mac.keyEvent(0, uint16(code), down)
	if ev == 0 {
		return errors.New("macOS wouldn't make a key event")
	}
	mac.postToPid(s.pid, ev)
	mac.release(ev)
	return nil
}

const (
	evLeftDown    = 1
	evLeftUp      = 2
	evMouseMoved  = 5
	evLeftDragged = 6
)

func (s *macSink) Pointer(fx, fy float64, pressed bool) error {
	if w, ok := largestWindow(int(s.pid)); ok {
		*s.win = w // it may have moved
	}
	at := cgPoint{s.win.x + math.Max(0, math.Min(1, fx))*s.win.w, s.win.y + math.Max(0, math.Min(1, fy))*s.win.h}
	kind := uint32(evMouseMoved)
	switch {
	case pressed && !s.down:
		kind = evLeftDown
	case !pressed && s.down:
		kind = evLeftUp
	case pressed:
		kind = evLeftDragged
	}
	s.down = pressed
	ev := mac.mouseEvent(0, kind, at, 0)
	if ev == 0 {
		return errors.New("macOS wouldn't make a mouse event")
	}
	mac.postToPid(s.pid, ev)
	mac.release(ev)
	return nil
}

// ---------------------------------------------------------------- ScreenCaptureKit

const (
	outputScreen = 0
	outputAudio  = 1
	pixelBGRA    = 0x42475241 // 'BGRA'
)

// sckCapture holds the newest picture, which a ticker hands FFmpeg at a steady rate (the
// window sends a frame only when something changes), and the sound as it comes.
type sckCapture struct {
	win        window
	w, h, fps  int
	mu         sync.Mutex
	frame      []byte
	audio      chan []byte
	stream     objc.ID
	stopOnce   sync.Once
	handlerKey uintptr
}

// Captures by the delegate object's address: a callback finds its capture here.
var (
	capturesMu sync.Mutex
	captures   = map[uintptr]*sckCapture{}
)

func newCapture(win window, w, h, fps int) (capture, error) {
	if err := loadMac(); err != nil {
		return nil, err
	}
	if !mac.screenAccess() {
		return nil, fmt.Errorf("%w: give it Screen & System Audio Recording in System Settings, Privacy & Security", errNoPermission)
	}
	return &sckCapture{win: win, w: w, h: h, fps: fps, frame: make([]byte, w*h*4), audio: make(chan []byte, 64)}, nil
}

func (c *sckCapture) inputs() []string {
	return []string{"-f", "rawvideo", "-pix_fmt", "bgra", "-video_size", strconv.Itoa(c.w) + "x" + strconv.Itoa(c.h), "-framerate", strconv.Itoa(c.fps), "-i", "pipe:0",
		"-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:3"}
}

func sel(name string) objc.SEL { return objc.RegisterName(name) }

// outputClass is the object ScreenCaptureKit hands its samples to.
func outputClass() (objc.Class, error) {
	if mac.outputClass != 0 || mac.outputClassErr != nil {
		return mac.outputClass, mac.outputClassErr
	}
	var protos []*objc.Protocol
	if p := objc.GetProtocol("SCStreamOutput"); p != nil {
		protos = append(protos, p)
	}
	mac.outputClass, mac.outputClassErr = objc.RegisterClass("WittyPlayStreamOutput", objc.GetClass("NSObject"), protos, nil, []objc.MethodDef{{
		Cmd: sel("stream:didOutputSampleBuffer:ofType:"),
		Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, kind int) {
			capturesMu.Lock()
			c := captures[uintptr(self)]
			capturesMu.Unlock()
			if c != nil {
				c.sample(sbuf, kind)
			}
		},
	}})
	return mac.outputClass, mac.outputClassErr
}

func (c *sckCapture) start() error {
	cls, err := outputClass()
	if err != nil {
		return err
	}
	// Find the window among what may be shared.
	type found struct {
		win objc.ID
		err string
	}
	got := make(chan found, 1)
	wantID := c.win.id
	block := objc.NewBlock(func(_ objc.Block, content, nserr objc.ID) {
		if content == 0 {
			got <- found{err: describe(nserr)}
			return
		}
		windows := content.Send(sel("windows"))
		n := objc.Send[uint](windows, sel("count"))
		for i := range n {
			w := windows.Send(sel("objectAtIndex:"), i)
			if objc.Send[uint32](w, sel("windowID")) == wantID {
				w.Send(sel("retain"))
				got <- found{win: w}
				return
			}
		}
		got <- found{err: "the emulator's window isn't one macOS will share"}
	})
	defer block.Release()
	objc.ID(objc.GetClass("SCShareableContent")).Send(sel("getShareableContentExcludingDesktopWindows:onScreenWindowsOnly:completionHandler:"), true, true, block)
	var f found
	select {
	case f = <-got:
	case <-time.After(10 * time.Second):
		return errors.New("ScreenCaptureKit didn't answer")
	}
	if f.win == 0 {
		return fmt.Errorf("%w: %s", errNoPermission, f.err)
	}
	defer f.win.Send(sel("release"))
	filter := objc.ID(objc.GetClass("SCContentFilter")).Send(sel("alloc")).Send(sel("initWithDesktopIndependentWindow:"), f.win)
	cfg := objc.ID(objc.GetClass("SCStreamConfiguration")).Send(sel("new"))
	cfg.Send(sel("setWidth:"), uint(c.w))
	cfg.Send(sel("setHeight:"), uint(c.h))
	cfg.Send(sel("setPixelFormat:"), uint32(pixelBGRA))
	cfg.Send(sel("setShowsCursor:"), false)
	cfg.Send(sel("setQueueDepth:"), 3)
	cfg.Send(sel("setCapturesAudio:"), true)
	cfg.Send(sel("setSampleRate:"), 48000)
	cfg.Send(sel("setChannelCount:"), 2)
	stream := objc.ID(objc.GetClass("SCStream")).Send(sel("alloc")).Send(sel("initWithFilter:configuration:delegate:"), filter, cfg, objc.ID(0))
	filter.Send(sel("release"))
	cfg.Send(sel("release"))
	if stream == 0 {
		return errors.New("ScreenCaptureKit wouldn't make a stream")
	}
	out := objc.ID(cls).Send(sel("new"))
	capturesMu.Lock()
	captures[uintptr(out)] = c
	capturesMu.Unlock()
	c.handlerKey, c.stream = uintptr(out), stream
	label := []byte("org.wittyplay.capture\x00")
	queue := mac.queueCreate(&label[0], 0)
	for _, kind := range []int{outputScreen, outputAudio} {
		if !objc.Send[bool](stream, sel("addStreamOutput:type:sampleHandlerQueue:error:"), out, kind, queue, uintptr(0)) {
			return errors.New("ScreenCaptureKit wouldn't take the capture's output")
		}
	}
	started := make(chan string, 1)
	sb := objc.NewBlock(func(_ objc.Block, nserr objc.ID) {
		if nserr != 0 {
			started <- describe(nserr)
			return
		}
		started <- ""
	})
	defer sb.Release()
	stream.Send(sel("startCaptureWithCompletionHandler:"), sb)
	select {
	case why := <-started:
		if why != "" {
			return fmt.Errorf("%w: %s", errNoPermission, why)
		}
	case <-time.After(10 * time.Second):
		return errors.New("the capture didn't start")
	}
	return nil
}

func describe(nserr objc.ID) string {
	if nserr == 0 {
		return "no reason given"
	}
	s := nserr.Send(sel("localizedDescription"))
	if s == 0 {
		return "no reason given"
	}
	p := objc.Send[*byte](s, sel("UTF8String"))
	if p == nil {
		return "no reason given"
	}
	var b []byte
	for i := 0; i < 1024; i++ {
		ch := *(*byte)(unsafe.Add(unsafe.Pointer(p), i))
		if ch == 0 {
			break
		}
		b = append(b, ch)
	}
	return string(b)
}

// sample takes one picture or piece of sound, on ScreenCaptureKit's own thread.
func (c *sckCapture) sample(sbuf uintptr, kind int) {
	switch kind {
	case outputScreen:
		pb := mac.sampleImage(sbuf)
		if pb == 0 {
			return // nothing changed: a frame that only says so
		}
		if mac.lockBase(pb, 1) != 0 {
			return
		}
		defer mac.unlockBase(pb, 1)
		w, h, stride := int(mac.pbWidth(pb)), int(mac.pbHeight(pb)), int(mac.bytesPerRow(pb))
		base := mac.baseAddress(pb)
		if base == 0 || w <= 0 || h <= 0 || stride < w*4 {
			return
		}
		src := unsafe.Slice((*byte)(cptr(base)), stride*h) // the pixel buffer, locked
		c.mu.Lock()
		// The window's picture is scaled to fit inside the stream's size; anything left over
		// stays black.
		rows, cols := min(h, c.h), min(w, c.w)*4
		for y := range rows {
			copy(c.frame[y*c.w*4:y*c.w*4+cols], src[y*stride:y*stride+cols])
		}
		c.mu.Unlock()
	case outputAudio:
		// Two channels of 32-bit floats, one buffer each: an AudioBufferList with room for two.
		var list [40]byte
		var block uintptr
		if mac.audioList(sbuf, 0, unsafe.Pointer(&list[0]), uintptr(len(list)), 0, 0, 1, &block) != 0 {
			return
		}
		defer mac.release(block)
		n := binary.LittleEndian.Uint32(list[0:4])
		if n == 0 || n > 2 {
			return
		}
		type buf struct {
			ch, size uint32
			data     uintptr
		}
		bufs := (*[2]buf)(unsafe.Pointer(&list[8]))
		left := unsafe.Slice((*float32)(cptr(bufs[0].data)), bufs[0].size/4)
		right := left
		if n == 2 {
			right = unsafe.Slice((*float32)(cptr(bufs[1].data)), bufs[1].size/4)
		}
		frames := min(len(left), len(right))
		out := make([]byte, frames*4)
		for i := range frames {
			binary.LittleEndian.PutUint16(out[i*4:], uint16(toS16(left[i])))
			binary.LittleEndian.PutUint16(out[i*4+2:], uint16(toS16(right[i])))
		}
		select {
		case c.audio <- out:
		default: // FFmpeg has fallen behind: sound is dropped rather than delayed
		}
	}
}

// cptr is memory a framework handed over by its address. Go's own memory is never passed this way.
func cptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

func toS16(f float32) int16 {
	return int16(math.Max(-32768, math.Min(32767, float64(f)*32767)))
}

func (c *sckCapture) run(ctx context.Context, video, audio io.WriteCloser) error {
	if err := c.start(); err != nil {
		return err
	}
	defer c.close()
	go paceAudio(ctx, c.audio, audio, 48000, 2)
	t := time.NewTicker(time.Second / time.Duration(c.fps))
	defer t.Stop()
	buf := make([]byte, len(c.frame))
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		c.mu.Lock()
		copy(buf, c.frame)
		c.mu.Unlock()
		if _, err := video.Write(buf); err != nil {
			return err
		}
	}
}

func (c *sckCapture) close() {
	c.stopOnce.Do(func() {
		if c.stream != 0 {
			done := make(chan struct{})
			b := objc.NewBlock(func(_ objc.Block, _ objc.ID) { close(done) })
			c.stream.Send(sel("stopCaptureWithCompletionHandler:"), b)
			select {
			case <-done:
			case <-time.After(3 * time.Second):
			}
			b.Release()
			c.stream.Send(sel("release"))
		}
		capturesMu.Lock()
		delete(captures, c.handlerKey)
		capturesMu.Unlock()
		if c.handlerKey != 0 {
			objc.ID(c.handlerKey).Send(sel("release"))
		}
	})
}
