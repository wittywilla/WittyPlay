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
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/wittywilla/wittyplay/emulators/inputmap"
	"github.com/wittywilla/wittyplay/sandbox/docker/inputagent"
)

// Windows: a job object holds the emulator to memory and process limits and ends everything in
// it with the module; it runs at low integrity, which lets it write only where it's been labelled
// to (its own save folder). FFmpeg's ddagrab (Desktop Duplication) captures the screen where its
// window is, WASAPI's loopback captures what the machine plays, and SendInput gives it keys,
// with its window brought to the front first, as Windows sends keys only there.
//
// This has been compiled, not run: see docs/STANDALONE-EMULATORS.md, "What hasn't been run".

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	ntdll    = windows.NewLazySystemDLL("ntdll.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procGetClientRect            = user32.NewProc("GetClientRect")
	procClientToScreen           = user32.NewProc("ClientToScreen")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetForegroundWindow      = user32.NewProc("GetForegroundWindow")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSendInput                = user32.NewProc("SendInput")
	procMapVirtualKey            = user32.NewProc("MapVirtualKeyW")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procPostMessage              = user32.NewProc("PostMessageW")
	procNtSuspendProcess         = ntdll.NewProc("NtSuspendProcess")
	procNtResumeProcess          = ntdll.NewProc("NtResumeProcess")
	procCoInitializeEx           = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance         = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree            = ole32.NewProc("CoTaskMemFree")
	procGlobalMemoryStatusEx     = kernel32.NewProc("GlobalMemoryStatusEx")
)

func unsupported() string { return "" }

var (
	ddagrabOnce sync.Once
	ddagrabOK   bool
)

func platformPermissions() []Permission { return nil }

func platformMissing() []string {
	var out []string
	if ff, err := exec.LookPath("ffmpeg"); err == nil {
		ddagrabOnce.Do(func() {
			cmd := exec.Command(ff, "-hide_banner", "-filters")
			hideConsole(cmd)
			b, _ := cmd.Output()
			ddagrabOK = strings.Contains(string(b), "ddagrab")
		})
		if !ddagrabOK {
			out = append(out, "this FFmpeg has no ddagrab (Desktop Duplication), which captures an emulator's picture: use a build from FFmpeg 6.1 or later made with Direct3D 11, as the server's own is")
		}
	}
	if !interactiveDesktop() {
		out = append(out, "the server is running as a service, which has no desktop for an emulator's window: run it signed in, from the Start menu or at sign-in")
	}
	return out
}

// interactiveDesktop says this process can show windows people could see: not session 0.
func interactiveDesktop() bool {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return false
	}
	return session != 0
}

func sandboxName() string { return "a job object at low integrity" }

func memoryMB() int64 {
	var st struct {
		Length               uint32
		MemoryLoad           uint32
		TotalPhys, AvailPhys uint64
		TotalPageFile        uint64
		AvailPageFile        uint64
		TotalVirtual         uint64
		AvailVirtual         uint64
		AvailExtendedVirtual uint64
	}
	st.Length = uint32(unsafe.Sizeof(st))
	if r, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return 0
	}
	return int64(st.TotalPhys >> 20)
}

func hideConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func platformEncoders() []string { return []string{"x264", "nvenc", "qsv", "amf"} }

// ---------------------------------------------------------------- the sandbox

type box struct {
	job   windows.Handle
	token windows.Token
	once  sync.Once
}

func (b *box) close() {
	b.once.Do(func() {
		if b.job != 0 {
			_ = windows.TerminateJobObject(b.job, 1)
			_ = windows.CloseHandle(b.job)
		}
		if b.token != 0 {
			_ = b.token.Close()
		}
	})
}

// lowLabel marks a folder, and what's made in it, writable at low integrity.
const lowLabel = "S:(ML;OICI;NW;;;LW)"

func labelLow(dir string) error {
	sd, err := windows.SecurityDescriptorFromString(lowLabel)
	if err != nil {
		return err
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl)
}

