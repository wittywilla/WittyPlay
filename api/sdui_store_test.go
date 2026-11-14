// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/sdui"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/emulators"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// The library and the store, as the apps' pages are given them (sdui_store.go).

// moduleUI is what WittyPlay's sdui.json and integration.json say: each source's yields, each of
// its own commands, and the routes the server passes on to it.
type moduleUI struct {
	yields   map[string][]string
	commands map[string]sduiCommand
	routes   []struct {
		Path    string   `json:"path"`
		Methods []string `json:"methods"`
		Auth    string   `json:"auth"`
		Confirm bool     `json:"confirm"`
	}
}

type sduiCommand struct {
	Op     string `json:"op"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   string `json:"body"`
	Params []struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Required bool   `json:"required"`
	} `json:"params"`
}

func readModuleUI(t *testing.T) moduleUI {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "sdui.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Sources []struct {
			Type   string   `json:"type"`
			Yields []string `json:"yields"`
		} `json:"sources"`
		Commands []sduiCommand `json:"commands"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	ui := moduleUI{yields: map[string][]string{}, commands: map[string]sduiCommand{}}
	for _, s := range file.Sources {
		ui.yields[s.Type] = s.Yields
	}
	for _, c := range file.Commands {
		ui.commands[c.Op] = c
	}
	b, err = os.ReadFile(filepath.Join("..", "integration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var integ struct {
		HTTP json.RawMessage `json:"http"`
	}
	if err := json.Unmarshal(b, &integ); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(integ.HTTP, &ui.routes); err != nil {
		t.Fatal(err)
	}
	return ui
}

// request is the request an app makes for a command action, as the catalogue says: the item in
// place of {item} and each parameter in place of {name}, URL-escaped in the path and JSON-escaped in
// the body. The path is the module's own, without the server's /api/modules/wittyplay.
func (c sduiCommand) request(t *testing.T, a *sdui.Action) *http.Request {
	t.Helper()
	path := strings.ReplaceAll(c.Path, "{item}", url.PathEscape(a.Item))
	body := strings.ReplaceAll(c.Body, `"{item}"`, jsonText(a.Item))
	for _, p := range c.Params {
		v := fmt.Sprint(a.Params[p.Name])
		if _, ok := a.Params[p.Name]; !ok {
			v = ""
		}
		path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(v))
		body = strings.ReplaceAll(body, `"{`+p.Name+`}"`, jsonText(v))
	}
	if body != "" && !json.Valid([]byte(body)) {
		t.Fatalf("%s: the body isn't JSON: %s", c.Op, body)
	}
	return httptest.NewRequest(c.Method, strings.TrimPrefix(path, "/api/modules/wittyplay"), strings.NewReader(body))
}

func jsonText(s string) string { b, _ := json.Marshal(s); return string(b) }

