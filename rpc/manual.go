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
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("%s isn't on the internet", host)
		}
		return nil
	}}).DialContext,
	TLSHandshakeTimeout: 30 * time.Second,
}}

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
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return &cores.Manager{Dir: r.Dir, System: r.System, Extra: r.Extra, Client: safeClient, Custom: func() []cores.Core { return r.Custom }}, nil
}

func resolveStore(raw json.RawMessage) (*cores.CustomStore, error) {
	var st cores.CustomStore
	if err := json.Unmarshal(raw, &st); err != nil {
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
	Handlers["stream.Manager.Configure"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
		var c wireConfig
		if err := service.Arg(args, 0, &c); err != nil {
			return nil, nil, err
		}
		configure(c.Config, c.Encoders)
		return nil, nil, nil
	}
	Handlers["stream.Manager.Start"] = func(_ context.Context, _ json.RawMessage, args []json.RawMessage) ([]any, any, error) {
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
		s := Mgr.Get(id)
		if s == nil {
			return []any{true}, nil, nil
		}
		select {
		case <-s.Done():
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

// Routes are what isn't a call: a browser's connection to a game, passed straight through.
func Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/{sid}", func(w http.ResponseWriter, r *http.Request) {
		s := Mgr.Get(r.PathValue("sid"))
		if s == nil {
			http.NotFound(w, r)
			return
		}
		s.ServeWS(w, r, r.URL.Query().Get("video"))
	})
}

// Busy says a game is being played.
func Busy() bool { return Mgr.Count() > 0 }

// Stop saves and ends every game, as the server goes away.
func Stop() { Mgr.StopAll() }
