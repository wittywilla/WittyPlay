package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/engine"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// sweepResult is what one game did on one core, in terms that can be compared.
type sweepResult struct {
	Console     string  `json:"console"`
	Core        string  `json:"core"`
	CoreName    string  `json:"coreName,omitempty"`
	Game        string  `json:"game"`
	Started     bool    `json:"started"`
	LoadSeconds float64 `json:"loadSeconds"`
	Target      float64 `json:"targetFps"`
	GameFPS     float64 `json:"gameFps"`     // the median over the last two thirds
	EncodedFPS  float64 `json:"encodedFps"`  // the same, for the encoder
	Distinct    int     `json:"distinct"`    // different pictures among the samples
	Samples     int     `json:"samples"`     // pictures looked at, one a second
	Delivered   int     `json:"delivered"`   // video frames that reached the viewer
	Decoded     int     `json:"decoded"`     // of those, what a real decoder made pictures of
	AudioBytes  int64   `json:"audioBytes"`  // sound that reached the viewer
	Encoder     string  `json:"encoder"`     // what the encoder is
	Renderer    string  `json:"renderer"`    // the graphics card, for a game drawn on one
	Size        string  `json:"size"`        // the picture, once it's running
	EndedEarly  bool    `json:"endedEarly"`  // the game went before the time was up
	Error       string  `json:"error"`       // why it didn't start, or how it ended
	Verdict     string  `json:"verdict"`     // ok, ran-still, slow, failed
	RunnerSaid  string  `json:"runnerSaid"`  // the end of what the runner printed
	SpeedNote   string  `json:"speedNote"`   // what the numbers mean, when they're low
	LoadAverage float64 `json:"loadAverage"` // how busy this machine was
}

