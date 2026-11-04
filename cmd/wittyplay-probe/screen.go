package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// screenWatch looks at what the browser actually puts on the screen, through its debugging
// connection, and counts black pictures shown once a game had been: what a person would see
// as a black flash, whichever part of the way made it. The page's own count only sees frames
// the decoder gave it, not a picture taken away before the next was ready.
type screenWatch struct {
	frames atomic.Int64 // pictures the browser composited
	black  atomic.Int64 // of those, black ones after the game had shown something
	runs   atomic.Int64 // separate black flashes
	lit    atomic.Bool
	inRun  atomic.Bool
	mu     sync.Mutex
	err    string
}

func (w *screenWatch) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := fmt.Sprintf("%d shown, %d black in %d flashes", w.frames.Load(), w.black.Load(), w.runs.Load())
	if w.err != "" {
		s += " (" + w.err + ")"
	}
	return s
}

func (w *screenWatch) fail(err error) {
	w.mu.Lock()
	w.err = err.Error()
	w.mu.Unlock()
}

// watch follows one browser started with --remote-debugging-port=0 and the profile in dir,
// until ctx ends. The page is the first one open at pageURL.
func (w *screenWatch) watch(ctx context.Context, dir, pageURL string) {
	port, err := devToolsPort(ctx, dir)
	if err != nil {
		w.fail(err)
		return
	}
	var target string
	for end := time.Now().Add(20 * time.Second); target == "" && time.Now().Before(end); {
		target = pageTarget(port, pageURL)
		if target == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if target == "" {
		w.fail(fmt.Errorf("the page never appeared to the debugger"))
		return
	}
	c, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		w.fail(err)
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(32 << 20)
	id := 0
	send := func(method string, params any) error {
		id++
		b, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
		return c.Write(ctx, websocket.MessageText, b)
	}
	if err := send("Page.startScreencast", map[string]any{"format": "jpeg", "quality": 40, "maxWidth": 480, "maxHeight": 360, "everyNthFrame": 1}); err != nil {
		w.fail(err)
		return
	}
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Method string `json:"method"`
			Params struct {
				Data      string `json:"data"`
				SessionID int    `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(b, &m) != nil || m.Method != "Page.screencastFrame" {
			continue
		}
		_ = send("Page.screencastFrameAck", map[string]any{"sessionId": m.Params.SessionID})
		raw, err := base64.StdEncoding.DecodeString(m.Params.Data)
		if err != nil {
			continue
		}
		img, err := jpeg.Decode(bytes.NewReader(raw))
		if err != nil {
			continue
		}
		w.note(middleLuma(img) < 12)
	}
}

func (w *screenWatch) note(dark bool) {
	w.frames.Add(1)
	if !dark {
		w.lit.Store(true)
		w.inRun.Store(false)
		return
	}
	if !w.lit.Load() {
		return
	}
	w.black.Add(1)
	if !w.inRun.Swap(true) {
		w.runs.Add(1)
	}
}

// middleLuma is the mean brightness of the middle of a picture, out of 255: where the game
// is, away from the status line in a corner.
func middleLuma(img image.Image) float64 {
	b := img.Bounds()
	x0, x1 := b.Min.X+b.Dx()/4, b.Max.X-b.Dx()/4
	y0, y1 := b.Min.Y+b.Dy()/4, b.Max.Y-b.Dy()/4
	var sum float64
	n := 0
	for y := y0; y < y1; y += 4 {
		for x := x0; x < x1; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(bl>>8)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// devToolsPort reads the port a browser chose for its debugger, which it writes to its profile.
func devToolsPort(ctx context.Context, dir string) (string, error) {
	f := filepath.Join(dir, "DevToolsActivePort")
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); {
		if b, err := os.ReadFile(f); err == nil {
			if port, _, _ := strings.Cut(string(b), "\n"); strings.TrimSpace(port) != "" {
				return strings.TrimSpace(port), nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("the browser never said where its debugger is")
}

func pageTarget(port, pageURL string) string {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/json/list")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var list []struct {
		Type, URL            string
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if json.NewDecoder(resp.Body).Decode(&list) != nil {
		return ""
	}
	base, _, _ := strings.Cut(pageURL, "?")
	for _, t := range list {
		if t.Type == "page" && strings.HasPrefix(t.URL, base) {
			return t.WebSocketDebuggerURL
		}
	}
	return ""
}