// lowIntegrityToken is this process's own token at low integrity, as the server makes for a
// module's helper (internal/modules/sandbox_windows.go).
func lowIntegrityToken() (windows.Token, error) {
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT|windows.TOKEN_ASSIGN_PRIMARY, &self); err != nil {
		return 0, err
	}
	defer self.Close()
	var tok windows.Token
	if err := windows.DuplicateTokenEx(self, 0, nil, windows.SecurityImpersonation, windows.TokenPrimary, &tok); err != nil {
		return 0, err
	}
	sid, err := windows.StringToSid("S-1-16-4096") // low mandatory level
	if err != nil {
		tok.Close()
		return 0, err
	}
	label := windows.Tokenmandatorylabel{Label: windows.SIDAndAttributes{Sid: sid, Attributes: windows.SE_GROUP_INTEGRITY}}
	if err := windows.SetTokenInformation(tok, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&label)), label.Size()); err != nil {
		tok.Close()
		return 0, err
	}
	return tok, nil
}

func sandbox(cmd *exec.Cmd, pl plan) (*box, error) {
	for _, d := range []string{pl.SaveDir} {
		if err := labelLow(d); err != nil {
			return nil, fmt.Errorf("letting the emulator write to its own folder: %w", err)
		}
	}
	tok, err := lowIntegrityToken()
	if err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		tok.Close()
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if pl.MemoryMB > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(pl.MemoryMB << 20)
	}
	if pl.Pids > 0 {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		info.BasicLimitInformation.ActiveProcessLimit = uint32(min(pl.Pids, 1<<16))
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		tok.Close()
		return nil, err
	}
	if pl.CPUs > 0 && pl.CPUs < float64(runtime.NumCPU()) {
		// JOBOBJECT_CPU_RATE_CONTROL_INFORMATION: enabled, a hard cap, in hundredths of a percent.
		const cpuRateInfo, rateEnable, rateHardCap = 15, 0x1, 0x4
		rate := struct{ ControlFlags, Value uint32 }{rateEnable | rateHardCap, uint32(pl.CPUs / float64(runtime.NumCPU()) * 10000)}
		_, _ = windows.SetInformationJobObject(job, cpuRateInfo, uintptr(unsafe.Pointer(&rate)), uint32(unsafe.Sizeof(rate)))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok), CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
	return &box{job: job, token: tok}, nil
}

// started puts the emulator in its job before it has run a single instruction: it was created
// suspended, and only then is let go.
func (b *box) started(cmd *exec.Cmd) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(b.job, h); err != nil {
		return err
	}
	if r, _, _ := procNtResumeProcess.Call(uintptr(h)); r != 0 {
		return fmt.Errorf("the emulator wouldn't start (NTSTATUS %#x)", r)
	}
	return nil
}

func systemEnv(pl plan) []string {
	home := filepath.Join(pl.SaveDir, "home")
	env := []string{"USERPROFILE=" + home, "APPDATA=" + filepath.Join(home, "AppData", "Roaming"), "LOCALAPPDATA=" + filepath.Join(home, "AppData", "Local"),
		"TEMP=" + filepath.Join(pl.SaveDir, "tmp"), "TMP=" + filepath.Join(pl.SaveDir, "tmp")}
	for _, k := range []string{"SystemRoot", "SystemDrive", "windir", "PATH", "NUMBER_OF_PROCESSORS", "PROCESSOR_ARCHITECTURE"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	for _, d := range []string{filepath.Join(home, "AppData", "Roaming"), filepath.Join(home, "AppData", "Local")} {
		_ = os.MkdirAll(d, 0o700)
	}
	return env
}

// jobPids are the processes in the emulator's job: an emulator may hand its window to another.
func jobPids(job windows.Handle) []uint32 {
	var buf [8 + 256*8]byte
	if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf)), nil); err != nil {
		return nil
	}
	n := binary.LittleEndian.Uint32(buf[4:8])
	out := make([]uint32, 0, n)
	for i := range min(int(n), 256) {
		out = append(out, uint32(binary.LittleEndian.Uint64(buf[8+i*8:])))
	}
	return out
}