// checkRows checks a source's rows are ones the server keeps and every app can run: a kind the
// source yields, an id and a title, and every command one of WittyPlay's own (sdui.json), with
// only the values it declares and those it requires, on a route integration.json passes on.
func checkRows(t *testing.T, ui moduleUI, src string, items []sdui.Item) {
	t.Helper()
	ids := map[string]bool{}
	for _, it := range items {
		if !slices.Contains(ui.yields[src], it.Kind) || it.ID == "" || strings.TrimSpace(it.Title) == "" || ids[it.ID] {
			t.Errorf("%s gave a row the server drops: %+v", src, it)
		}
		ids[it.ID] = true
		if len([]rune(it.Trailing)) > 40 {
			t.Errorf("%s: %q is cut at a row's end", src, it.Trailing)
		}
		acts := []*sdui.Action{it.Action}
		for _, a := range it.Actions {
			acts = append(acts, a.Action)
		}
		for _, a := range acts {
			if a == nil {
				continue
			}
			switch a.Type {
			case "navigate":
				if !strings.HasPrefix(a.To, "/") || strings.HasPrefix(a.To, "//") {
					t.Errorf("%s: %q isn't a place in the app", src, a.To)
				}
			case "command":
				c, ok := ui.commands[a.Op]
				if !ok {
					t.Errorf("%s: %s isn't one of sdui.json's commands", src, a.Op)
					continue
				}
				if !regexp.MustCompile(`^[A-Za-z0-9_.:~-]{1,128}$`).MatchString(a.Item) {
					t.Errorf("%s: %q can't be a command's item", src, a.Item)
				}
				for name := range a.Params {
					if !slices.ContainsFunc(c.Params, func(p struct {
						Name     string `json:"name"`
						Type     string `json:"type"`
						Required bool   `json:"required"`
					}) bool {
						return p.Name == name
					}) {
						t.Errorf("%s: %s takes no %s", src, a.Op, name)
					}
				}
				for _, p := range c.Params {
					if _, ok := a.Params[p.Name]; p.Required && !ok {
						t.Errorf("%s: %s needs %s", src, a.Op, p.Name)
					}
				}
			default:
				t.Errorf("%s: a %s action", src, a.Type)
			}
		}
		if c := it.Control; c != nil && c.Type == "select" {
			if it.Action == nil || it.Action.Type != "command" || !slices.ContainsFunc(ui.commands[it.Action.Op].Params, func(p struct {
				Name     string `json:"name"`
				Type     string `json:"type"`
				Required bool   `json:"required"`
			}) bool {
				return p.Name == "value"
			}) {
				t.Errorf("%s: a choice whose command doesn't take the value chosen: %+v", src, it)
			}
		}
	}
}

// rows asks for a source as a page for viewer would.
func rows(t *testing.T, ui moduleUI, viewer sdui.Viewer, src string, params ...any) []sdui.Item {
	t.Helper()
	source := sdui.Source{"type": src}
	for i := 0; i+1 < len(params); i += 2 {
		source[params[i].(string)] = params[i+1]
	}
	ans, err := SDUIItems(sdui.ItemsRequest{Source: source, Limit: 200, Viewer: viewer})
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	if _, ok := ui.yields[src]; !ok {
		t.Fatalf("%s isn't in sdui.json", src)
	}
	checkRows(t, ui, src, ans.Items)
	return ans.Items
}

