package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"time"
)

// soak plays one game for a long time and writes down everything that would give away a
// leak or a slow decline: frame rates, each stage's delay, memory and handles of everything
// involved, and how much sound has been stretched to keep it in time. At the end it says
// whether the second half looks any worse than the first.
func soak(args []string) error {
	fs := flag.NewFlagSet("soak", flag.ExitOnError)
	f := addRigFlags(fs)
	minutes := fs.Float64("minutes", 65, "how long to run")
	out := fs.String("out", "soak.csv", "the file to write a line a minute to")
	_ = fs.Parse(args)
	r, err := f.start(url.Values{"every": {"60"}})
	if err != nil {
		return err
	}
	defer r.close()
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer file.Close()
	w := csv.NewWriter(file)
	head := []string{"minute", "gameFps", "encodedFps", "glassP50", "glassP95", "photonP50", "photonP95", "framesRead", "skipped", "dropped", "lost", "audioBufferMs", "audioConcealed", "audioFaster", "audioSlower", "serverHeapMB", "goroutines", "runnerMB", "runnerHandles", "encoderMB", "encoderHandles", "browserHeapMB", "kbps", "encoderRestarts", "drops", "pageBlack", "pageBlackRuns", "screenFrames", "screenBlack", "screenFlashes", "dropWhy"}
	_ = w.Write(head)
	start := time.Now()
	deadline := time.After(time.Duration(*minutes * float64(time.Minute)))
	minute := 0
	var first, last []soakLine
	var prevBytes float64
	for {
		select {
		case <-deadline:
			w.Flush()
			return judgeSoak(first, last, minute)
		case <-r.session().Done():
			w.Flush()
			return fmt.Errorf("the game ended %s in", time.Since(start).Round(time.Second))
		case b := <-r.results:
			w.Flush()
			return fmt.Errorf("the page stopped: %s", b)
		case b := <-r.partial:
			var p pageResults
			if json.Unmarshal(b, &p) != nil {
				continue
			}
			minute++
			line := r.soakSample(minute, &p, &prevBytes)
			_ = w.Write(line.cells)
			w.Flush()
			fmt.Printf("%3d min: game %.0f fps, glass %.1f/%.1f ms, press %.1f/%.1f ms, runner %.0f MB, server %.0f MB, encoder %.0f MB, page %.0f MB, %d goroutines\n", minute, line.gameFps, line.glass, p.Glass.P95, p.Photon.P50, p.Photon.P95, line.runnerMB, line.serverMB, line.encoderMB, line.browserMB, line.goroutines)
			fmt.Printf("         drops %d %v, encoder starts %d, black decoded %d in %d runs, screen %s\n", len(p.Recoveries), p.DropWhy, r.session().Processes().VideoStarts, p.Pixels["dark"], p.Pixels["darkRuns"], r.screen.String())
			if len(first) < 10 {
				first = append(first, line)
			}
			last = append(last, line)
			if len(last) > 10 {
				last = last[1:]
			}
		}
	}
}

type soakLine struct {
	cells                                         []string
	gameFps, glass, runnerMB, serverMB, encoderMB float64
	browserMB                                     float64
	goroutines                                    int
	handles                                       int
}

func (r *rig) soakSample(minute int, p *pageResults, prevBytes *float64) soakLine {
	sess := r.session()
	st := sess.Stats()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	pids := sess.Processes()
	runnerMB, runnerH := procInfo(pids.Runner)
	encoderMB, encoderH := procInfo(pids.Video)
	num := func(k string) float64 {
		v, _ := p.Stats[k].(float64)
		return v
	}
	kbps := (num("kbps") - *prevBytes) * 8 / 1000 / 60
	*prevBytes = num("kbps")
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
	cells := []string{strconv.Itoa(minute), f(st.GameFPS), f(st.EncodedFPS), f(p.Glass.P50), f(p.Glass.P95), f(p.Photon.P50), f(p.Photon.P95), strconv.Itoa(p.Pixels["read"]), strconv.Itoa(p.Pixels["skipped"]),
		f(num("dropped")), f(num("lost")), f(num("audioBufferMs")), f(num("audioConcealed")), f(num("audioFaster")), f(num("audioSlower")), f(float64(mem.HeapAlloc) / (1 << 20)), strconv.Itoa(runtime.NumGoroutine()),
		f(runnerMB), strconv.Itoa(runnerH), f(encoderMB), strconv.Itoa(encoderH), f(float64(p.Heap) / (1 << 20)), f(kbps), strconv.Itoa(pids.VideoStarts),
		strconv.Itoa(len(p.Recoveries)), strconv.Itoa(p.Pixels["dark"]), strconv.Itoa(p.Pixels["darkRuns"]), strconv.FormatInt(r.screen.frames.Load(), 10), strconv.FormatInt(r.screen.black.Load(), 10), strconv.FormatInt(r.screen.runs.Load(), 10), fmt.Sprint(p.DropWhy)}
	return soakLine{cells: cells, gameFps: st.GameFPS, glass: p.Glass.P50, runnerMB: runnerMB, serverMB: float64(mem.HeapAlloc) / (1 << 20), encoderMB: encoderMB, browserMB: float64(p.Heap) / (1 << 20), goroutines: runtime.NumGoroutine(), handles: runnerH}
}

// judgeSoak compares the first ten minutes with the last.
func judgeSoak(first, last []soakLine, minutes int) error {
	if len(first) == 0 || len(last) == 0 {
		return fmt.Errorf("nothing was measured")
	}
	mean := func(l []soakLine, get func(soakLine) float64) float64 {
		var t float64
		for _, x := range l {
			t += get(x)
		}
		return t / float64(len(l))
	}
	fmt.Printf("after %d minutes, first ten against last ten:\n", minutes)
	worse := false
	for _, c := range []struct {
		name  string
		get   func(soakLine) float64
		slack float64
	}{
		{"glass ms", func(x soakLine) float64 { return x.glass }, 3},
		{"runner MB", func(x soakLine) float64 { return x.runnerMB }, 32},
		{"server MB", func(x soakLine) float64 { return x.serverMB }, 16},
		{"encoder MB", func(x soakLine) float64 { return x.encoderMB }, 32},
		{"page MB", func(x soakLine) float64 { return x.browserMB }, 32},
		{"goroutines", func(x soakLine) float64 { return float64(x.goroutines) }, 8},
		{"runner handles", func(x soakLine) float64 { return float64(x.handles) }, 16},
	} {
		a, b := mean(first, c.get), mean(last, c.get)
		verdict := "ok"
		if b > a+c.slack {
			verdict = "WORSE"
			worse = true
		}
		fmt.Printf("  %-16s %9.1f %9.1f  %s\n", c.name, a, b, verdict)
	}
	if worse {
		return fmt.Errorf("something grew")
	}
	return nil
}