func each(job windows.Handle, fn func(h windows.Handle)) {
	for _, pid := range jobPids(job) {
		h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, pid)
		if err != nil {
			continue
		}
		fn(h)
		windows.CloseHandle(h)
	}
}

func suspend(_ *exec.Cmd, b *box, paused bool) error {
	if b == nil || b.job == 0 {
		return errors.New("it isn't running")
	}
	each(b.job, func(h windows.Handle) {
		if paused {
			procNtSuspendProcess.Call(uintptr(h))
		} else {
			procNtResumeProcess.Call(uintptr(h))
		}
	})
	return nil
}

const wmClose = 0x0010

// terminate asks the emulator's windows to close, as its own close button would.
func terminate(cmd *exec.Cmd, b *box) {
	if cmd.Process == nil {
		return
	}
	if w, ok := largestWindow(map[uint32]bool{uint32(cmd.Process.Pid): true}); ok {
		procPostMessage.Call(uintptr(w.hwnd), wmClose, 0, 0)
	}
}

func kill(cmd *exec.Cmd, b *box) {
	if b != nil && b.job != 0 {
		_ = windows.TerminateJobObject(b.job, 1)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// ---------------------------------------------------------------- its window

type window struct {
	hwnd windows.HWND
	pids map[uint32]bool
	job  windows.Handle
}

type rect struct{ Left, Top, Right, Bottom int32 }
type point struct{ X, Y int32 }

var (
	enumMu     sync.Mutex
	enumWant   map[uint32]bool
	enumBest   windows.HWND
	enumArea   int64
	enumCallbk = syscall.NewCallback(func(hwnd windows.HWND, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadProcessID.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&pid)))
		if !enumWant[pid] {
			return 1
		}
		if v, _, _ := procIsWindowVisible.Call(uintptr(hwnd)); v == 0 {
			return 1
		}
		var r rect
		procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r)))
		w, h := int64(r.Right-r.Left), int64(r.Bottom-r.Top)
		if w >= 160 && h >= 120 && w*h > enumArea {
			enumBest, enumArea = hwnd, w*h
		}
		return 1
	})
)

func largestWindow(pids map[uint32]bool) (window, bool) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumWant, enumBest, enumArea = pids, 0, 0
	procEnumWindows.Call(enumCallbk, 0)
	if enumBest == 0 {
		return window{}, false
	}
	return window{hwnd: enumBest, pids: pids}, true
}

// boxOf is how findWindow learns the job; startGame's box isn't passed down to it.
var (
	boxesMu sync.Mutex
	boxes   = map[int]*box{}
)

func findWindow(ctx context.Context, pid int) (window, error) {
	for {
		pids := map[uint32]bool{uint32(pid): true}
		boxesMu.Lock()
		b := boxes[pid]
		boxesMu.Unlock()
		if b != nil {
			for _, p := range jobPids(b.job) {
				pids[p] = true
			}
		}
		if w, ok := largestWindow(pids); ok {
			if b != nil {
				w.job = b.job
			}
			// Desktop Duplication captures the first screen: the window goes to its top left.
			const swpNoSize, swpNoZOrder = 0x0001, 0x0004
			procSetWindowPos.Call(uintptr(w.hwnd), 0, 0, 0, 0, 0, swpNoSize|swpNoZOrder)
			return w, nil
		}
		select {
		case <-ctx.Done():
			return window{}, errors.New("it opened no window")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// clientArea is the window's drawing area on the screen.
func clientArea(hwnd windows.HWND) (x, y, w, h int) {
	var r rect
	procGetClientRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&r)))
	p := point{}
	procClientToScreen.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y), int(r.Right - r.Left), int(r.Bottom - r.Top)
}

