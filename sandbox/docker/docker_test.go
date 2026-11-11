// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const pinned = "ghcr.io/wittywilla/wittyplay-emu-test:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func request() Request {
	return Request{
		SessionID: "0123456789abcdef0123456789abcdef", UserID: 7, Emulator: "pcsx2", Image: pinned,
		ROM: "/library/games/ps2/game.iso", SaveDir: "/data/saves/7/pcsx2", BIOSDir: "/data/bios/7",
		Cmd: []string{"emu", "/game/game.iso"}, Stream: "pipe", InputToken: NewSecret(), Network: "wittyplay", RTPHost: "172.30.0.1", RTPVideo: 40000, RTPAudio: 40002,
	}
}

// TestBuildIsHardened checks every security field of the spec, for each way a container can be
// asked for.
func TestBuildIsHardened(t *testing.T) {
	kasm := request()
	kasm.Stream, kasm.KasmToken = "kasmvnc", NewSecret()
	loop := request()
	loop.PublishLoopback = true
	gpu := request()
	gpu.GPU = GPU{DRI: true, Nvidia: true}
	noBios := request()
	noBios.BIOSDir = ""
	cases := map[string]Request{"pipe": request(), "kasmvnc": kasm, "loopback": loop, "gpu": gpu, "no bios": noBios}

	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			s, err := Build(r)
			if err != nil {
				t.Fatal(err)
			}
			checks := []struct {
				what string
				ok   bool
			}{
				{"non-root user", s.User == "1000:1000"},
				{"read-only root", s.ReadOnlyRoot},
				{"cap drop ALL", slices.Equal(s.CapDrop, []string{"ALL"})},
				{"no caps added", len(s.CapAdd) == 0},
				{"no-new-privileges", slices.Contains(s.SecurityOpt, "no-new-privileges:true")},
				{"default seccomp (no override)", !slices.ContainsFunc(s.SecurityOpt, func(o string) bool { return strings.HasPrefix(o, "seccomp") })},
				{"not privileged", !s.Privileged},
				{"init", s.Init},
				{"pids limit", s.PidsLimit == DefaultPids},
				{"memory limit", s.MemoryBytes == DefaultMemoryMB<<20},
				{"cpu limit", s.NanoCPUs == int64(DefaultCPUs*1e9)},
				{"tmpfs /tmp", strings.Contains(s.Tmpfs["/tmp"], "noexec") && strings.Contains(s.Tmpfs["/tmp"], "nosuid")},
				{"own network", s.Network == "wittyplay"},
				{"labels", s.Labels[LabelManaged] == "true" && s.Labels[LabelSession] == r.SessionID && s.Labels[LabelUser] == "7"},
				{"pinned image", s.Image == pinned},
				{"passes Check", Check(s) == nil},
			}
			for _, c := range checks {
				if !c.ok {
					t.Errorf("%s: %+v", c.what, s)
				}
			}
			// Mounts: the game read-only, saves read-write, BIOS read-only and only if given.
			var rom, saves, bios *Mount
			for i := range s.Mounts {
				switch m := &s.Mounts[i]; {
				case m.Source == r.ROM:
					rom = m
				case m.Source == r.SaveDir:
					saves = m
				case m.Source == r.BIOSDir:
					bios = m
				}
			}
			if rom == nil || !rom.ReadOnly || saves == nil || saves.ReadOnly {
				t.Errorf("mounts: %+v", s.Mounts)
			}
			if (r.BIOSDir != "") != (bios != nil) || bios != nil && !bios.ReadOnly {
				t.Errorf("bios mount: %+v", s.Mounts)
			}
			if len(s.Mounts) > 3 {
				t.Errorf("more mounts than needed: %+v", s.Mounts)
			}
			// Ports only on loopback, and only when asked.
			if !r.PublishLoopback && len(s.Publish) > 0 {
				t.Errorf("published without being asked: %+v", s.Publish)
			}
			for _, p := range s.Publish {
				if p.HostIP != netip.MustParseAddr("127.0.0.1") {
					t.Errorf("published on %v", p.HostIP)
				}
			}
			// GPU only when asked.
			if r.GPU.DRI != slices.Contains(s.Devices, "/dev/dri") || r.GPU.Nvidia != s.NvidiaGPUs {
				t.Errorf("gpu: %+v %v", s.Devices, s.NvidiaGPUs)
			}
		})
	}
}

