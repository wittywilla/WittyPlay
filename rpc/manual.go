// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package rpc answers a server's calls to WittyPlay's service: gen.go is generated, by kit/facade in
// WittyModule, and this is what can't be.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/stream"
)

// Mgr is this process's games.
var Mgr = stream.NewManager(stream.Config{Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})

var errNoSession = errors.New("that game has ended")

// safeClient fetches what someone asks for by address. It refuses anything private when it
// connects, because a name can resolve to anything, and has no proxy, which would only hide that.
var safeClient = &http.Client{Timeout: 15 * time.Minute, Transport: &http.Transport{
	DialContext: (&net.Dialer{Timeout: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || !onTheInternet(ip) {
			return fmt.Errorf("%s isn't on the internet", host)
		}
		return nil
	}}).DialContext,
	TLSHandshakeTimeout: 30 * time.Second,
}}

// notInternet are ranges that aren't the internet, beyond those the standard library names: shared
// address space, documentation, and benchmarks.
var notInternet = []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "fec0::/10"}

// onTheInternet is what a fetched address has to be, including inside the IPv6 addresses that carry
// an IPv4 one for a gateway to unwrap.
func onTheInternet(ip net.IP) bool {
	if v6 := ip.To16(); v6 != nil && ip.To4() == nil {
		switch {
		case v6[0] == 0x00 && v6[1] == 0x64 && v6[2] == 0xff && v6[3] == 0x9b:
			ip = net.IP(v6[12:16])
		case v6[0] == 0x20 && v6[1] == 0x02:
			ip = net.IP(v6[2:6])
		case v6[0] == 0x20 && v6[1] == 0x01 && v6[2] == 0 && v6[3] == 0:
			return false // Teredo
		}
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, s := range notInternet {
		if _, n, err := net.ParseCIDR(s); err == nil && n.Contains(ip) {
			return false
		}
	}
	return true
}

func resolveManager(json.RawMessage) (*stream.Manager, error) { return Mgr, nil }

func resolveSession(raw json.RawMessage) (*stream.Session, error) {
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if s := Mgr.Get(r.ID); s != nil {
		return s, nil
	}
	return nil, errNoSession
}

func resolveCores(raw json.RawMessage) (*cores.Manager, error) {
	var r struct {
		Dir    string
		System string
		Extra  []string
		Custom []cores.Core
	}
	if err := service.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &cores.Manager{Dir: r.Dir, System: r.System, Extra: r.Extra, Client: safeClient, Custom: func() []cores.Core { return r.Custom }}, nil
}

func resolveStore(raw json.RawMessage) (*cores.CustomStore, error) {
	var st cores.CustomStore
	if err := service.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	st.Client = safeClient
	return &st, nil
}

// configure makes what arrived into a process's settings.
func configure(in stream.Config, encoders []string) {
	Mgr.Configure(func(c *stream.Config) {
		log := c.Log
		*c = in
		c.Log = log
		c.HaveEncoder = func(n string) bool { return slices.Contains(encoders, n) }
	})
}

type wireConfig struct {
	stream.Config
	Encoders []string
}

func init() {
	service.RegisterErrorType[stream.ProbeError]("stream.ProbeError")
	service.RegisterErrorType[stream.StreamError]("stream.StreamError")
	Handlers["stream.Manager.Configure"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var c wireConfig
		if err := service.Arg(args, 0, &c); err != nil {
			return nil, nil, err
		}
		configure(c.Config, c.Encoders)
		return nil, nil, nil
	}
	// A game in a standalone emulator. Only the server calls these, with a game it has checked
	// this person may play; a refusal comes back as an answer, not a failure.
	Handlers["docker.Launch"] = func(ctx context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var o LaunchOptions
		if err := service.Arg(args, 0, &o); err != nil {
			return nil, nil, err
		}
		ds, err := Docker.Launch(ctx, o)
		var no *Refusal
		if errors.As(err, &no) {
			return []any{LaunchResult{Refusal: no}}, nil, nil
		}
		if err != nil {
			return nil, nil, err
		}
		return []any{LaunchResult{Session: ds}}, nil, nil
	}
	Handlers["docker.Offers"] = func(ctx context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var console, systemDir string
		if err := service.Arg(args, 0, &console); err != nil {
			return nil, nil, err
		}
		if err := service.Arg(args, 1, &systemDir); err != nil {
			return nil, nil, err
		}
		return []any{Docker.Offers(ctx, console, systemDir)}, nil, nil
	}
	Handlers["stream.Manager.Start"] = func(ctx context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var o stream.Options
		if err := service.Arg(args, 0, &o); err != nil {
			return nil, nil, err
		}
		o.OnCoreOptions = func(coreID string, list []ipc.Option) {
			_ = wittymodule.Notify("stream.coreoptions", struct {
				CoreID string       `json:"coreId"`
				List   []ipc.Option `json:"list"`
			}{coreID, list})
		}
		s, err := Mgr.Start(o)
		if err != nil {
			return nil, nil, err
		}
		return []any{s}, nil, nil
	}
	Handlers["stream.Manager.StopAll"] = func(context.Context, json.RawMessage, []json.RawMessage) ([]any, any, error) {
		Mgr.StopAll()
		return nil, nil, nil
	}
	Handlers["stream.Session.Wait"] = func(ctx context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var id string
		if err := service.Arg(args, 0, &id); err != nil {
			return nil, nil, err
		}
		var done <-chan struct{}
		if s := Mgr.Get(id); s != nil {
			done = s.Done()
		} else if ds := Docker.Session(id); ds != nil {
			done = ds.Done()
		} else {
			return []any{true}, nil, nil
		}
		select {
		case <-done:
			return []any{true}, nil, nil
		case <-ctx.Done():
			return []any{false}, nil, nil
		}
	}
	Handlers["stream.ProbeGPU"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var runner, dir string
		if err := service.Arg(args, 0, &runner); err != nil {
			return nil, nil, err
		}
		if err := service.Arg(args, 1, &dir); err != nil {
			return nil, nil, err
		}
		return []any{stream.ProbeGPU(runner, dir, slog.New(slog.NewTextHandler(os.Stderr, nil)))}, nil, nil
	}
	Handlers["wittyplay.Consoles"] = func(context.Context, json.RawMessage, []json.RawMessage) ([]any, any, error) {
		return []any{wittyplay.Consoles}, nil, nil
	}
	Handlers["wittyplay.SetCustomConsoles"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var list []wittyplay.Console
		if err := service.Arg(args, 0, &list); err != nil {
			return nil, nil, err
		}
		wittyplay.SetCustomConsoles(list)
		return nil, nil, nil
	}
	Handlers["wittyplay.CleanProfile"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var p wittyplay.Profile
		var known []string
		if err := service.Arg(args, 0, &p); err != nil {
			return nil, nil, err
		}
		if err := service.Arg(args, 1, &known); err != nil {
			return nil, nil, err
		}
		out, err := wittyplay.CleanProfile(p, func(id string) bool { return slices.Contains(known, id) })
		return []any{out}, nil, err
	}
	Handlers["cores.CustomStore.StageFile"] = func(_ context.Context, raw json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		st, err := resolveStore(raw)
		if err != nil {
			return nil, nil, err
		}
		var path, name string
		if err := service.Arg(args, 0, &path); err != nil {
			return nil, nil, err
		}
		if err := service.Arg(args, 1, &name); err != nil {
			return nil, nil, err
		}
		f, err := openUpload(st, path)
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		out, err := st.StageReader(f, name)
		return []any{out}, nil, err
	}
	Handlers["cores.CustomStore.AddExtrasFile"] = func(_ context.Context, raw json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		st, err := resolveStore(raw)
		if err != nil {
			return nil, nil, err
		}
		var path, name, id, sum string
		for i, p := range []*string{&path, &name, &id, &sum} {
			if err := service.Arg(args, i, p); err != nil {
				return nil, nil, err
			}
		}
		f, err := openUpload(st, path)
		if err != nil {
			return nil, nil, err
		}
		defer f.Close()
		added, kept, err := st.AddExtras(f, name, id, sum)
		return []any{added, kept}, nil, err
	}
}

// openUpload opens an upload a server kept beside the store, and nothing else.
func openUpload(st *cores.CustomStore, path string) (*os.File, error) {
	if !strings.HasPrefix(path, st.Dir) {
		abs := strings.TrimPrefix(path, st.Dir)
		_ = abs
	}
	return os.Open(path)
}

// Routes are what isn't a call: a browser's connection to a game, passed straight through, and
// the Docker backend's routes (docker.go). Containers a crash left behind are removed here, in
// the background, only when Docker is on and reachable; nothing about Docker can stop the
// service starting.
func Routes(mux *http.ServeMux) {
	Docker.Routes(mux)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		Docker.Startup(ctx)
	}()
	mux.HandleFunc("GET /ws/{sid}", func(w http.ResponseWriter, r *http.Request) {
		s := Mgr.Get(r.PathValue("sid"))
		if s == nil {
			http.NotFound(w, r)
			return
		}
		s.ServeWS(w, r, r.URL.Query().Get("video"))
	})
	// Version 2's WebSocket. Only the server reaches this, and it says where the device is.
	mux.HandleFunc("GET /stream/{sid}", func(w http.ResponseWriter, r *http.Request) {
		s := Mgr.Get(r.PathValue("sid"))
		if s == nil {
			http.NotFound(w, r)
			return
		}
		s.ServeStream(w, r, r.URL.Query().Get("away") == "1")
	})
}

// Busy says a game is being played, by a core or in a container.
func Busy() bool { return Mgr.Count() > 0 || Docker.Count() > 0 }

// Stop saves and ends every game, as the server goes away.
func Stop() {
	Mgr.StopAll()
	Docker.StopAll()
}