// ---------------------------------------------------------------- keys and the pointer

const (
	inputMouse            = 0
	inputKeyboard         = 1
	keyExtended           = 0x0001
	keyUp                 = 0x0002
	keyScancode           = 0x0008
	mouseMove             = 0x0001
	mouseLeftDown         = 0x0002
	mouseLeftUp           = 0x0004
	mouseAbsolute         = 0x8000
	mouseVirtualDesk      = 0x4000
	mapVKToVSC            = 0
	smXVirtualScreen      = 76
	smYVirtualScreen      = 77
	smCXVirtualScreen     = 78
	smCYVirtualScreen     = 79
	vkMenu                = 0x12
	sizeofInput           = 40 // INPUT on 64-bit Windows
	offsetUnion           = 8
	sizeofKeybdInputUnion = 24
)

// extended keys need the flag that says so, or Windows takes the arrow keys for the keypad's.
var extendedVK = map[int]bool{0x21: true, 0x22: true, 0x23: true, 0x24: true, 0x25: true, 0x26: true, 0x27: true, 0x28: true, 0x2D: true, 0x2E: true, 0x6F: true, 0x0D | 0x100: true}

type winSink struct {
	win  window
	down bool
}

func newSink(_ int, win window, _, _ int) inputagent.Sink { return &winSink{win: win} }

func (s *winSink) front() {
	if fg, _, _ := procGetForegroundWindow.Call(); windows.HWND(fg) == s.win.hwnd {
		return
	}
	// Windows lets a program bring another to the front only just after input; a tap of Alt
	// is input.
	s.send(keyInput(vkMenu, 0, false), keyInput(vkMenu, 0, true))
	procSetForegroundWindow.Call(uintptr(s.win.hwnd))
}

func keyInput(vk, scan int, up bool) [sizeofInput]byte {
	var in [sizeofInput]byte
	binary.LittleEndian.PutUint32(in[0:], inputKeyboard)
	flags := uint32(0)
	if scan != 0 {
		flags |= keyScancode
	}
	if extendedVK[vk] {
		flags |= keyExtended
	}
	if up {
		flags |= keyUp
	}
	binary.LittleEndian.PutUint16(in[offsetUnion:], uint16(vk&0xFF))
	binary.LittleEndian.PutUint16(in[offsetUnion+2:], uint16(scan))
	binary.LittleEndian.PutUint32(in[offsetUnion+4:], flags)
	return in
}

func (s *winSink) send(ins ...[sizeofInput]byte) error {
	if len(ins) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(ins)*sizeofInput)
	for _, in := range ins {
		buf = append(buf, in[:]...)
	}
	n, _, err := procSendInput.Call(uintptr(len(ins)), uintptr(unsafe.Pointer(&buf[0])), sizeofInput)
	if int(n) != len(ins) {
		return fmt.Errorf("Windows took %d of %d key events: %v", n, len(ins), err)
	}
	return nil
}

func (s *winSink) Key(name string, down bool) error {
	vk, ok := inputmap.HostKey("windows", name)
	if !ok {
		return fmt.Errorf("no Windows key for %q", name)
	}
	if down {
		s.front()
	}
	scan, _, _ := procMapVirtualKey.Call(uintptr(vk&0xFF), mapVKToVSC)
	return s.send(keyInput(vk, int(scan), !down))
}

