package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

//go:embed page/index.html page/probe.js
var page embed.FS

// rigFlags say how to set a game up in front of a real browser. Every kind of test here
// starts with one of these.
type rigFlags struct {
	game                                           *gameFlags
	seconds, every, udpPort, reconnectEvery        int
	browser, browserArgs, listen, host, ffmpeg     string
	encoders, kind, label, state, profile, latency string
	impair, shots, swap                            string
	loopback, noAutoAhead, watchScreen             bool
}

func addRigFlags(fs *flag.FlagSet) *rigFlags {
	f := &rigFlags{game: addGameFlags(fs)}
	fs.IntVar(&f.seconds, "seconds", 30, "how long to measure")
	fs.StringVar(&f.browser, "browser", "", "the browser to start; empty to print the address and wait")
	fs.StringVar(&f.browserArgs, "browser-args", "", "more arguments for it, separated by spaces")
	fs.StringVar(&f.listen, "listen", "127.0.0.1:0", "where to serve the test page")
	fs.StringVar(&f.host, "url-host", "", "the name the browser should use for this machine, when it's another machine")
	fs.StringVar(&f.ffmpeg, "ffmpeg", "ffmpeg", "the FFmpeg program")
	fs.StringVar(&f.encoders, "encoders", "h264_nvenc,libx264", "encoders to try, best first")
	fs.StringVar(&f.kind, "kind", "webrtc", "webrtc, ws-h264 or ws-jpeg")
	fs.StringVar(&f.label, "label", "", "a name for this run, for the results file")
	fs.StringVar(&f.state, "state", "", "a save state to start from")
	fs.BoolVar(&f.loopback, "loopback", false, "keep WebRTC to the loopback address, as on Windows with no firewall rule")
	fs.StringVar(&f.profile, "profile", "", "the browser's profile folder; a new temporary one if empty")
	fs.StringVar(&f.latency, "latency", "", "auto, fastest, balanced or steady; empty for the player's default")
	fs.IntVar(&f.udpPort, "udp-port", 0, "the UDP port for WebRTC; with -impair, one is chosen if this isn't set")
	fs.StringVar(&f.impair, "impair", "", "a network to go through: delay=30,jitter=5,loss=2,kbps=3000,queue=100, in ms, percent and kilobits")
	fs.BoolVar(&f.noAutoAhead, "no-auto-run-ahead", false, "leave automatic run-ahead off")
	fs.StringVar(&f.shots, "shots", "", "a folder for pictures of what the browser shows and what the core made, side by side")
	fs.BoolVar(&f.watchScreen, "watch-screen", false, "count black pictures the browser puts on the screen, through its debugger")
	fs.StringVar(&f.swap, "swap", "", "how a new connection's picture replaces the old: empty for the player's way, 0.3.4 for the way it was")
	fs.IntVar(&f.reconnectEvery, "reconnect-every", 0, "connect again this many seconds apart, to test the change between connections")
	return f
}

// rig is a game and a page for a browser to measure it from.
type rig struct {
	f       *rigFlags
	mgr     *stream.Manager
	sess    *stream.Session
	sessMu  sync.Mutex
	relay   *relay
	addr    string
	results chan []byte
	partial chan []byte

	beatMu   sync.Mutex
	beat     beatInfo
	beatAt   time.Time
	cancel   context.CancelFunc
	cmd      *exec.Cmd
	cleanups []func()
	screen   screenWatch
}

type beatInfo struct {
	Frames     int            `json:"frames"`
	Seq        int            `json:"seq"`
	Reconnects int            `json:"reconnects"`
	Recoveries []float64      `json:"recoveries"`
	DropWhy    map[string]int `json:"dropWhy"`
	Heap       int64          `json:"heap"`
	Hidden     bool           `json:"hidden"`
}