func rowByID(items []sdui.Item, id string) *sdui.Item {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

var (
	adminViewer = sdui.Viewer{User: 1, ProfileKind: "adult", Admin: true, FormFactor: "tv", Platform: "tvos"}
	otherViewer = sdui.Viewer{User: 2, ProfileKind: "adult", FormFactor: "tv", Platform: "tvos"}
)

// Every command sdui.json declares runs one of WittyPlay's own routes, which integration.json
// passes on to it for administrators, with nothing that needs a password.
func TestStoreCommandsAreWittyPlaysRoutes(t *testing.T) {
	ui := readModuleUI(t)
	want := []string{opCoreInstall, opCoreRemove, opConsoleCore, opEmuInstall, opEmuRemove, opEmuCancel}
	for _, op := range want {
		if _, ok := ui.commands[op]; !ok {
			t.Errorf("sdui.json doesn't declare %s", op)
		}
	}
	for op, c := range ui.commands {
		rest, ok := strings.CutPrefix(c.Path, "/api/modules/wittyplay/")
		if !ok {
			t.Errorf("%s: %s isn't WittyPlay's", op, c.Path)
			continue
		}
		found := false
		for _, r := range ui.routes {
			pat := "^" + regexp.MustCompile(`\\\{[a-z]+\\\}`).ReplaceAllString(regexp.QuoteMeta(strings.TrimPrefix(r.Path, "/")), `[^/]+`) + "$"
			if regexp.MustCompile(pat).MatchString(strings.ReplaceAll(rest, "{item}", "x")) && slices.Contains(r.Methods, c.Method) {
				found = true
				if r.Auth != "admin" || r.Confirm {
					t.Errorf("%s: route %s is auth %q, confirm %v", op, r.Path, r.Auth, r.Confirm)
				}
			}
		}
		if !found {
			t.Errorf("%s: integration.json passes on no %s %s", op, c.Method, c.Path)
		}
	}
}

// The store is an administrator's: anyone else, a page for nobody and a profile kept from games
// get no rows.
func TestStoreSourcesAreForAdministrators(t *testing.T) {
	ui := readModuleUI(t)
	coresApp(t)
	for _, src := range []string{srcPlayStoreConsoles, srcPlayStoreEmus} {
		for name, v := range map[string]sdui.Viewer{
			"not an administrator": otherViewer,
			"nobody":               {ProfileKind: "adult", Admin: true},
			"kept from games":      {User: 1, ProfileKind: "adult", Admin: true, Cannot: []string{"games"}},
		} {
			if got := rows(t, ui, v, src); len(got) != 0 {
				t.Errorf("%s for %s: %d rows", src, name, len(got))
			}
			if got := rows(t, ui, v, src, "console", "nes"); len(got) != 0 {
				t.Errorf("%s about the NES for %s: %d rows", src, name, len(got))
			}
		}
	}
}

// The consoles' rows: one each, those with games first, saying whether it plays here and opening
// its own rows; then a row saying custom cores are added on a computer. A console's rows: the core
// it plays on as a choice of those installed, each core with what can be done with it, and its
// BIOS files. Each command, run as the row says, does what it says.
func TestStoreConsoleRows(t *testing.T) {
	ui := readModuleUI(t)
	a, h, ph := coresApp(t)
	setCardAnswer(a, stream.GPUProbe{Name: "A test card"})
	ph.counts = map[string]int{"ps1": 3, "nes": 12}
	putCores(t, a, "nestopia")

	all := rows(t, ui, adminViewer, srcPlayStoreConsoles)
	if len(all) < 20 || all[0].ID != "console-nes" || all[1].ID != "console-ps1" {
		t.Fatalf("the consoles with the most games first: %d rows, %v", len(all), all[:min(2, len(all))])
	}
	nes := all[0]
	if nes.Title == "" || nes.Trailing != "Plays here" || !strings.Contains(nes.Subtitle, "12 games") || !strings.Contains(nes.Subtitle, "Nestopia") {
		t.Errorf("the NES row: %+v", nes)
	}
	if nes.Action == nil || nes.Action.Type != "navigate" || !strings.HasPrefix(nes.Action.To, "/page/wittyplay-store-console?") || !strings.Contains(nes.Action.To, "item=nes") {
		t.Errorf("the NES row opens: %+v", nes.Action)
	}
	if ps1 := all[1]; ps1.Trailing == "Plays here" || ps1.Trailing == "" {
		t.Errorf("a console with no core: %+v", ps1)
	}
	last := all[len(all)-1]
	if !strings.HasPrefix(last.Title, "On a computer: ") || last.Action != nil || last.Control != nil {
		t.Errorf("custom cores, which need a password and a file, are a row that says where: %+v", last)
	}

	rs := rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "nes")
	pick := rowByID(rs, "plays-with")
	if pick == nil || pick.Control == nil || pick.Control.Type != "select" || pick.Action == nil || pick.Action.Op != opConsoleCore || pick.Action.Item != "nes" {
		t.Fatalf("the core it plays on: %+v", pick)
	}
	var opts []string
	for _, o := range pick.Control.Options {
		opts = append(opts, o.Value)
	}
	if !slices.Equal(opts, []string{"", "nestopia"}) || pick.Control.Value != "" || pick.Control.Disabled {
		t.Errorf("a choice of Automatic and the installed cores: %v, value %v", opts, pick.Control.Value)
	}
	if r := rowByID(rs, "core-nestopia"); r == nil || r.Trailing != "Installed" || !strings.Contains(r.Subtitle, "Licence: ") {
		t.Errorf("an installed core: %+v", r)
	}
	if rowByID(rs, "update-nestopia") == nil || rowByID(rs, "remove-nestopia") == nil || rowByID(rs, "install-nestopia") != nil {
		t.Errorf("an installed core is updated or removed, not installed")
	}
	fce := rowByID(rs, "core-fceumm")
	if fce == nil || fce.Trailing == "Installed" || !slices.Contains(fce.Badges, "Recommended") {
		t.Errorf("a core to install: %+v", fce)
	}
	install := rowByID(rs, "install-fceumm")
	if cores.CanDownload() {
		if install == nil || !strings.HasPrefix(install.Title, "Accept ") || !strings.Contains(install.Title, " and install ") || install.Action.Op != opCoreInstall || install.Action.Item != "fceumm" {
			t.Fatalf("installing names the licence it accepts: %+v", install)
		}
	}
	if r := rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "nope"); len(r) != 0 {
		t.Errorf("a console that isn't there: %v", r)
	}
	ps1 := rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "ps1")
	if p := rowByID(ps1, "plays-with"); p == nil || !p.Control.Disabled || p.Control.Why == "" {
		t.Errorf("with nothing installed there's nothing to choose, and it says why: %+v", p)
	}
	bios := 0
	for _, r := range ps1 {
		if strings.HasPrefix(r.ID, "bios-") && r.ID != "bios-how" {
			bios++
			if r.Action != nil || r.Trailing == "" || r.Subtitle == "" {
				t.Errorf("a BIOS file is text to read: %+v", r)
			}
		}
	}
	if bios == 0 || rowByID(ps1, "bios-how") == nil {
		t.Errorf("the PlayStation's BIOS files are listed: %d", bios)
	}

	// What the rows run, as an app would run them.
	old := installCore
	t.Cleanup(func() { installCore = old })
	installCore = func(_ context.Context, m *cores.Manager, id string) (*cores.Core, error) {
		putCores(t, a, id)
		return &cores.Core{ID: id, SHA256: "abc"}, nil
	}
	run := func(it *sdui.Item, params map[string]any) {
		t.Helper()
		act := *it.Action
		if params != nil {
			act.Params = params
		}
		r := ui.commands[act.Op].request(t, &act)
		r.Header.Set(service.HeaderUser, "1")
		r.Header.Set(service.HeaderKind, "adult")
		r.Header.Set(service.HeaderAdmin, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", r.Method, r.URL.Path, w.Code, w.Body)
		}
	}
	if install != nil {
		if has, known := coreBuildFor("fceumm", runtime.GOOS, runtime.GOARCH); !known || has {
			run(install, nil)
			if r := rowByID(rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "nes"), "core-fceumm"); r == nil || r.Trailing != "Installed" {
				t.Errorf("after installing: %+v", r)
			}
		}
	}
	run(pick, map[string]any{"value": "nestopia"}) // a select adds the value chosen
	if got := a.PreferredCores(context.Background())["nes"]; got != "nestopia" {
		t.Errorf("the choice wasn't saved: %q", got)
	}
	if p := rowByID(rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "nes"), "plays-with"); p.Control.Value != "nestopia" {
		t.Errorf("the choice shows: %v", p.Control.Value)
	}
	run(pick, map[string]any{"value": ""})
	run(rowByID(rs, "remove-nestopia"), nil)
	if r := rowByID(rows(t, ui, adminViewer, srcPlayStoreConsoles, "console", "nes"), "core-nestopia"); r == nil || r.Trailing == "Installed" {
		t.Errorf("after removing: %+v", r)
	}
}