func (s *winSink) Pointer(fx, fy float64, pressed bool) error {
	x, y, w, h := clientArea(s.win.hwnd)
	vx, _, _ := procGetSystemMetrics.Call(smXVirtualScreen)
	vy, _, _ := procGetSystemMetrics.Call(smYVirtualScreen)
	vw, _, _ := procGetSystemMetrics.Call(smCXVirtualScreen)
	vh, _, _ := procGetSystemMetrics.Call(smCYVirtualScreen)
	if vw == 0 || vh == 0 {
		return errors.New("no screen")
	}
	px := float64(x) + math.Max(0, math.Min(1, fx))*float64(w) - float64(int32(vx))
	py := float64(y) + math.Max(0, math.Min(1, fy))*float64(h) - float64(int32(vy))
	flags := uint32(mouseMove | mouseAbsolute | mouseVirtualDesk)
	switch {
	case pressed && !s.down:
		s.front()
		flags |= mouseLeftDown
	case !pressed && s.down:
		flags |= mouseLeftUp
	}
	s.down = pressed
	var in [sizeofInput]byte
	binary.LittleEndian.PutUint32(in[0:], inputMouse)
	binary.LittleEndian.PutUint32(in[offsetUnion:], uint32(int32(px*65535/float64(vw))))
	binary.LittleEndian.PutUint32(in[offsetUnion+4:], uint32(int32(py*65535/float64(vh))))
	binary.LittleEndian.PutUint32(in[offsetUnion+12:], flags)
	return s.send(in)
}

// ---------------------------------------------------------------- the picture and the sound

type winCapture struct {
	win         window
	w, h, fps   int
	rate, chans int
	bits        int
	float       bool
	stop        chan struct{}
	once        sync.Once
}

func newCapture(win window, w, h, fps int) (capture, error) {
	c := &winCapture{win: win, w: w, h: h, fps: fps, rate: 48000, chans: 2, stop: make(chan struct{})}
	return c, nil
}

func even(n int) int { return n &^ 1 }