func (f *rigFlags) start(extraQuery url.Values) (*rig, error) {
	g := f.game
	if err := os.MkdirAll(g.save, 0o755); err != nil {
		return nil, err
	}
	if g.core == ipc.TestCore && g.rom == "" {
		// The test core draws the same from any file.
		g.rom = filepath.Join(g.save, "test.rom")
		if err := os.WriteFile(g.rom, []byte("WITTYPROBE"), 0o644); err != nil {
			return nil, err
		}
	}
	if f.state != "" {
		b, err := os.ReadFile(f.state)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(g.save, "auto.state"), b, 0o644); err != nil {
			return nil, err
		}
	}
	names := strings.Split(f.encoders, ",")
	have := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	var hw []string
	for _, n := range names {
		if strings.HasPrefix(n, "h264_") {
			hw = append(hw, n)
		}
	}
	r := &rig{f: f, results: make(chan []byte, 1), partial: make(chan []byte, 16)}
	bindHost := "127.0.0.1"
	var im impairment
	if f.impair != "" || f.udpPort != 0 {
		var err error
		if im, err = parseImpairment(f.impair); err != nil {
			return nil, err
		}
		if f.udpPort == 0 {
			l, err := net.ListenPacket("udp4", net.JoinHostPort(bindHost, "0"))
			if err != nil {
				return nil, err
			}
			f.udpPort = l.LocalAddr().(*net.UDPAddr).Port
			l.Close()
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r.mgr = stream.NewManager(stream.Config{Runner: g.runner, FFmpeg: f.ffmpeg, HWEncoders: hw, GPUEncoders: hw, HaveEncoder: have, Log: log, LoopbackOnly: f.loopback, UDPPort: f.udpPort})
	if f.udpPort != 0 && f.impair != "" {
		rl, err := startRelay(bindHost, f.udpPort, im)
		if err != nil {
			return nil, err
		}
		r.relay = rl
	}
	if err := r.startGame(); err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := page.ReadFile("page/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/probe.js", func(w http.ResponseWriter, _ *http.Request) {
		b, _ := page.ReadFile("page/probe.js")
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/answer", func(w http.ResponseWriter, q *http.Request) {
		offer, _ := io.ReadAll(io.LimitReader(q.Body, stream.MaxOfferSize+1))
		sdp, err := r.session().Answer(q.Context(), string(offer), "")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if r.relay != nil {
			sdp = r.relay.answer(sdp, f.udpPort)
		}
		_, _ = io.WriteString(w, sdp)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, q *http.Request) { r.session().ServeWS(w, q, q.URL.Query().Get("video")) })
	mux.HandleFunc("/results", func(_ http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(q.Body, 1<<20))
		select {
		case r.results <- b:
		default:
		}
	})
	mux.HandleFunc("/shot", func(_ http.ResponseWriter, q *http.Request) {
		var s struct {
			PNG string `json:"png"`
			Seq int    `json:"seq"`
		}
		if json.NewDecoder(io.LimitReader(q.Body, 64<<20)).Decode(&s) != nil || f.shots == "" {
			return
		}
		_, data, ok := strings.Cut(s.PNG, ",")
		if !ok {
			return
		}
		if b, err := base64.StdEncoding.DecodeString(data); err == nil {
			_ = os.MkdirAll(f.shots, 0o755)
			_ = os.WriteFile(filepath.Join(f.shots, f.label+"-browser.png"), b, 0o644)
			// And what left the core at about the same moment, before any encoder.
			if hdr, raw := r.session().RawFrame(); raw != nil {
				if out, err := os.Create(filepath.Join(f.shots, f.label+"-core.png")); err == nil {
					_ = png.Encode(out, toImage(hdr, raw))
					out.Close()
				}
			}
		}
	})
	mux.HandleFunc("/partial", func(_ http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(q.Body, 1<<20))
		select {
		case r.partial <- b:
		default:
		}
	})
	mux.HandleFunc("/beat", func(_ http.ResponseWriter, q *http.Request) {
		var b beatInfo
		if json.NewDecoder(io.LimitReader(q.Body, 1<<16)).Decode(&b) == nil {
			r.beatMu.Lock()
			if b.Frames != r.beat.Frames {
				r.beatAt = time.Now()
			}
			r.beat = b
			r.beatMu.Unlock()
		}
	})
	ln, err := net.Listen("tcp", f.listen)
	if err != nil {
		return nil, err
	}
	go func() { _ = http.Serve(ln, mux) }()
	hostName := f.host
	if hostName == "" {
		hostName = "127.0.0.1"
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	q := url.Values{"seconds": {strconv.Itoa(f.seconds)}, "kind": {f.kind}, "label": {f.label}}
	if f.latency != "" {
		q.Set("latency", f.latency)
	}
	if f.swap != "" {
		q.Set("swap", f.swap)
	}
	if f.reconnectEvery > 0 {
		q.Set("reconnectEvery", strconv.Itoa(f.reconnectEvery))
	}
	for k, v := range extraQuery {
		q[k] = v
	}
	r.addr = "http://" + net.JoinHostPort(hostName, port) + "/?" + q.Encode()
	fmt.Println("probe page:", r.addr)
	if f.browser != "" {
		if err := r.startBrowser(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// control is what the settings would say, with nobody having changed them.
func (f *rigFlags) control() *ipc.Control {
	if f.noAutoAhead {
		return nil
	}
	ahead := ipc.RunAheadAuto
	return &ipc.Control{RunAhead: &ahead}
}

// startGame starts the game, or starts it again after it has gone.
func (r *rig) startGame() error {
	g := r.f.game
	load, runnerArgs := g.load()
	gpu := g.coreID != "" && wittyplay.Traits(g.coreID).GPU
	resume := r.f.state != ""
	if _, err := os.Stat(filepath.Join(g.save, "auto.state")); err == nil {
		resume = true
	}
	sess, err := r.mgr.Start(stream.Options{Control: r.f.control(), UserID: 1, ItemID: 1, Title: filepath.Base(g.rom), Console: g.console, Core: g.core, ROM: g.rom, SystemDir: g.system, SaveDir: g.save,
		Resume: resume, GPU: gpu, RunnerArgs: runnerArgs, CoreID: g.coreID, CoreOptions: load.Options, CoreAssets: load.Assets, LoadLimit: 5 * time.Minute, Stamp: true, NoAutoSave: true})
	if err != nil {
		return err
	}
	sess.SetAutoSave(false)
	r.sessMu.Lock()
	r.sess = sess
	r.sessMu.Unlock()
	return nil
}

func (r *rig) session() *stream.Session {
	r.sessMu.Lock()
	defer r.sessMu.Unlock()
	return r.sess
}

func (r *rig) startBrowser() error {
	f := r.f
	dir := f.profile
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "wittyplay-probe-"); err != nil {
			return err
		}
		r.cleanups = append(r.cleanups, func() {
			time.Sleep(500 * time.Millisecond)
			_ = os.RemoveAll(dir)
		})
	}
	a := []string{"--headless=new", "--user-data-dir=" + windowsPath(f.browser, dir), "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-background-networking", "--disable-sync", "--disable-features=Translate,OptimizationHints,MediaRouter,DialMediaRouteProvider",
		"--autoplay-policy=no-user-gesture-required", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--window-size=1280,960", "--enable-precise-memory-info"}
	if f.loopback {
		a = append(a, "--allow-loopback-in-peer-connection")
	}
	if f.watchScreen {
		a = append(a, "--remote-debugging-port=0")
		_ = os.Remove(filepath.Join(dir, "DevToolsActivePort"))
	}
	if f.browserArgs != "" {
		a = append(a, strings.Fields(f.browserArgs)...)
	}
	a = append(a, r.addr)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, f.browser, a...)
	if err := cmd.Start(); err != nil {
		cancel()
		return err
	}
	r.cancel, r.cmd = cancel, cmd
	if f.watchScreen {
		go r.screen.watch(ctx, dir, r.addr)
	}
	return nil
}

func (r *rig) stopBrowser() {
	if r.cancel != nil {
		r.cancel()
		_ = r.cmd.Wait()
		r.cancel, r.cmd = nil, nil
	}
}

func (r *rig) close() {
	r.stopBrowser()
	if s := r.session(); s != nil {
		s.Stop()
	}
	for _, c := range r.cleanups {
		c()
	}
}

// beatNow says what the page last said, and how long ago a frame was last new.
func (r *rig) beatNow() (beatInfo, time.Duration) {
	r.beatMu.Lock()
	defer r.beatMu.Unlock()
	return r.beat, time.Since(r.beatAt)
}

// windowsPath gives a Windows browser started from WSL a folder it can read.
func windowsPath(browser, dir string) string {
	if runtime.GOOS == "linux" && strings.HasSuffix(strings.ToLower(browser), ".exe") {
		if out, err := exec.Command("wslpath", "-w", dir).Output(); err == nil {
			return strings.TrimSpace(string(out))
		}
	}
	return dir
}