func TestBuildRefuses(t *testing.T) {
	mut := map[string]func(*Request){
		"unpinned image":  func(r *Request) { r.Image = "ghcr.io/a/b:latest" },
		"root":            func(r *Request) { r.Limits.UID = -1 },
		"relative rom":    func(r *Request) { r.ROM = "game.iso" },
		"dotdot save":     func(r *Request) { r.SaveDir = "/data/../etc" },
		"etc":             func(r *Request) { r.SaveDir = "/etc" },
		"docker socket":   func(r *Request) { r.BIOSDir = "/var/run/docker.sock" },
		"host network":    func(r *Request) { r.Network = "host" },
		"no network":      func(r *Request) { r.Network = "" },
		"default bridge":  func(r *Request) { r.Network = "bridge" },
		"bad session":     func(r *Request) { r.SessionID = "../x" },
		"ld_preload":      func(r *Request) { r.Env = []string{"LD_PRELOAD=/x.so"} },
		"own variable":    func(r *Request) { r.Env = []string{"WITTYPLAY_STREAM=other"} },
		"kasm without pw": func(r *Request) { r.Stream = "kasmvnc" },
		"pipe without rtp": func(r *Request) {
			r.RTPHost = ""
		},
	}
	for name, m := range mut {
		r := request()
		m(&r)
		if _, err := Build(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckRefusesWeakenedSpecs(t *testing.T) {
	good, err := Build(request())
	if err != nil {
		t.Fatal(err)
	}
	mut := map[string]func(*Spec){
		"root":              func(s *Spec) { s.User = "0:0" },
		"writable":          func(s *Spec) { s.ReadOnlyRoot = false },
		"caps":              func(s *Spec) { s.CapAdd = []string{"SYS_ADMIN"} },
		"no nnp":            func(s *Spec) { s.SecurityOpt = nil },
		"unconfined":        func(s *Spec) { s.SecurityOpt = append(s.SecurityOpt, "seccomp=unconfined") },
		"privileged":        func(s *Spec) { s.Privileged = true },
		"no pids":           func(s *Spec) { s.PidsLimit = 0 },
		"no memory":         func(s *Spec) { s.MemoryBytes = 0 },
		"public port":       func(s *Spec) { s.Publish = []Publish{{Port: 1, Proto: "tcp", HostIP: netip.MustParseAddr("0.0.0.0")}} },
		"rom writable":      func(s *Spec) { s.Mounts[0].ReadOnly = false },
		"device":            func(s *Spec) { s.Devices = []string{"/dev/kvm"} },
		"no label":          func(s *Spec) { s.Labels = map[string]string{} },
		"no cpu":            func(s *Spec) { s.NanoCPUs = 0 },
		"no init":           func(s *Spec) { s.Init = false },
		"apparmor off":      func(s *Spec) { s.SecurityOpt = append(s.SecurityOpt, "apparmor=unconfined") },
		"cap drop few":      func(s *Spec) { s.CapDrop = []string{"NET_RAW"} },
		"host network":      func(s *Spec) { s.Network = "host" },
		"no network":        func(s *Spec) { s.Network = "none" },
		"another's network": func(s *Spec) { s.Network = "container:abc" },
		"tag only":          func(s *Spec) { s.Image = "ghcr.io/a/b:latest" },
		"no image":          func(s *Spec) { s.Image = "" },
		"exec tmp":          func(s *Spec) { s.Tmpfs = map[string]string{"/tmp": "rw,exec,nosuid,nodev"} },
		"suid tmp":          func(s *Spec) { s.Tmpfs = map[string]string{"/tmp": "rw,noexec,nodev"} },
		"no tmp":            func(s *Spec) { s.Tmpfs = map[string]string{} },
		"docker socket": func(s *Spec) {
			s.Mounts = append(s.Mounts, Mount{Source: "/var/run/docker.sock", Target: "/saves/docker.sock"})
		},
		"a socket by another name": func(s *Spec) { s.Mounts[1].Source = "/home/me/.docker/run/docker.sock" },
		"podman socket":            func(s *Spec) { s.Mounts[1].Source = "/run/user/1000/podman/podman.sock" },
		"etc under saves":          func(s *Spec) { s.Mounts[1].Source = "/etc/cron.d" },
		"an extra mount":           func(s *Spec) { s.Mounts = append(s.Mounts, Mount{Source: "/home/me", Target: "/host"}) },
		"two games": func(s *Spec) {
			s.Mounts = append(s.Mounts, Mount{Source: "/library/other.iso", Target: "/game/other.iso", ReadOnly: true})
		},
		"bios writable":     func(s *Spec) { s.Mounts[2].ReadOnly = false },
		"ld_preload":        func(s *Spec) { s.Env = append(slices.Clone(s.Env), "LD_PRELOAD=/saves/x.so") },
		"local on a daemon": func(s *Spec) { s.Local = true },
	}
	for name, m := range mut {
		s := good
		s.Mounts = slices.Clone(good.Mounts)
		s.SecurityOpt = slices.Clone(good.SecurityOpt)
		m(&s)
		if Check(s) == nil {
			t.Errorf("%s: passed Check", name)
		}
		f := &Fake{}
		if _, err := f.Create(context.Background(), s); err == nil {
			t.Errorf("%s: the runtime created it", name)
		}
	}
}

var specUser int64 = 100

func spec(t *testing.T, session string) Spec {
	t.Helper()
	r := request()
	r.SessionID = session
	specUser++ // each session is someone else's, as one person only ever has one
	r.UserID = specUser
	s, err := Build(r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestLifecycleIdleAndLifetime(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	clk := &clock{t: time.Unix(1_000_000, 0)}
	l := &Lifecycle{RT: f, Idle: time.Minute, MaxLifetime: time.Hour, Now: clk.now, Max: 4}

	a, err := l.Start(ctx, spec(t, "aaaaaaaaaaaaaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Start(ctx, spec(t, "bbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatal(err)
	}
	clk.add(50 * time.Second)
	l.Touch("bbbbbbbbbbbbbbbb")
	clk.add(20 * time.Second) // a: 70 s idle; b: 20 s
	l.Reap(ctx)
	select {
	case <-a.Done():
	default:
		t.Fatal("an idle container wasn't stopped")
	}
	if a.Reason() != "idle" || l.Get("bbbbbbbbbbbbbbbb") == nil {
		t.Fatalf("reason %q; b still there: %v", a.Reason(), l.Get("bbbbbbbbbbbbbbbb") != nil)
	}
	// However busy, b ends at its lifetime.
	for range 70 {
		clk.add(time.Minute)
		l.Touch("bbbbbbbbbbbbbbbb")
		l.Reap(ctx)
	}
	if b.Reason() != "lifetime" {
		t.Fatalf("b ended for %q", b.Reason())
	}
	if f.Live() != 0 {
		t.Fatalf("%d containers left", f.Live())
	}
}

func TestLifecycleExitedByItself(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	l := &Lifecycle{RT: f}
	c, err := l.Start(ctx, spec(t, "cccccccccccccccc"))
	if err != nil {
		t.Fatal(err)
	}
	f.Exit(c.ID)
	l.Reap(ctx)
	if c.Reason() != "exited" || f.Live() != 0 {
		t.Fatalf("reason %q, live %d", c.Reason(), f.Live())
	}
}

func TestLifecycleOrphanSweep(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	orphan := f.Add(map[string]string{LabelManaged: "true", LabelSession: "old"}, true)
	exited := f.Add(map[string]string{LabelManaged: "true", LabelSession: "older"}, false)
	other := f.Add(map[string]string{"com.example": "yes"}, true) // not ours: never touched
	l := &Lifecycle{RT: f}
	mine, err := l.Start(ctx, spec(t, "dddddddddddddddd"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := l.SweepOrphans(ctx)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	if !f.Containers[orphan].Removed || !f.Containers[exited].Removed {
		t.Error("an orphan was left")
	}
	if f.Containers[other].Removed {
		t.Error("someone else's container was removed")
	}
	if f.Containers[mine.ID].Removed {
		t.Error("a running session's container was removed")
	}
}

func TestLifecycleConcurrencyLimit(t *testing.T) {
	ctx := context.Background()
	f := &Fake{}
	l := &Lifecycle{RT: f, Max: 2}
	ids := []string{"1111111111111111", "2222222222222222", "3333333333333333", "4444444444444444", "5555555555555555"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	started, busy := 0, 0
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := l.Start(ctx, spec(t, id))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				started++
			case errors.Is(err, ErrBusy):
				busy++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if started != 2 || busy != 3 || l.Count() != 2 || f.Live() != 2 {
		t.Fatalf("started %d, busy %d, count %d, live %d", started, busy, l.Count(), f.Live())
	}
	// Room again once one stops.
	if err := l.Stop(ctx, func() string {
		for _, id := range ids {
			if l.Get(id) != nil {
				return id
			}
		}
		return ""
	}()); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(ctx, spec(t, "6666666666666666")); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleCleansUpAFailedStart(t *testing.T) {
	f := &Fake{StartErr: errors.New("boom")}
	l := &Lifecycle{RT: f}
	if _, err := l.Start(context.Background(), spec(t, "eeeeeeeeeeeeeeee")); err == nil {
		t.Fatal("no error")
	}
	if f.Live() != 0 || l.Count() != 0 {
		t.Fatalf("left behind: live %d count %d", f.Live(), l.Count())
	}
}

func TestUnavailableIsReported(t *testing.T) {
	f := &Fake{Down: true}
	if st := f.Available(context.Background()); st.Available || st.Error == "" {
		t.Fatalf("%+v", st)
	}
	l := &Lifecycle{RT: f}
	if _, err := l.Start(context.Background(), spec(t, "ffffffffffffffff")); err == nil {
		t.Fatal("started with Docker down")
	}
}

func TestKasmProxyEnforcesAuth(t *testing.T) {
	password := NewSecret()
	var gotAuth, gotCookie, gotPath, gotQuery, gotWitty string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotCookie, gotPath, gotQuery, gotWitty = r.Header.Get("Authorization"), r.Header.Get("Cookie"), r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Witty-User")
		// A container that has been taken over tries what it can on the browser.
		w.Header().Set("WWW-Authenticate", `Basic realm="kasm"`)
		w.Header().Set("Content-Security-Policy", "script-src *")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.SetCookie(w, &http.Cookie{Name: "wms_session", Value: "stolen", Path: "/"})
		switch r.URL.Path {
		case "/go":
			w.Header().Set("Location", "/vnc.html")
			w.WriteHeader(http.StatusFound)
		case "/away":
			w.Header().Set("Location", "https://example.org/phish")
			w.WriteHeader(http.StatusFound)
		default:
			_, _ = w.Write([]byte("desktop"))
		}
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)

	p := &KasmProxy{}
	ticket, err := p.Register("sess1", u, password, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 64 {
		t.Fatalf("a ticket of %d characters", len(ticket))
	}
	if path, ok := p.Path("sess1"); !ok || path != "/kasm/sess1/"+ticket+"/" {
		t.Fatalf("path: %q", path)
	}
	mux := http.NewServeMux()
	mux.Handle("/kasm/", http.StripPrefix("/kasm", p))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	do := func(method, path string, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	get := func(path string, hdr map[string]string) *http.Response { return do("GET", path, hdr) }

	// Without the ticket in the address there's nothing there, whatever else a request carries:
	// not the ticket as a parameter, a header or a cookie, and not the container's own password.
	for what, r := range map[string]*http.Response{
		"no ticket":                get("/kasm/sess1/", nil),
		"a wrong ticket":           get("/kasm/sess1/"+strings.Repeat("0", 64)+"/", nil),
		"a short ticket":           get("/kasm/sess1/"+ticket[:63]+"/", nil),
		"the ticket as a query":    get("/kasm/sess1/?ticket="+ticket, nil),
		"the ticket as a cookie":   get("/kasm/sess1/", map[string]string{"Cookie": "wittyplay_kasm=" + ticket}),
		"the ticket as a header":   get("/kasm/sess1/", map[string]string{"X-WittyPlay-Kasm-Ticket": ticket}),
		"the container's password": get("/kasm/sess1/", map[string]string{"Authorization": BasicAuthHeader(password)}),
		"another session":          get("/kasm/other/"+ticket+"/", nil),
		"no session":               get("/kasm/", nil),
		"a write":                  do("POST", "/kasm/sess1/"+ticket+"/api/x", nil),
	} {
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", what, r.StatusCode)
		}
		if !strings.Contains(r.Header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: an answer without the sandbox", what)
		}
	}
	if gotPath != "" {
		t.Fatalf("a refused request reached the container: %q", gotPath)
	}

	r := get("/kasm/sess1/"+ticket+"/vnc.html?x=1", map[string]string{"Cookie": "wms_session=secret", "Authorization": "Bearer mine", "X-Witty-User": "7"})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("the right ticket: %d", r.StatusCode)
	}
	if gotAuth != BasicAuthHeader(password) {
		t.Errorf("the container wasn't given its password: %q", gotAuth)
	}
	if gotCookie != "" || gotWitty != "" {
		t.Errorf("the browser's cookies or the server's headers reached the container: %q %q", gotCookie, gotWitty)
	}
	if gotPath != "/vnc.html" || gotQuery != "x=1" || strings.Contains(gotPath+gotQuery, ticket) {
		t.Errorf("upstream saw %q ? %q", gotPath, gotQuery)
	}
	// What the container sent to turn the browser against the server doesn't arrive.
	if r.Header.Get("WWW-Authenticate") != "" || len(r.Cookies()) != 0 || r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("the container's headers reached the browser: %v", r.Header)
	}
	csp := r.Header.Values("Content-Security-Policy")
	if len(csp) != 1 || csp[0] != SandboxPolicy {
		t.Errorf("the content policy: %q", csp)
	}
	// The sandbox gives scripts, and never the server's origin: a page from a container can't
	// read the app's storage or ask the server anything as the person signed in.
	box := SandboxPolicy[strings.Index(SandboxPolicy, "sandbox"):]
	if !strings.Contains(box, "allow-scripts") || strings.Contains(box, "allow-same-origin") || strings.Contains(box, "allow-top-navigation") || strings.Contains(box, "allow-popups") {
		t.Errorf("the sandbox: %q", box)
	}
	// Redirects stay within this session's address, or go nowhere.
	if loc := get("/kasm/sess1/"+ticket+"/go", nil).Header.Get("Location"); loc != "/kasm/sess1/"+ticket+"/vnc.html" {
		t.Errorf("a redirect within the desktop: %q", loc)
	}
	if loc := get("/kasm/sess1/"+ticket+"/away", nil).Header.Get("Location"); loc != "" {
		t.Errorf("a redirect to somewhere else: %q", loc)
	}

	// Only loopback and private addresses can be a container; a session that's forgotten or has
	// run out is gone.
	pub, _ := url.Parse("http://203.0.113.5:6901")
	if _, err := p.Register("x", pub, password, time.Hour); err == nil {
		t.Error("a public address was accepted as a container")
	}
	if _, err := p.Register("x", u, "short", time.Hour); err == nil {
		t.Error("a short password was accepted")
	}
	p.Forget("sess1")
	if r := get("/kasm/sess1/"+ticket+"/", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("after forgetting: %d", r.StatusCode)
	}
	old, _ := p.Register("sess2", u, password, -time.Second)
	if r := get("/kasm/sess2/"+old+"/", nil); r.StatusCode != http.StatusNotFound {
		t.Errorf("a ticket that has run out: %d", r.StatusCode)
	}
}