// The standalone emulators: a row each, then what can be done, the licence named in the words of the
// row that installs; a download's progress at the row's end, with a way to stop it; one that's never
// downloaded is installed on a computer.
func TestEmulatorRows(t *testing.T) {
	ui := readModuleUI(t)
	entries := []rpc.CatalogEntry{
		{ID: "pcsx2", Name: "PCSX2", Systems: []string{"ps2"}, License: "GPL-3.0", Version: "2.2", Installable: true, DownloadBytes: 50 << 20,
			Bios: []rpc.BiosStatus{{Name: "scph39001.bin", Present: true}, {Name: "x.bin"}}},
		{ID: "dolphin", Name: "Dolphin", Systems: []string{"gc", "wii"}, License: "GPL-2.0-or-later", Version: "2509", Installable: true,
			Job: &rpc.InstallJob{ID: "dolphin", State: "running", Phase: emulators.PhaseDownloading, Got: 42, Total: 100}},
		{ID: "rpcs3", Name: "RPCS3", Systems: []string{"ps3"}, License: "GPL-2.0", Version: "0.0.40", Installable: true, UpdateAvailable: true,
			Installed: &rpc.InstalledJSON{ID: "rpcs3", Version: "0.0.39"}},
		{ID: "ryujinx", Name: "Ryujinx", Systems: []string{"switch"}, License: "MIT", Manual: &rpc.ManualSummary{Download: "https://example.org"},
			Why: "it's never downloaded"},
		{ID: "cemu", Name: "Cemu", Systems: []string{"wiiu"}, License: "MPL-2.0", Why: "no download for this platform"},
	}
	rs := emulatorRows(entries)
	checkRows(t, ui, srcPlayStoreEmus, rs)

	p := rowByID(rs, "emulator-pcsx2")
	if p == nil || p.Trailing != "Not installed" || !strings.Contains(p.Subtitle, "PS2") || !strings.Contains(p.Subtitle, "GPL-3.0") || !strings.Contains(p.Subtitle, "Firmware: 1 of 2 found") {
		t.Errorf("an emulator to install: %+v", p)
	}
	in := rowByID(rs, "install-pcsx2")
	if in == nil || in.Title != "Accept GPL-3.0 and install PCSX2" || in.Action.Op != opEmuInstall || in.Action.Item != "pcsx2" || in.Action.Params["license"] != "GPL-3.0" {
		t.Errorf("installing accepts the licence it names: %+v", in)
	}
	if d := rowByID(rs, "emulator-dolphin"); d == nil || d.Trailing != "42%" {
		t.Errorf("a download's progress: %+v", d)
	}
	if c := rowByID(rs, "cancel-dolphin"); c == nil || c.Action.Op != opEmuCancel || rowByID(rs, "install-dolphin") != nil {
		t.Errorf("a download can be stopped, and isn't started again: %+v", c)
	}
	if r := rowByID(rs, "emulator-rpcs3"); r == nil || r.Trailing != "Update to 0.0.40" {
		t.Errorf("an update: %+v", r)
	}
	if u := rowByID(rs, "update-rpcs3"); u == nil || u.Action.Params["license"] != "GPL-2.0" || !strings.Contains(u.Title, "Accept GPL-2.0") {
		t.Errorf("updating accepts the licence too: %+v", u)
	}
	if r := rowByID(rs, "remove-rpcs3"); r == nil || r.Action.Op != opEmuRemove || r.Action.Item != "rpcs3" {
		t.Errorf("an installed emulator is removed: %+v", r)
	}
	if r := rowByID(rs, "computer-emulator-ryujinx"); r == nil || r.Action != nil || !strings.HasPrefix(r.Title, "On a computer: ") {
		t.Errorf("one installed by hand: %+v", r)
	}
	if rowByID(rs, "install-ryujinx") != nil || rowByID(rs, "install-cemu") != nil || rowByID(rs, "remove-cemu") != nil {
		t.Error("nothing to install or remove where it can't be")
	}
	if r := rowByID(rs, "emulator-cemu"); r == nil || r.Trailing != "Can't install here" || !strings.Contains(r.Subtitle, "No download for this platform.") {
		t.Errorf("one that can't be installed says why: %+v", r)
	}

	// The install a row asks for is the one the backend takes: its licence, in the body.
	act := *in.Action
	r := ui.commands[opEmuInstall].request(t, &act)
	var body struct{ License string }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.License != "GPL-3.0" || r.URL.Path != "/docker/emulators/pcsx2/install" || r.Method != "POST" {
		t.Errorf("%s %s %+v %v", r.Method, r.URL.Path, body, err)
	}

	// Asked for by an administrator, the backend's own list.
	coresApp(t)
	got := rows(t, ui, adminViewer, srcPlayStoreEmus)
	if len(got) == 0 || !strings.HasPrefix(got[len(got)-1].Title, "On a computer: ") {
		t.Errorf("the backend's emulators: %d rows", len(got))
	}
}