func (c *winCapture) inputs() []string {
	x, y, w, h := clientArea(c.win.hwnd)
	x, y = max(0, x), max(0, y)
	w, h = even(max(w, 2)), even(max(h, 2))
	src := fmt.Sprintf("ddagrab=output_idx=0:draw_mouse=0:framerate=%d:video_size=%dx%d:offset_x=%d:offset_y=%d,hwdownload,format=bgra,"+
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2", c.fps, w, h, x, y, c.w, c.h, c.w, c.h)
	return []string{"-f", "lavfi", "-i", src, "-f", "s16le", "-ar", "48000", "-ac", "2", "-i", "pipe:3"}
}

func (c *winCapture) run(ctx context.Context, _, audio io.WriteCloser) error {
	in := make(chan []byte, 64)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	go paceAudio(ctx, in, audio, 48000, 2)
	err := loopback(ctx, in)
	if err != nil {
		// No sound: the picture still goes, with silence beside it.
		<-ctx.Done()
	}
	return err
}

func (c *winCapture) close() { c.once.Do(func() { close(c.stop) }) }

// GUIDs for WASAPI.
var (
	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

// cptr is memory Windows handed over by its address. Go's own memory is never passed this way.
func cptr(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// com calls method i of a COM object's table.
func com(obj uintptr, i int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(cptr(obj))
	fn := *(*uintptr)(cptr(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func release(obj uintptr) {
	if obj != 0 {
		com(obj, 2)
	}
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// loopback captures what the machine plays, through WASAPI's loopback on the default output,
// as 48 kHz stereo 16-bit samples. It's the whole machine's sound: a server that plays nothing
// else is what this is for.
func loopback(ctx context.Context, out chan<- []byte) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	const coinitMultithreaded = 0
	procCoInitializeEx.Call(0, coinitMultithreaded)
	const clsctxAll = 0x17
	var enum, dev, client, capt uintptr
	defer func() { release(capt); release(client); release(dev); release(enum) }()
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll, uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum))); failed(hr) {
		return fmt.Errorf("no audio devices (%#x)", hr)
	}
	if hr := com(enum, 4, 0 /* eRender */, 0 /* eConsole */, uintptr(unsafe.Pointer(&dev))); failed(hr) {
		return fmt.Errorf("no sound output (%#x)", hr)
	}
	if hr := com(dev, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client))); failed(hr) {
		return fmt.Errorf("the sound output wouldn't open (%#x)", hr)
	}
	var wfx uintptr
	if hr := com(client, 8, uintptr(unsafe.Pointer(&wfx))); failed(hr) || wfx == 0 {
		return fmt.Errorf("the sound output has no format (%#x)", hr)
	}
	defer procCoTaskMemFree.Call(wfx)
	format := unsafe.Slice((*byte)(cptr(wfx)), 40) // WAVEFORMATEXTENSIBLE
	tag := binary.LittleEndian.Uint16(format[0:])
	chans := int(binary.LittleEndian.Uint16(format[2:]))
	rate := int(binary.LittleEndian.Uint32(format[4:]))
	bits := int(binary.LittleEndian.Uint16(format[14:]))
	isFloat := tag == 3 || (tag == 0xFFFE && bits == 32 && format[24] == 3) // IEEE float, or extensible with the float subformat
	const sharedMode, loopbackFlag = 0, 0x00020000
	if hr := com(client, 3, sharedMode, loopbackFlag, 2_000_000 /* 200 ms */, 0, wfx, 0); failed(hr) {
		return fmt.Errorf("the sound output wouldn't loop back (%#x)", hr)
	}
	if hr := com(client, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capt))); failed(hr) {
		return fmt.Errorf("no capture from the sound output (%#x)", hr)
	}
	if hr := com(client, 10); failed(hr) {
		return fmt.Errorf("the sound capture wouldn't start (%#x)", hr)
	}
	defer com(client, 11)
	resample := newResampler(rate, 48000)
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		for {
			var next uint32
			if hr := com(capt, 5, uintptr(unsafe.Pointer(&next))); failed(hr) || next == 0 {
				break
			}
			var data uintptr
			var frames, flags uint32
			if hr := com(capt, 3, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0); failed(hr) {
				break
			}
			const silent = 0x2
			pcm := make([]float64, 0, frames*2)
			for i := range int(frames) {
				for ch := range 2 {
					if flags&silent != 0 || data == 0 || chans == 0 {
						pcm = append(pcm, 0)
						continue
					}
					c := min(ch, chans-1)
					at := cptr(data + uintptr((i*chans+c)*bits/8))
					switch {
					case isFloat && bits == 32:
						pcm = append(pcm, float64(*(*float32)(at)))
					case bits == 16:
						pcm = append(pcm, float64(*(*int16)(at))/32768)
					case bits == 32:
						pcm = append(pcm, float64(*(*int32)(at))/2147483648)
					default:
						pcm = append(pcm, 0)
					}
				}
			}
			com(capt, 4, uintptr(frames))
			b := resample.s16(pcm)
			select {
			case out <- b:
			default:
			}
		}
	}
}

// resampler turns stereo samples at one rate into 48 kHz signed 16-bit, by linear steps: a
// machine's sound output is nearly always 48 kHz already, or 44.1.
type resampler struct {
	step  float64
	pos   float64
	lastL float64
	lastR float64
}

func newResampler(from, to int) *resampler {
	if from <= 0 {
		from = to
	}
	return &resampler{step: float64(from) / float64(to)}
}

func (r *resampler) s16(pcm []float64) []byte {
	n := len(pcm) / 2
	var out []byte
	put := func(v float64) {
		out = binary.LittleEndian.AppendUint16(out, uint16(int16(math.Max(-32768, math.Min(32767, v*32767)))))
	}
	for ; r.pos < float64(n); r.pos += r.step {
		i := int(r.pos)
		f := r.pos - float64(i)
		l0, r0 := r.lastL, r.lastR
		if i > 0 {
			l0, r0 = pcm[(i-1)*2], pcm[(i-1)*2+1]
		}
		l1, r1 := pcm[i*2], pcm[i*2+1]
		put(l0 + (l1-l0)*f)
		put(r0 + (r1-r0)*f)
	}
	r.pos -= float64(n)
	if n > 0 {
		r.lastL, r.lastR = pcm[(n-1)*2], pcm[(n-1)*2+1]
	}
	return out
}
