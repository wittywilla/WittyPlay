// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// run plays a game through the whole real path to a real browser, which measures what it
// sees and posts the numbers back.
func run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	f := addRigFlags(fs)
	out := fs.String("out", "", "add the results to this JSON lines file")
	ahead := fs.Int("run-ahead", 0, "frames of run-ahead")
	_ = fs.Parse(args)
	r, err := f.start(nil)
	if err != nil {
		return err
	}
	defer r.close()
	if *ahead > 0 {
		if err := r.session().Apply(ipc.Control{RunAhead: ahead}, nil); err != nil {
			return err
		}
	}
	limit := time.Duration(f.seconds+90) * time.Second
	select {
	case b := <-r.results:
		return report(b, f.label, *out, r.session())
	case <-time.After(limit):
		return fmt.Errorf("no results from the page after %s", limit)
	case <-r.session().Done():
		return fmt.Errorf("the game ended")
	}
}

type summary struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	N   int     `json:"n"`
}

type pageResults struct {
	Error      string             `json:"error"`
	Label      string             `json:"label"`
	Kind       string             `json:"kind"`
	UA         string             `json:"ua"`
	Frames     map[string]summary `json:"frames"`
	Presses    map[string]summary `json:"presses"`
	Glass      summary            `json:"glass"`
	Photon     summary            `json:"photon"`
	Made       summary            `json:"madeToHanded"`
	Gap        summary            `json:"interval"`
	Pixels     map[string]int     `json:"pixels"`
	Mis        map[string]int     `json:"misfiled"`
	Stats      map[string]any     `json:"stats"`
	Waits      []float64          `json:"waitTrace"`
	Shifted    float64            `json:"shifted"`
	Recoveries []float64          `json:"recoveries"`
	DropWhy    map[string]int     `json:"dropWhy"`
	Heap       int64              `json:"heap"`
	Colours    struct {
		Error int     `json:"error"`
		Seen  [][]int `json:"seen"`
	} `json:"colours"`
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
	fmt.Println("  colours, worst error out of 255:", r.Colours.Error, "seen:", r.Colours.Seen)
	fmt.Printf("  moved frames by %.1f ms; waits for the display over time: %v\n", r.Shifted, r.Waits)
	if len(r.Recoveries) > 0 {
		fmt.Println("  came back after a lost connection, ms:", r.Recoveries, "why:", r.DropWhy)
	}
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