// sweep plays one game through the path a real game takes: the session manager, the
// sandboxed runner, the encoder and a WebSocket viewer. It says whether frames reached the
// viewer with time advancing in the game, without needing a browser.
func sweep(args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ExitOnError)
	g := addGameFlags(fs)
	seconds := fs.Int("seconds", 30, "how long to play once it has loaded")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "the FFmpeg program")
	encoders := fs.String("encoders", "h264_nvenc,libx264", "encoders to try, best first")
	out := fs.String("out", "", "add the result to this JSON lines file")
	shots := fs.String("shots", "", "a folder for the last picture from the core and from the decoder")
	loadLimit := fs.Duration("load-limit", 5*time.Minute, "how long it may take to load")
	label := fs.String("label", "", "a name for this run")
	stamp := fs.Bool("stamp", false, "draw numbers into each picture, as the other measurements do")
	_ = fs.Parse(args)
	r := sweepResult{Console: g.console, Core: g.coreID, Game: filepath.Base(g.rom)}
	err := playSweep(g, &r, *seconds, *ffmpeg, *encoders, *shots, *label, *loadLimit, *stamp)
	if err != nil {
		r.Error = err.Error()
	}
	r.Verdict = sweepVerdict(r)
	r.LoadAverage = loadAverage()
	line, _ := json.Marshal(r)
	fmt.Println(string(line))
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(append(line, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func sweepVerdict(r sweepResult) string {
	switch {
	case !r.Started || r.Delivered == 0 || r.GameFPS <= 0 || r.EndedEarly:
		return "failed"
	case r.Distinct < 2:
		return "ran-still" // running, and showing one unchanging picture
	case r.GameFPS < r.Target*0.9:
		return "slow"
	}
	return "ok"
}

func loadAverage() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	first, _, _ := strings.Cut(string(b), " ")
	v, _ := strconv.ParseFloat(first, 64)
	return v
}

// tailLog keeps what the session logs at warning level or above, as that holds what the
// runner printed on its way out.
type tailLog struct{ b strings.Builder }

func (t *tailLog) Write(p []byte) (int, error) {
	if t.b.Len() < 1<<16 {
		t.b.Write(p)
	}
	return len(p), nil
}

func playSweep(g *gameFlags, r *sweepResult, seconds int, ffmpeg, encoders, shots, label string, loadLimit time.Duration, stamp bool) error {
	if err := os.MkdirAll(g.save, 0o755); err != nil {
		return err
	}
	if why := wittyplay.Refusal(g.rom, g.console); why != "" {
		return errors.New(why)
	}
	if g.console == "3ds" { // as the server prepares a 3DS game
		t, err := wittyplay.Inspect3DS(g.rom)
		if err != nil {
			return err
		}
		if t.NewOnly {
			g.options = strings.TrimPrefix(g.options+";"+wittyplay.NewThreeDSOption+"="+wittyplay.NewThreeDSValue, ";")
		}
		if t.Package {
			dir, err := os.MkdirTemp("", "wittyplay-sweep-3ds-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(dir)
			out := filepath.Join(dir, "game.cxi")
			if err := t.CopyProgram(g.rom, out); err != nil {
				return err
			}
			g.rom = out
		}
	}
	names := strings.Split(encoders, ",")
	var hw []string
	for _, n := range names {
		if strings.HasPrefix(n, "h264_") {
			hw = append(hw, n)
		}
	}
	said := &tailLog{}
	log := slog.New(slog.NewTextHandler(said, &slog.HandlerOptions{Level: slog.LevelWarn}))
	mgr := stream.NewManager(stream.Config{Runner: g.runner, FFmpeg: ffmpeg, HWEncoders: hw, GPUEncoders: hw, HaveEncoder: func(n string) bool { return slices.Contains(names, n) }, Log: log})
	load, runnerArgs := g.load()
	gpu := g.coreID != "" && wittyplay.Traits(g.coreID).GPU
	ahead := ipc.RunAheadAuto
	began := time.Now()
	sess, err := mgr.Start(stream.Options{Control: &ipc.Control{RunAhead: &ahead}, UserID: 1, ItemID: 1, Title: filepath.Base(g.rom), Console: g.console, Core: g.core, ROM: g.rom, SystemDir: g.system, SaveDir: g.save,
		GPU: gpu, RunnerArgs: runnerArgs, CoreID: g.coreID, CoreOptions: load.Options, CoreAssets: load.Assets, LoadLimit: loadLimit, NoAutoSave: true, Stamp: stamp, PreferDevices: wittyplay.Pad(g.console).PreferDevices})
	if err != nil {
		r.RunnerSaid = tail(said.b.String(), 3000)
		return err
	}
	defer sess.Stop()
	sess.SetAutoSave(false)
	r.Started, r.LoadSeconds = true, time.Since(began).Seconds()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) { sess.ServeWS(w, q, "h264") }))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(64 << 20)

	dir, err := os.MkdirTemp("", "wittyplay-sweep-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	video := filepath.Join(dir, "video.h264")
	vf, err := os.Create(video)
	if err != nil {
		return err
	}
	defer vf.Close()
	var delivered atomic.Int64
	var audio atomic.Int64
	go func() {
		const keep = 256 << 20 // enough to prove it decodes, and no more
		var written int64
		for {
			t, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if t != websocket.MessageBinary || len(b) < 2 {
				continue
			}
			switch b[0] {
			case 1: // see wsH264
				delivered.Add(1)
				if written < keep {
					n, _ := vf.Write(b[2:])
					written += int64(n)
				}
			case 3: // see wsPCM
				audio.Add(int64(len(b) - 5))
			}
		}
	}()

	// Buttons, as a person getting past a title screen would press them. A game showing a
	// menu has nothing to do until it's asked.
	press := func(id int, down bool) {
		msg := make([]byte, ipc.InputSize)
		if down {
			binary.LittleEndian.PutUint16(msg[1:], 1<<id)
		}
		sess.Input(msg)
	}
	var games, encoded []float64
	seen := map[[32]byte]bool{}
	samples := 0
	buttons := []int{engine.ButtonStart, engine.ButtonA, engine.ButtonStart, engine.ButtonB}
	started := time.Now()
	limit := time.Duration(seconds) * time.Second
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	_ = sess.Stats()
loop:
	for i := 0; ; i++ {
		select {
		case <-sess.Done():
			r.EndedEarly = true
			break loop
		case <-tick.C:
		}
		el := time.Since(started)
		if el >= limit {
			break
		}
		if i%4 == 1 {
			id := buttons[(i/4)%len(buttons)]
			press(id, true)
			time.AfterFunc(150*time.Millisecond, func() { press(id, false) })
		}
		st := sess.Stats()
		r.Size, r.Encoder = fmt.Sprintf("%dx%d", st.Width, st.Height), st.Encoder
		if el > limit/3 { // the first third is for caches and first-launch stalls
			games = append(games, st.GameFPS)
			encoded = append(encoded, st.EncodedFPS)
		}
		if _, raw := sess.RawFrame(); raw != nil {
			samples++
			seen[sha256.Sum256(raw)] = true
		}
	}
	info := sess.Info()
	r.CoreName, r.Target, r.Renderer = info.Core, info.FPS, info.Renderer
	r.GameFPS, r.EncodedFPS = median(games), median(encoded)
	r.Distinct, r.Samples = len(seen), samples
	r.Delivered, r.AudioBytes = int(delivered.Load()), audio.Load()
	if r.EndedEarly {
		time.Sleep(200 * time.Millisecond)
	}
	if shots != "" {
		_ = os.MkdirAll(shots, 0o755)
		if label == "" {
			label = g.coreID
		}
		if hdr, raw := sess.RawFrame(); raw != nil {
			if f, err := os.Create(filepath.Join(shots, label+"-core.png")); err == nil {
				_ = png.Encode(f, toImage(hdr, raw))
				f.Close()
			}
		}
	}
	sess.Stop()
	cancel()
	_ = vf.Close()
	r.Decoded = decodedFrames(ffmpeg, video, shots, label)
	r.RunnerSaid = tail(said.b.String(), 3000)
	if r.EndedEarly {
		return errors.New("the game ended by itself after " + time.Since(started).Round(time.Second).String())
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	v = slices.Clone(v)
	slices.Sort(v)
	return v[len(v)/2]
}

// decodedFrames runs what the viewer received through a real decoder and counts the
// pictures, keeping the last one.
func decodedFrames(ffmpeg, video, shots, label string) int {
	st, err := os.Stat(video)
	if err != nil || st.Size() == 0 {
		return 0
	}
	args := []string{"-hide_banner", "-v", "error", "-y", "-f", "h264", "-i", video, "-map", "0:v", "-f", "framecrc", "-"}
	if shots != "" {
		args = append(args, "-map", "0:v", "-update", "1", filepath.Join(shots, label+"-decoded.png"))
	}
	cmd := exec.Command(ffmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0
	}
	if err := cmd.Start(); err != nil {
		return 0
	}
	b, _ := io.ReadAll(stdout)
	_ = cmd.Wait()
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" && line[0] != '#' {
			n++
		}
	}
	return n
}