// libraryHost is a server with three games libraries' worth of games: 1 holds every console
// (NES 2, SNES 1), 2 is kept for the SNES (2 games), 3 holds films. Game 13 is one sam may not see.
func libraryHost(t *testing.T) *host.Client {
	t.Helper()
	type game struct {
		id      int64
		lib     int64
		console string
		title   string
	}
	games := []game{{10, 1, "nes", "Moth Racer"}, {11, 1, "nes", "Air Lift"}, {12, 1, "snes", "Star Tide"}, {13, 2, "snes", "Grown-up game"}, {14, 2, "snes", "Bubble Bay"}}
	sees := func(user int64, g game) bool { return !(user == 3 && g.id == 13) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Witty-Host", "1")
		p := strings.TrimPrefix(r.URL.Path, host.Base)
		on, _ := strconv.ParseInt(r.Header.Get(host.HeaderOnUser), 10, 64)
		q := r.URL.Query()
		enc := json.NewEncoder(w)
		switch p {
		case "/libraries":
			_ = enc.Encode(map[string]any{"libraries": []host.Library{{ID: 1, Name: "Games", Kind: "games", Console: "auto"}, {ID: 2, Name: "SNES", Kind: "games", Console: "snes"}, {ID: 3, Name: "Films", Kind: "movies"}}})
		case "/items/counts":
			lib, _ := strconv.ParseInt(q.Get("library"), 10, 64)
			counts := map[string]int{}
			for _, g := range games {
				if g.lib == lib && sees(on, g) && q.Get("by") == "console" {
					counts[g.console]++
				}
			}
			_ = enc.Encode(map[string]any{"counts": counts})
		case "/items":
			lib, _ := strconv.ParseInt(q.Get("library"), 10, 64)
			if q.Get("full") != "1" || q.Get("kind") != "game" || on == 0 || (lib == 0 && q.Get("libraryKind") != "games") {
				w.WriteHeader(400)
				return
			}
			var out []map[string]any
			for _, g := range games {
				if (lib == 0 || g.lib == lib) && g.console == q.Get("facet.console") && sees(on, g) {
					out = append(out, map[string]any{"id": g.id, "kind": "game", "title": g.title, "console": g.console})
				}
			}
			slices.SortFunc(out, func(a, b map[string]any) int { return strings.Compare(a["title"].(string), b["title"].(string)) })
			off, _ := strconv.Atoi(q.Get("offset"))
			lim, _ := strconv.Atoi(q.Get("limit"))
			out = out[min(off, len(out)):]
			if lim > 0 && len(out) > lim {
				out = out[:lim]
			}
			_ = enc.Encode(map[string]any{"items": out})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return host.NewAt(srv.URL, "t", nil)
}

// A library's shelves: each console's games by title, as the person may see them, paged; and the
// consoles a library (or every games library) has games for, opening the library that holds them.
func TestLibrarySource(t *testing.T) {
	ui := readModuleUI(t)
	testApp(t, libraryHost(t))
	alex := sdui.Viewer{User: 2, ProfileKind: "adult", FormFactor: "tv"}
	sam := sdui.Viewer{User: 3, ProfileKind: "adult", FormFactor: "tv"}

	snes := rows(t, ui, alex, srcPlayLibrary, "library", "2", "console", "snes")
	if len(snes) != 2 || snes[0].Title != "Bubble Bay" || snes[0].Kind != "game" || snes[0].Route != "/game/14" || len(snes[0].Actions) == 0 {
		t.Fatalf("library 2's SNES shelf: %+v", snes)
	}
	if got := rows(t, ui, sam, srcPlayLibrary, "library", float64(2), "console", "snes"); len(got) != 1 {
		t.Errorf("only what sam may see: %+v", got)
	}
	if got := rows(t, ui, alex, srcPlayLibrary, "console", "snes"); len(got) != 3 {
		t.Errorf("every games library's SNES games: %d", len(got))
	}
	if got := rows(t, ui, alex, srcPlayLibrary, "library", "1", "console", "nosuch"); len(got) != 0 {
		t.Errorf("a console that isn't there: %+v", got)
	}
	ans, err := SDUIItems(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayLibrary, "library": "1", "console": "nes"}, Limit: 1, Viewer: alex})
	if err != nil || len(ans.Items) != 1 || !ans.More || ans.Items[0].Title != "Air Lift" {
		t.Errorf("a page of one, and more: %+v %v", ans, err)
	}
	ans, _ = SDUIItems(sdui.ItemsRequest{Source: sdui.Source{"type": srcPlayLibrary, "library": "1", "console": "nes"}, Offset: 1, Limit: 1, Viewer: alex})
	if len(ans.Items) != 1 || ans.More || ans.Items[0].Title != "Moth Racer" {
		t.Errorf("the next page: %+v", ans)
	}

	tiles := rows(t, ui, alex, srcPlayLibrary)
	if len(tiles) != 2 || tiles[0].ID != "nes" || tiles[1].ID != "snes" {
		t.Fatalf("the consoles with games, in the console list's order: %+v", tiles)
	}
	if tiles[0].Count != 2 || tiles[0].Route != "/library/1?tab=consoles" || tiles[0].Kind != "view" {
		t.Errorf("the NES: %+v", tiles[0])
	}
	if tiles[1].Count != 3 || tiles[1].Route != "/library/2?tab=consoles" {
		t.Errorf("the SNES opens the library kept for it: %+v", tiles[1])
	}
	if one := rows(t, ui, alex, srcPlayLibrary, "library", "1"); len(one) != 2 || one[1].Count != 1 || one[1].Route != "/library/1?tab=consoles" {
		t.Errorf("one library's consoles: %+v", one)
	}
	for name, v := range map[string]sdui.Viewer{"nobody": {ProfileKind: "adult"}, "kept from games": {User: 2, ProfileKind: "adult", Cannot: []string{"games"}}} {
		if got := rows(t, ui, v, srcPlayLibrary, "library", "1", "console", "nes"); len(got) != 0 {
			t.Errorf("%s: %d", name, len(got))
		}
	}
}

// The pages that draw these exist, with the sources and commands they use, and every WittyPlay
// page with a dock sends Settings to the page every app draws.
func TestTVPages(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "sdui.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Pages []struct {
			ID      string          `json:"id"`
			Kind    string          `json:"kind"`
			Regions json.RawMessage `json:"regions"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	for _, p := range file.Pages {
		pages[p.ID] = p.Kind
		if len(p.Regions) > 0 && !bytes.Contains(p.Regions, []byte(`"route": "/page/wittyplay-settings"`)) {
			t.Errorf("%s: the dock's Settings isn't the apps' settings page", p.ID)
		}
	}
	for id, kind := range map[string]string{"wittyplay-library": "library", "wittyplay-store": "custom", "wittyplay-store-console": "custom", "wittyplay-settings": "custom"} {
		if pages[id] != kind {
			t.Errorf("%s: %q", id, pages[id])
		}
	}
	for _, s := range []string{`"type": "wittyplay.library"`, `"type": "wittyplay.storeConsoles"`, `"type": "wittyplay.storeEmulators"`, `"to": "/page/wittyplay-store"`, `"op": "refresh"`} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("sdui.json has no %s", s)
		}
	}
}
