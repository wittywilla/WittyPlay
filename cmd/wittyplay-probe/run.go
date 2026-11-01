package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

//go:embed page/index.html page/probe.js
var page embed.FS

// run plays a game through the whole real path to a real browser, which measures what it
// sees and posts the numbers back.
func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	g := addGameFlags(fs)
	seconds := fs.Int("seconds", 30, "how long to measure")
	warm := fs.Int("warm", 0, "seconds to let the game run before measuring, past its logos")
	browser := fs.String("browser", "", "the browser to start; empty to print the address and wait")
	browserArgs := fs.String("browser-args", "", "more arguments for it, separated by spaces")
	listen := fs.String("listen", "127.0.0.1:0", "where to serve the test page")
	host := fs.String("url-host", "", "the name the browser should use for this machine, when it's another machine")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "the FFmpeg program")
	encoders := fs.String("encoders", "h264_nvenc,libx264", "encoders to try, best first")
	kind := fs.String("kind", "webrtc", "webrtc, ws-h264 or ws-jpeg")
	label := fs.String("label", "", "a name for this run, for the results file")
	out := fs.String("out", "", "add the results to this JSON lines file")
	state := fs.String("state", "", "a save state to start from")
	loopback := fs.Bool("loopback", false, "keep WebRTC to the loopback address, as on Windows with no firewall rule")
	profile := fs.String("profile", "", "the browser's profile folder; a new temporary one if empty")
	tune := addTuneFlags(fs)
	_ = fs.Parse(args)

	if err := os.MkdirAll(g.save, 0o755); err != nil {
		return err
	}
	if g.core == ipc.TestCore && g.rom == "" {
		// The test core draws the same from any file. A real game's size of work is what -pattern is for.
		g.rom = filepath.Join(g.save, "test.rom")
		if err := os.WriteFile(g.rom, []byte("WITTYPROBE"), 0o644); err != nil {
			return err
		}
	}
	if *state != "" {
		b, err := os.ReadFile(*state)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(g.save, "auto.state"), b, 0o644); err != nil {
			return err
		}
	}
	names := strings.Split(*encoders, ",")
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
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mgr := stream.NewManager(stream.Config{Runner: g.runner, FFmpeg: *ffmpeg, HWEncoders: hw, GPUEncoders: hw, HaveEncoder: have, Log: log, LoopbackOnly: *loopback, Tuning: tune.tuning()})
	load, runnerArgs := g.load()
	gpu := g.coreID != "" && wittyplay.Traits(g.coreID).GPU
	sess, err := mgr.Start(stream.Options{UserID: 1, ItemID: 1, Title: filepath.Base(g.rom), Console: g.console, Core: g.core, ROM: g.rom, SystemDir: g.system, SaveDir: g.save,
		Resume: *state != "", GPU: gpu, RunnerArgs: runnerArgs, CoreID: g.coreID, CoreOptions: load.Options, CoreAssets: load.Assets, LoadLimit: 5 * time.Minute, Stamp: true, NoAutoSave: true})
	if err != nil {
		return err
	}
	defer sess.Stop()
	sess.SetAutoSave(false)

	results := make(chan []byte, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := page.ReadFile("page/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/probe.js", func(w http.ResponseWriter, r *http.Request) {
		b, _ := page.ReadFile("page/probe.js")
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/answer", func(w http.ResponseWriter, r *http.Request) {
		offer, _ := io.ReadAll(io.LimitReader(r.Body, stream.MaxOfferSize+1))
		sdp, err := sess.Answer(r.Context(), string(offer), "")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = io.WriteString(w, sdp)
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { sess.ServeWS(w, r, r.URL.Query().Get("video")) })
	mux.HandleFunc("/results", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		select {
		case results <- b:
		default:
		}
	})
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	go func() { _ = http.Serve(ln, mux) }()
	hostName := *host
	if hostName == "" {
		hostName = "127.0.0.1"
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	q := url.Values{"seconds": {strconv.Itoa(*seconds)}, "kind": {*kind}, "label": {*label}}
	addr := "http://" + net.JoinHostPort(hostName, port) + "/?" + q.Encode()
	fmt.Println("probe page:", addr)
	if *warm > 0 {
		// The game runs only with someone watching, so a warm-up with a state needs a viewer
		// too: the page's own three seconds are enough for anything else.
		time.Sleep(time.Duration(*warm) * time.Second)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *browser != "" {
		dir := *profile
		if dir == "" {
			dir, err = os.MkdirTemp("", "wittyplay-probe-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
		}
		a := []string{"--headless=new", "--user-data-dir=" + windowsPath(*browser, dir), "--no-first-run", "--no-default-browser-check", "--disable-extensions", "--disable-background-networking", "--disable-sync", "--disable-features=Translate,OptimizationHints,MediaRouter,DialMediaRouteProvider",
			"--autoplay-policy=no-user-gesture-required", "--disable-background-timer-throttling", "--disable-renderer-backgrounding", "--disable-backgrounding-occluded-windows", "--window-size=1280,960"}
		if *loopback {
			a = append(a, "--allow-loopback-in-peer-connection")
		}
		if *browserArgs != "" {
			a = append(a, strings.Fields(*browserArgs)...)
		}
		a = append(a, addr)
		cmd := exec.CommandContext(ctx, *browser, a...)
		if err := cmd.Start(); err != nil {
			return err
		}
		defer func() { cancel(); _ = cmd.Wait() }()
	}
	limit := time.Duration(*seconds+90) * time.Second
	select {
	case b := <-results:
		return report(b, *label, *out, sess)
	case <-time.After(limit):
		return fmt.Errorf("no results from the page after %s", limit)
	case <-sess.Done():
		return fmt.Errorf("the game ended")
	}
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

type summary struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	N   int     `json:"n"`
}

type pageResults struct {
	Error   string             `json:"error"`
	Label   string             `json:"label"`
	Kind    string             `json:"kind"`
	UA      string             `json:"ua"`
	Frames  map[string]summary `json:"frames"`
	Presses map[string]summary `json:"presses"`
	Glass   summary            `json:"glass"`
	Photon  summary            `json:"photon"`
	Made    summary            `json:"madeToHanded"`
	Gap     summary            `json:"interval"`
	Pixels  map[string]int     `json:"pixels"`
	Mis     map[string]int     `json:"misfiled"`
	Stats   map[string]any     `json:"stats"`
}

func report(b []byte, label, out string, sess *stream.Session) error {
	var r pageResults
	if err := json.Unmarshal(b, &r); err != nil {
		return fmt.Errorf("the page said something unreadable: %s", b)
	}
	if r.Error != "" {
		return fmt.Errorf("the page failed: %s", r.Error)
	}
	st := sess.Stats()
	fmt.Printf("%s: %s, %s, %dx%d, game %.0f fps, encoder %.0f fps\n", label, sess.Info().Core, st.Encoder, st.Width, st.Height, st.GameFPS, st.EncodedFPS)
	row := func(name string, s summary) {
		if s.N > 0 {
			fmt.Printf("  %-18s %7.1f  %7.1f   (%d)\n", name, s.P50, s.P95, s.N)
		}
	}
	fmt.Printf("  %-18s %7s  %7s\n", "stage, ms", "median", "95%")
	for _, k := range []string{"core", "toServer", "queue", "encode", "send", "network", "decode", "display", "total"} {
		row(k, r.Frames[k])
	}
	for _, k := range []string{"uplink", "toRunner", "waitCore", "photon"} {
		row("press "+k, r.Presses[k])
	}
	row("glass (pixels)", r.Glass)
	row("photon (pixels)", r.Photon)
	row("frame gap", r.Gap)
	keys := make([]string, 0, len(r.Stats))
	for k := range r.Stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, r.Stats[k]))
	}
	fmt.Println("  browser:", strings.Join(parts, " "))
	fmt.Println("  pixels:", r.Pixels, "records against pixels:", r.Mis)
	if out != "" {
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["label"], m["game"], m["encoder"], m["size"], m["gameFps"], m["at"] = label, sess.Info().Core, st.Encoder, fmt.Sprintf("%dx%d", st.Width, st.Height), st.GameFPS, time.Now().Format(time.RFC3339)
		line, _ := json.Marshal(m)
		_, err = f.Write(append(line, '\n'))
		return err
	}
	return nil
}
