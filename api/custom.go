// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/appcontrol"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// Custom cores: an administrator's own libretro cores, and custom consoles to play
// with them (moved from the server's api_games_custom.go). The server's docs/GAMING.md has the
// reasoning. A core is native code that will run on this machine, so:
//
//   - only an adult administrator gets here (auth admin, and checked again here), and adding one
//     asks for the password again: the server checks it on a "confirm" route, and these refuse
//     unless it was given and right (Identity.Confirmed);
//   - it is test-loaded in a runner process, boxed in like a game, before anyone can
//     approve it, and always runs in one afterwards. This process never loads it;
//   - its SHA-256 is approved, and a file that no longer matches doesn't run again until
//     an administrator looks at it;
//   - the whole thing can be off, and is until someone turns it on. Turning it on takes
//     the machine's say first (the server's Hello, Machine["gamingCustomCores"], which a
//     lock on the machine turns off), and a lock beats everything.
//
// What happened is written in the server's audit log (host Audit), so it stays whatever
// happens to WittyPlay.

const (
	settingCustomCores = "gamingCustomCores"
	// lockCustomCores keeps custom cores off whatever the settings say, for whoever
	// runs the machine when that isn't the same person as this server's administrator.
	lockCustomCores = "WMS_GAMING_NO_CUSTOM_CORES"
	// allowCustomCores is the name of the switch that has to be set on the machine before
	// custom cores can be turned on at all: a core runs as a program that can read what the
	// server can, so a web session, even one with the password, can't decide that alone.
	allowCustomCores = "WMS_GAMING_CUSTOM_CORES"

	stageLife      = time.Hour
	stagesAtOnce   = 2
	maxKeptVersion = 3
	maxFormField   = 4 << 10
	uploadTime     = 30 * time.Minute
)

// The routes. Everything here is for administrators, and every route that changes anything other
// than turning this off or taking something away checks it is turned on.
func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		on := customOn
		mux.Handle("GET /gaming/custom", w(adminOnly(handleCustomGet)))
		mux.Handle("PUT /gaming/custom/enabled", w(adminOnly(handleCustomEnable)))
		mux.Handle("POST /gaming/custom/staged/upload", w(on(handleStageUpload)))
		mux.Handle("POST /gaming/custom/staged/url", w(on(handleStageURL)))
		mux.Handle("POST /gaming/custom/staged/placed", w(on(handleStagePlaced)))
		mux.Handle("POST /gaming/custom/staged/{token}/approve", w(on(handleStageApprove)))
		mux.Handle("DELETE /gaming/custom/staged/{token}", w(adminOnly(handleStageCancel)))
		mux.Handle("PUT /gaming/custom/cores/{id}", w(on(handleCustomCorePut)))
		mux.Handle("DELETE /gaming/custom/cores/{id}", w(adminOnly(handleCustomCoreDelete)))
		mux.Handle("POST /gaming/custom/cores/{id}/reapprove", w(on(handleCustomReapprove)))
		mux.Handle("POST /gaming/custom/cores/{id}/rollback", w(on(handleCustomRollback)))
		mux.Handle("POST /gaming/custom/cores/{id}/files", w(on(handleCustomFiles)))
		mux.Handle("POST /gaming/custom/consoles", w(on(handleCustomConsolePost)))
		mux.Handle("PUT /gaming/custom/consoles/{id}", w(on(handleCustomConsolePut)))
		mux.Handle("DELETE /gaming/custom/consoles/{id}", w(adminOnly(handleCustomConsoleDelete)))
	})
	// Custom consoles are known from the start: the scanner asks WittyPlay's table about them.
	onEvent("server.started", func(a *App, _ json.RawMessage) { a.publishCustomConsoles(context.Background()) })
}

type coreRoute = func(*App, http.ResponseWriter, *http.Request)

// adminOnly is isAdmin, around a route.
func adminOnly(h coreRoute) coreRoute {
	return func(a *App, w http.ResponseWriter, r *http.Request) {
		if isAdmin(w, r) {
			h(a, w, r)
		}
	}
}

// customService is what the routes keep between requests.
type customService struct {
	store *cores.CustomStore

	mu        sync.Mutex
	staged    map[string]*stagedCore
	slots     chan struct{}
	verified  map[string]verifyMemo
	probeTime time.Duration     // 0 for stream.ProbeTimeout; tests shorten it
	noticed   map[string]string // a changed file already in the audit log, by core
}

type stagedCore struct {
	s        *cores.Staged
	report   CoreReport
	owner    int64
	replaces string
}

type verifyMemo struct {
	size  int64
	mod   time.Time
	sum   string
	state cores.CoreFileState
}

func newCustomService(dir, system string, placed []string, client *http.Client) *customService {
	st := &cores.CustomStore{Dir: dir, System: system, Placed: placed, Client: client}
	st.ClearStaging()
	return &customService{store: st, staged: map[string]*stagedCore{}, slots: make(chan struct{}, stagesAtOnce), verified: map[string]verifyMemo{}, noticed: map[string]string{}}
}

// ---- whether this is on ----

func customLocked() bool { return os.Getenv(lockCustomCores) == "1" }

// customAllowed: the machine's owner has said custom cores may be used here, and nothing on the
// machine locks them off. The server says so in its Hello; its answer is kept for a minute.
func (a *App) customAllowed(ctx context.Context) bool {
	e := a.eng()
	e.machineMu.Lock()
	defer e.machineMu.Unlock()
	if e.machine == nil || time.Since(e.machineAt) > time.Minute {
		h, err := a.Host.Hello(ctx)
		if err != nil {
			return e.machine["gamingCustomCores"] // what it said last, if anything
		}
		e.machine, e.machineAt = h.Machine, time.Now()
		if e.machine == nil {
			e.machine = map[string]bool{}
		}
	}
	return e.machine["gamingCustomCores"]
}

func (a *App) customCoresOn(ctx context.Context) bool {
	if _, sandboxed := stream.SandboxStrength(); !sandboxed || customLocked() || !a.customAllowed(ctx) {
		return false
	}
	on := false
	_, _ = a.Config(ctx, settingCustomCores, &on)
	return on
}

// customOn lets a request through from an adult administrator while custom cores are on. Kids
// and guest profiles are never administrators; this says so again, so a change elsewhere can't
// quietly open this up.
func customOn(h coreRoute) coreRoute {
	return func(a *App, w http.ResponseWriter, r *http.Request) {
		if !isAdmin(w, r) {
			return
		}
		if k := who(r).Kind; k != "adult" && k != "" {
			writeErr(w, 403, "admin only")
			return
		}
		if !a.customCoresOn(r.Context()) {
			writeErr(w, 409, "Custom cores are turned off on this server. Turn them on in Settings, Consoles & cores.")
			return
		}
		h(a, w, r)
	}
}

// customConfirmed: the request carried the person's password and the server found it right
// (a "confirm" route), as checkOwnPassword was. A refusal is written down.
func (a *App) customConfirmed(w http.ResponseWriter, r *http.Request, core string) bool {
	if who(r).Confirmed {
		return true
	}
	a.auditCore(r.Context(), host.For(r), "password-refused", core, r.Method+" "+r.URL.Path)
	writeErr(w, 403, "the current password is not right")
	return false
}

// PUT /gaming/custom/enabled {enabled}, with the password (confirm). Turning this on takes the
// password; turning it off, which only ever makes things safer, doesn't.
func handleCustomEnable(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled bool `json:"enabled"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	if b.Enabled {
		if customLocked() {
			writeErr(w, 403, "Whoever runs this machine has kept custom cores off with "+lockCustomCores+".")
			return
		}
		if !a.customAllowed(ctx) {
			writeErr(w, 403, "Custom cores have to be allowed on the machine first: start the server with "+allowCustomCores+"=1, or gamingCustomCores in its config file.")
			return
		}
		if why, sandboxed := stream.SandboxStrength(); !sandboxed {
			writeErr(w, 403, "Custom cores can't be used on this system: the only thing boxing a core in here would be "+why+".")
			return
		}
		if !a.customConfirmed(w, r, "") {
			return
		}
	}
	action := "turned-off"
	if b.Enabled {
		action = "turned-on"
	}
	if err := a.SetConfig(ctx, settingCustomCores, b.Enabled); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.auditCore(ctx, host.For(r), action, "", "")
	a.publishCustomConsoles(ctx)
	writeJSON(w, 200, map[string]any{"enabled": b.Enabled})
}

// ---- the audit log ----

// auditCore writes down something that happened to a custom core, in the server's audit log, and
// also logs it here, so it reaches the log files whatever happens to either. Written with its own
// context: a request that has gone away still happened.
func (a *App) auditCore(ctx context.Context, on host.On, action, core, detail string) {
	if len(detail) > 800 {
		detail = detail[:800]
	}
	bg := context.WithoutCancel(ctx)
	if err := a.Host.Audit(bg, on, action, core, detail); err != nil {
		a.Log.WarnContext(bg, "could not write an audit row", "cat", "games", "err", err)
	}
	a.Log.InfoContext(bg, "audit record", "cat", "games", "kind", "custom core", "action", action, "by", on.User, "core", core, "detail", detail)
}

// userName is an account's name, for "added by"; "" when the server doesn't say.
func (a *App) userName(ctx context.Context, id int64) string {
	users, err := a.Host.Users(ctx)
	if err != nil {
		return ""
	}
	for _, u := range users {
		if u.ID == id {
			return u.Name
		}
	}
	return ""
}

// ---- what's stored about a core ----

// CoreReport is what a test-load and the file's own headers said. Everything in it
// came from the file, so it's shown as what the core says, never as fact.
type CoreReport struct {
	ID            string           `json:"id"` // the name this server would use
	File          string           `json:"file"`
	Size          int64            `json:"size"`
	SHA256        string           `json:"sha256"`
	Binary        cores.Binary     `json:"binary"`
	Arch          string           `json:"arch"`
	Name          string           `json:"name"`
	Version       string           `json:"version"`
	Extensions    []string         `json:"extensions"`
	NeedFullPath  bool             `json:"needFullPath,omitempty"`
	BlockExtract  bool             `json:"blockExtract,omitempty"`
	NoGame        bool             `json:"noGame,omitempty"`
	Options       int              `json:"options,omitempty"`
	NeedsGPU      string           `json:"needsGpu"` // yes | maybe | no
	GPUWhy        string           `json:"gpuWhy,omitempty"`
	BIOS          []cores.Firmware `json:"bios"`
	BIOSKnown     bool             `json:"biosKnown"` // an info file said which, so its absence means something
	License       string           `json:"license"`
	LicenseSource string           `json:"licenseSource,omitempty"` // info | table
	NonCommercial bool             `json:"nonCommercial,omitempty"`
	HasInfo       bool             `json:"hasInfo,omitempty"`
	System        string           `json:"system,omitempty"` // the system its info file names
	Notes         string           `json:"notes,omitempty"`
	Assets        int              `json:"assets,omitempty"`
	SystemFiles   int              `json:"systemFiles,omitempty"`
	Ignored       []string         `json:"ignored,omitempty"`
	SandboxNote   string           `json:"sandboxNote,omitempty"`
	Warnings      []string         `json:"warnings"`
	Consoles      []string         `json:"consoles"` // consoles its extensions say it may be for
	Source        string           `json:"source"`
	SourceURL     string           `json:"sourceUrl,omitempty"`
}

// CustomCore is a core somebody approved, as the settings page sees it.
type CustomCore struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Version       string              `json:"version"`
	SHA256        string              `json:"sha256"`
	Size          int64               `json:"size"`
	License       string              `json:"license"`
	LicenseSource string              `json:"licenseSource,omitempty"` // info | table | admin
	NonCommercial bool                `json:"nonCommercial,omitempty"`
	Consoles      []string            `json:"consoles"`
	DefaultFor    []string            `json:"defaultFor"`
	Note          string              `json:"note,omitempty"`
	Source        string              `json:"source"`
	SourceURL     string              `json:"sourceUrl,omitempty"`
	AddedBy       string              `json:"addedBy"`
	AddedAt       int64               `json:"addedAt"`
	UpdatedAt     int64               `json:"updatedAt"`
	Report        CoreReport          `json:"report"`
	State         cores.CoreFileState `json:"state"`
	Versions      []CoreVersion       `json:"versions"`
	Plays         CorePlays           `json:"plays"`
	Running       int                 `json:"running"`

	systemFiles []string
}

// CoreVersion is a version kept for going back to.
type CoreVersion struct {
	SHA256  string `json:"sha256"`
	Version string `json:"version"`
	AddedAt int64  `json:"addedAt"`
	Current bool   `json:"current"`
}

// CorePlays is how a core has really gone, across every game played with it.
type CorePlays struct {
	Starts    int     `json:"starts"`
	Failures  int     `json:"failures"`
	Seconds   float64 `json:"seconds"`
	LastError string  `json:"lastError,omitempty"`
	LastAt    int64   `json:"lastAt,omitempty"`
}

const customCoreCols = `id, name, version, sha256, size, license, license_source, non_commercial, consoles, note, source, source_url, added_by, added_at, updated_at, report, system_files`

func scanCustomCore(row interface{ Scan(...any) error }) (*CustomCore, error) {
	var c CustomCore
	var consoles, report, system string
	var nc int
	if err := row.Scan(&c.ID, &c.Name, &c.Version, &c.SHA256, &c.Size, &c.License, &c.LicenseSource, &nc, &consoles, &c.Note, &c.Source, &c.SourceURL, &c.AddedBy, &c.AddedAt, &c.UpdatedAt, &report, &system); err != nil {
		return nil, err
	}
	c.NonCommercial = nc != 0
	_ = json.Unmarshal([]byte(consoles), &c.Consoles)
	_ = json.Unmarshal([]byte(report), &c.Report)
	_ = json.Unmarshal([]byte(system), &c.systemFiles)
	if c.Consoles == nil {
		c.Consoles = []string{}
	}
	return &c, nil
}

func (a *App) customCoreRows(ctx context.Context) []*CustomCore {
	rows, err := a.DB.QueryContext(ctx, `SELECT `+customCoreCols+` FROM custom_cores ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*CustomCore
	for rows.Next() {
		if c, err := scanCustomCore(rows); err == nil {
			out = append(out, c)
		}
	}
	return out
}

func (a *App) customCoreRow(ctx context.Context, id string) (*CustomCore, error) {
	return scanCustomCore(a.DB.QueryRowContext(ctx, `SELECT `+customCoreCols+` FROM custom_cores WHERE id = ?`, id))
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func (a *App) saveCustomCore(ctx context.Context, c *CustomCore) error {
	if c.Consoles == nil {
		c.Consoles = []string{}
	}
	nc := 0
	if c.NonCommercial {
		nc = 1
	}
	c.UpdatedAt = time.Now().Unix()
	_, err := a.DB.ExecContext(ctx, `INSERT INTO custom_cores(`+customCoreCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, version=excluded.version, sha256=excluded.sha256, size=excluded.size, license=excluded.license,
		license_source=excluded.license_source, non_commercial=excluded.non_commercial, consoles=excluded.consoles, note=excluded.note, source=excluded.source,
		source_url=excluded.source_url, added_by=excluded.added_by, updated_at=excluded.updated_at, report=excluded.report, system_files=excluded.system_files`,
		c.ID, c.Name, c.Version, c.SHA256, c.Size, c.License, c.LicenseSource, nc, toJSON(c.Consoles), c.Note, c.Source, c.SourceURL, c.AddedBy, c.AddedAt, c.UpdatedAt, toJSON(c.Report), toJSON(c.systemFiles))
	return err
}

// customCoreList is what the core manager adds to its list: approved cores, when
// they're on. A core file that has changed stays listed, so the refusal can say why.
func (a *App) customCoreList() []cores.Core {
	ctx := context.Background()
	if !a.customCoresOn(ctx) {
		return nil
	}
	enginesMu.Lock()
	e := engines[a]
	enginesMu.Unlock()
	if e == nil {
		return nil
	}
	store := e.custom.store
	var out []cores.Core
	for _, c := range a.customCoreRows(ctx) {
		out = append(out, cores.Core{ID: c.ID, Name: c.Name, License: c.License, LicenseSource: c.LicenseSource, Source: cores.SourceCustom, SHA256: c.SHA256,
			NonCommercial: c.NonCommercial, Version: c.Version, Consoles: c.Consoles, Installed: c.AddedAt,
			Path: store.CorePath(c.ID, c.SHA256), Assets: store.AssetsPath(c.ID, c.SHA256), WantsGPU: c.Report.NeedsGPU == "yes"})
	}
	return out
}

// customState is a core's file against what was approved. The settings page asks
// often, so it only reads the whole file when the file's size or time has moved. Play
// always reads it all (checkCustomCore).
func (a *App) customState(ctx context.Context, c *CustomCore) cores.CoreFileState {
	cs := a.eng().custom
	p := cs.store.CorePath(c.ID, c.SHA256)
	st, err := os.Lstat(p)
	if err != nil {
		return cores.CoreFileState{State: "missing"}
	}
	cs.mu.Lock()
	memo, ok := cs.verified[c.ID]
	cs.mu.Unlock()
	if ok && memo.sum == c.SHA256 && memo.size == st.Size() && memo.mod.Equal(st.ModTime()) {
		return memo.state
	}
	state := cs.store.Verify(c.ID, c.SHA256)
	cs.mu.Lock()
	cs.verified[c.ID] = verifyMemo{size: st.Size(), mod: st.ModTime(), sum: c.SHA256, state: state}
	cs.mu.Unlock()
	a.noticeChange(ctx, c, state)
	return state
}

// noticeChange writes a changed file in the audit log, once for each change.
func (a *App) noticeChange(ctx context.Context, c *CustomCore, state cores.CoreFileState) {
	if state.State == "ok" {
		return
	}
	cs := a.eng().custom
	key := state.State + ":" + state.Found
	cs.mu.Lock()
	seen := cs.noticed[c.ID] == key
	cs.noticed[c.ID] = key
	cs.mu.Unlock()
	if !seen {
		a.auditCore(ctx, host.On{}, "file-"+state.State, c.ID, "approved "+c.SHA256+", found "+state.Found)
	}
}

// CheckCustomCore is the last thing between an approved file and a game starting. It
// reads the whole file, every time: on a low-end machine that's a moment against a
// game's load, and it's the promise this feature makes.
func (a *App) CheckCustomCore(ctx context.Context, core *cores.Core) error {
	c, err := a.customCoreRow(ctx, core.ID)
	if err != nil {
		return errors.New("this core isn't approved")
	}
	state := a.eng().custom.store.Verify(c.ID, c.SHA256)
	if state.State == "ok" {
		return nil
	}
	a.noticeChange(ctx, c, state)
	return fmt.Errorf("The core %s has changed since it was approved, so it won't start. An administrator can look at it in Settings, Consoles & cores.", c.Name)
}

// ---- which core plays what ----

// CustomFor says whether an approved custom core has been assigned to a console. That
// is an administrator's choice to try it, which is what lifts a console's "not playable
// yet": they know more about their core than our table does.
func (a *App) CustomFor(ctx context.Context, console string) bool {
	if !a.customCoresOn(ctx) {
		return false
	}
	for _, c := range a.customCoreRows(ctx) {
		if slices.Contains(c.Consoles, console) {
			return true
		}
	}
	return false
}

// ---- how play went ----

func (a *App) corePlayTotals(ctx context.Context) map[string]CorePlays {
	out := map[string]CorePlays{}
	rows, err := a.DB.QueryContext(ctx, `SELECT core_id, SUM(starts), SUM(failures), SUM(seconds), MAX(last_at) FROM game_core_plays GROUP BY core_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p CorePlays
		if rows.Scan(&id, &p.Starts, &p.Failures, &p.Seconds, &p.LastAt) == nil {
			out[id] = p
		}
	}
	return out
}

// ---- GET /gaming/custom ----

// CustomConsoleView is a custom console as it can be edited or exported.
type CustomConsoleView struct {
	wittyplay.CustomConsole
	Games     int `json:"games"`
	Libraries int `json:"libraries"`
}

// CustomStatus is everything the settings page needs for this section.
type CustomStatus struct {
	Enabled     bool                `json:"enabled"`
	Locked      bool                `json:"locked,omitempty"`
	CanEnable   bool                `json:"canEnable"`
	NeedsSwitch bool                `json:"needsSwitch"` // not allowed on the machine yet
	Sandbox     string              `json:"sandbox"`
	HaveRunner  bool                `json:"haveRunner"`
	Platform    string              `json:"platform"`
	LibraryExt  string              `json:"libraryExt"`
	MaxUploadMB int64               `json:"maxUploadMB"`
	CoresDir    string              `json:"coresDir"`
	SystemDir   string              `json:"systemDir"`
	Cores       []*CustomCore       `json:"cores"`
	Consoles    []CustomConsoleView `json:"consoles"`
	Placed      []cores.PlacedFile  `json:"placed"`
	// The audit log is the server's now (host Audit), and shown with its own.
	PadChoices []CorePickOption `json:"padChoices"`
	OtherCores []CorePickOption `json:"otherCores"` // cores already here a custom console could use
}

// CorePickOption is something to choose from a list.
type CorePickOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func handleCustomGet(a *App, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mgr := a.Cores()
	why, sandboxed := stream.SandboxStrength()
	allowed := a.customAllowed(ctx)
	out := CustomStatus{
		Enabled: a.customCoresOn(ctx), Locked: customLocked(), NeedsSwitch: !allowed, CanEnable: sandboxed && !customLocked() && allowed, Sandbox: why, HaveRunner: ownRunner() != "",
		Platform: cores.Binary{OS: runtime.GOOS, Arch: runtime.GOARCH}.Describe(), LibraryExt: cores.LibraryExt(), MaxUploadMB: cores.DefaultLimits.Package >> 20,
		CoresDir: mgr.Dir, SystemDir: a.SystemDir(), Cores: []*CustomCore{}, Consoles: []CustomConsoleView{}, Placed: []cores.PlacedFile{},
	}
	if out.Enabled {
		// The page says to put files here, so it has to exist on a new server.
		_ = os.MkdirAll(mgr.Dir, 0o755)
	}
	totals := a.corePlayTotals(ctx)
	prefs := a.PreferredCores(ctx)
	for _, c := range a.customCoreRows(ctx) {
		c.State = a.customState(ctx, c)
		c.Versions = a.coreVersions(ctx, c)
		c.Plays = totals[c.ID]
		c.Running = rpc.Mgr.UsingCore(a.eng().custom.store.CorePath(c.ID, c.SHA256))
		c.DefaultFor = []string{}
		for console, id := range prefs {
			if id == c.ID {
				c.DefaultFor = append(c.DefaultFor, console)
			}
		}
		sort.Strings(c.DefaultFor)
		out.Cores = append(out.Cores, c)
	}
	out.Consoles = a.customConsoleViews(ctx, host.For(r))
	if out.Enabled {
		out.Placed = a.placedFiles()
	}
	for _, c := range wittyplay.Consoles {
		if !c.Hidden {
			out.PadChoices = append(out.PadChoices, CorePickOption{c.ID, c.Name})
			for _, core := range c.Cores {
				if !slices.ContainsFunc(out.OtherCores, func(o CorePickOption) bool { return o.ID == core.ID }) {
					out.OtherCores = append(out.OtherCores, CorePickOption{core.ID, core.Name})
				}
			}
		}
	}
	writeJSON(w, 200, out)
}

// placedFiles is what's waiting in the cores folders. A core this server installed
// itself isn't waiting for anything.
func (a *App) placedFiles() []cores.PlacedFile {
	installed := map[string]bool{}
	for _, c := range a.Cores().List() {
		if c.SHA256 != "" && c.Source != cores.SourceCustom {
			installed[filepath.Base(c.Path)] = true
		}
	}
	out := []cores.PlacedFile{}
	for _, p := range a.eng().custom.store.ListPlaced() {
		if !(p.Ours && installed[p.Name]) {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) coreVersions(ctx context.Context, c *CustomCore) []CoreVersion {
	out := []CoreVersion{}
	rows, err := a.DB.QueryContext(ctx, `SELECT sha256, version, added_at FROM custom_core_versions WHERE core_id = ? ORDER BY added_at DESC, rowid DESC`, c.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var v CoreVersion
		if rows.Scan(&v.SHA256, &v.Version, &v.AddedAt) == nil {
			v.Current = v.SHA256 == c.SHA256
			out = append(out, v)
		}
	}
	return out
}

// ---- adding a core: staging ----

func (cs *customService) takeSlot() bool {
	select {
	case cs.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (cs *customService) freeSlot() { <-cs.slots }

const stageBusy = "Another core is being tested right now. Try again when it's finished."

// stageError answers for a file that was refused, and writes it down.
func (a *App) stageError(w http.ResponseWriter, r *http.Request, err error, name string) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		a.auditCore(r.Context(), host.For(r), "add-refused", name, "too large")
		writeErr(w, 413, "This file is larger than a core may be.")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	a.auditCore(r.Context(), host.For(r), "add-refused", name, err.Error())
	writeErr(w, 422, sentence(err.Error()))
}

// sentence makes a message from the file checks read as one: they're written to be
// joined onto others, and this one stands alone on the page.
func sentence(s string) string {
	if s == "" {
		return s
	}
	s = strings.ToUpper(s[:1]) + s[1:]
	if !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

// finishStage is what every route ends with: test-load the file, build the report, and
// hold it for approval.
func (a *App) finishStage(w http.ResponseWriter, r *http.Request, st *cores.Staged, replaces, expect string) {
	ctx := r.Context()
	cs := a.eng().custom
	u := who(r)
	if expect = strings.ToLower(strings.TrimSpace(expect)); expect != "" && expect != st.SHA256 && expect != st.DownloadSHA {
		cs.store.Discard(st)
		a.auditCore(ctx, host.For(r), "add-refused", st.OrigName, "not the SHA-256 that was expected: "+st.SHA256)
		writeErr(w, 422, "This isn't the file you expected: its SHA-256 is "+st.SHA256+".")
		return
	}
	if replaces != "" {
		if _, err := a.customCoreRow(ctx, replaces); err != nil {
			cs.store.Discard(st)
			writeErr(w, 404, "the core being replaced isn't here")
			return
		}
	}
	res, err := a.testLoad(ctx, st.Library, filepath.Join(st.Dir, "box"))
	if err != nil {
		cs.store.Discard(st)
		var pe *stream.ProbeError
		kind := "refused"
		if errors.As(err, &pe) {
			kind = pe.Kind
		}
		a.auditCore(ctx, host.For(r), "test-load-failed", st.OrigName, kind+": "+err.Error()+" sha256 "+st.SHA256)
		writeJSON(w, 422, map[string]any{"error": err.Error(), "kind": kind, "sha256": st.SHA256})
		return
	}
	report := a.buildReport(ctx, st, res, replaces)
	cs.mu.Lock()
	for token, old := range cs.staged {
		if time.Since(old.s.Created) > stageLife {
			cs.store.Discard(old.s)
			delete(cs.staged, token)
		}
	}
	cs.staged[st.Token] = &stagedCore{s: st, report: report, owner: u.User, replaces: replaces}
	cs.mu.Unlock()
	a.auditCore(ctx, host.For(r), "test-load-passed", report.ID, report.Name+" "+report.Version+", sha256 "+st.SHA256)
	writeJSON(w, 201, map[string]any{"token": st.Token, "report": report, "replaces": replaces})
}

// testLoad runs a core file in the sandbox. box is a folder for it to write in, and
// the only one it can.
func (a *App) testLoad(ctx context.Context, core, box string) (*stream.ProbeResult, error) {
	res, err := stream.Probe(ctx, stream.ProbeOptions{Runner: ownRunner(), Core: core, SystemDir: a.SystemDir(), WorkDir: box, Timeout: a.eng().custom.probeTime, Log: a.Log})
	if err != nil {
		return nil, err
	}
	res.Probe = cores.CleanProbe(res.Probe)
	return res, nil
}

// buildReport brings together what the file, its info file and its test-load say.
func (a *App) buildReport(ctx context.Context, st *cores.Staged, res *stream.ProbeResult, replaces string) CoreReport {
	r := CoreReport{
		File: cores.CleanText(st.OrigName, 200), Size: st.Size, SHA256: st.SHA256, Binary: st.Binary, Arch: st.Binary.Describe(), Name: res.Name, Version: res.Version,
		Extensions: res.Extensions, NeedFullPath: res.NeedFullPath, BlockExtract: res.BlockExtract, NoGame: res.NoGame, Options: res.Options, BIOS: []cores.Firmware{},
		HasInfo: st.HasInfo, Assets: st.Assets, SystemFiles: st.System, Ignored: st.Ignored, SandboxNote: res.SandboxNote, Warnings: []string{}, Source: st.Source, SourceURL: st.SourceURL,
	}
	if r.Name == "" {
		r.Warnings = append(r.Warnings, "It loaded, but doesn't give a name for itself.")
		r.Name = strings.TrimSuffix(r.File, filepath.Ext(r.File))
	}
	r.ID = cores.SuggestID(st.OrigName, res.Name)
	if replaces != "" {
		r.ID = replaces
	}
	r.NeedsGPU = "no"
	switch {
	case res.HWRender:
		r.NeedsGPU, r.GPUWhy = "yes", "It asked for a graphics card while loading."
	case st.Info != nil && st.Info.HWRender:
		r.NeedsGPU, r.GPUWhy = "yes", "Its info file says it wants a graphics card."
	case st.GPUHint:
		r.NeedsGPU, r.GPUWhy = "maybe", "It has graphics card code, though it may only use it when one is there. This can only be known once a game is running."
	}
	if r.NeedsGPU == "yes" {
		r.Warnings = append(r.Warnings, gpuNote)
	}
	if st.Info != nil {
		r.BIOSKnown, r.System, r.Notes = true, st.Info.System, st.Info.Notes
		for _, f := range st.Info.Firmware {
			native := filepath.FromSlash(f.File)
			if filepath.IsLocal(native) {
				if _, err := os.Lstat(filepath.Join(a.SystemDir(), native)); err == nil {
					f.Present = true
				}
			}
			r.BIOS = append(r.BIOS, f)
		}
		if len(st.Info.Extensions) > 0 && len(res.Extensions) > 0 && len(intersect(st.Info.Extensions, res.Extensions)) == 0 {
			r.Warnings = append(r.Warnings, "Its info file doesn't seem to belong to it: the file types it lists aren't the ones the core reports.")
		}
		if st.Info.CoreName != "" && res.Name != "" && !strings.EqualFold(st.Info.CoreName, res.Name) && !looselySame(st.Info.CoreName, res.Name) {
			r.Warnings = append(r.Warnings, fmt.Sprintf("Its info file says it's for %q, and the core says it's %q.", st.Info.CoreName, res.Name))
		}
	}
	if len(res.Extensions) == 0 {
		r.Warnings = append(r.Warnings, "It doesn't say which files it opens, so it can't be matched to a console for you.")
	}
	if len(st.Ignored) > 0 {
		r.Warnings = append(r.Warnings, "Some files in the package aren't used: only the core, its .info file, and assets/ and system/ folders are unpacked.")
	}
	r.License, r.LicenseSource, r.NonCommercial = coreLicense(r.ID, st.OrigName, res.Name, st.Info, "")
	r.Consoles = suggestConsoles(res.Extensions, testingGames())
	return r
}

// gpuNote says what a core asking for a graphics card can expect. It stays in one place
// because what the runner can do is what decides it.
const gpuNote = "It asked for a graphics card. It draws on the server's graphics card if the server has one; on a server without one, it may fall back to software or may not start."

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

var notWord = regexp.MustCompile(`[^a-z0-9]+`)

func looselySame(a, b string) bool {
	x, y := notWord.ReplaceAllString(strings.ToLower(a), ""), notWord.ReplaceAllString(strings.ToLower(b), "")
	return strings.Contains(x, y) || strings.Contains(y, x)
}

// coreLicense decides what to say about a core's licence: what the administrator
// typed, else what its info file says, else what our table says about the name. It's
// never more than a claim, so the page says where it came from. Non-commercial is said
// if anything at all points that way, because that one matters most when it's wrong.
func coreLicense(id, file, reported string, info *cores.Info, typed string) (license, source string, nonCommercial bool) {
	base := strings.TrimSuffix(id, "-custom")
	stem := strings.TrimSuffix(strings.TrimSuffix(file, filepath.Ext(file)), "_libretro")
	switch {
	case typed != "":
		license, source = typed, "admin"
	case info != nil && info.License != "":
		license, source = info.License, "info"
	default:
		if known, ok := wittyplay.CoreLicense(base); ok {
			license, source = known.License, "table"
		}
	}
	nonCommercial = wittyplay.IsNonCommercial(base) || wittyplay.IsNonCommercial(stem) || wittyplay.NonCommercialName(reported) ||
		wittyplay.NonCommercialName(stem) || wittyplay.NonCommercialLicense(license)
	if nonCommercial && license == "" {
		license, source = "Non-commercial", "table"
	}
	return license, source, nonCommercial
}

// suggestConsoles finds the consoles only this core's file types point to.
func suggestConsoles(exts []string, testing bool) []string {
	out := []string{}
	all := wittyplay.All()
	for i := range all {
		c := &all[i]
		if c.Hidden && !testing {
			continue
		}
		for _, e := range exts {
			if slices.Contains(c.Extensions, "."+e) {
				out = append(out, c.ID)
				break
			}
		}
	}
	return out
}

// readFormValue reads one small field of a multipart form.
func readFormValue(part *multipart.Part) (string, error) {
	b, err := io.ReadAll(io.LimitReader(part, maxFormField+1))
	if err != nil {
		return "", err
	}
	if len(b) > maxFormField {
		return "", errors.New("a form field is too long")
	}
	return string(b), nil
}

// rawFileName is the name a client sent. part.FileName tidies away any folders, and
// someone sending a name with folders in it is something this code wants to see.
func rawFileName(part *multipart.Part) string {
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if err != nil {
		return ""
	}
	return params["filename"]
}

// readUpload checks the password, which has to come first so nothing is read from anyone who
// doesn't have it (the server checked it, from a header, before passing the request on), and
// returns the fields before the file and the file. The server limits the body too (limits).
func (a *App) readUpload(w http.ResponseWriter, r *http.Request, core string) (fields map[string]string, file *multipart.Part, ok bool) {
	if !a.customConfirmed(w, r, core) {
		return nil, nil, false
	}
	limits := cores.DefaultLimits
	if st := a.eng().custom.store.Limits; st.Package > 0 {
		limits = st
	}
	r.Body = http.MaxBytesReader(w, r.Body, limits.Package+1<<20)
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadTime))
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected a multipart upload")
		return nil, nil, false
	}
	fields = map[string]string{}
	for {
		part, err := mr.NextPart()
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				a.stageError(w, r, err, core)
			} else {
				writeErr(w, 400, "the form has no file in it")
			}
			return nil, nil, false
		}
		if part.FormName() == "file" {
			return fields, part, true
		}
		v, err := readFormValue(part)
		part.Close()
		if err != nil {
			writeErr(w, 400, err.Error())
			return nil, nil, false
		}
		fields[part.FormName()] = v
	}
}

// POST /gaming/custom/staged/upload, with the password (confirm): multipart, optional replaces
// and sha256, then file.
func handleStageUpload(a *App, w http.ResponseWriter, r *http.Request) {
	cs := a.eng().custom
	if !cs.takeSlot() {
		writeErr(w, 429, stageBusy)
		return
	}
	defer cs.freeSlot()
	fields, part, ok := a.readUpload(w, r, "")
	if !ok {
		return
	}
	defer part.Close()
	name := rawFileName(part)
	st, err := cs.store.StageReader(part, name)
	if err != nil {
		a.stageError(w, r, err, cores.CleanText(name, 120))
		return
	}
	a.finishStage(w, r, st, fields["replaces"], fields["sha256"])
}

// POST /gaming/custom/staged/url {url, sha256?, replaces?}, with the password (confirm). The
// address is fetched only on the internet: CheckFetchURL, and fetchClient where it connects.
func handleStageURL(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		URL      string `json:"url"`
		SHA256   string `json:"sha256"`
		Replaces string `json:"replaces"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !a.customConfirmed(w, r, "") {
		return
	}
	cs := a.eng().custom
	if !cs.takeSlot() {
		writeErr(w, 429, stageBusy)
		return
	}
	defer cs.freeSlot()
	ctx, cancel := context.WithTimeout(r.Context(), uploadTime)
	defer cancel()
	st, err := cs.store.StageURL(ctx, b.URL)
	if err != nil {
		a.stageError(w, r, err, cores.CleanText(b.URL, 300))
		return
	}
	a.finishStage(w, r, st, b.Replaces, b.SHA256)
}

// POST /gaming/custom/staged/placed {key, replaces?}, with the password (confirm): the key is one
// GET /gaming/custom listed, so no request ever names a path.
func handleStagePlaced(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Key      string `json:"key"`
		Replaces string `json:"replaces"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !a.customConfirmed(w, r, "") {
		return
	}
	cs := a.eng().custom
	if !cs.takeSlot() {
		writeErr(w, 429, stageBusy)
		return
	}
	defer cs.freeSlot()
	st, err := cs.store.StagePlaced(b.Key)
	if err != nil {
		a.stageError(w, r, err, cores.CleanText(b.Key, 200))
		return
	}
	a.finishStage(w, r, st, b.Replaces, "")
}

// takeStage finds a stage this person made. To anyone else it doesn't exist.
func (a *App) takeStage(r *http.Request) *stagedCore {
	cs := a.eng().custom
	cs.mu.Lock()
	defer cs.mu.Unlock()
	st := cs.staged[r.PathValue("token")]
	if st == nil || st.owner != who(r).User {
		return nil
	}
	if time.Since(st.s.Created) > stageLife {
		cs.store.Discard(st.s)
		delete(cs.staged, st.s.Token)
		return nil
	}
	return st
}

// DELETE /gaming/custom/staged/{token}
func handleStageCancel(a *App, w http.ResponseWriter, r *http.Request) {
	st := a.takeStage(r)
	if st == nil {
		writeErr(w, 404, "not found")
		return
	}
	cs := a.eng().custom
	cs.mu.Lock()
	delete(cs.staged, st.s.Token)
	cs.mu.Unlock()
	cs.store.Discard(st.s)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ---- adding a core: approval ----

type coreSettings struct {
	Name       *string   `json:"name"`
	License    *string   `json:"license"`
	Note       *string   `json:"note"`
	Consoles   *[]string `json:"consoles"`
	DefaultFor *[]string `json:"defaultFor"`
}

// checkConsoles says which consoles can be assigned: any that exists and isn't hidden.
func (a *App) checkConsoles(list []string) error {
	if len(list) > 200 {
		return errors.New("that's too many consoles")
	}
	for _, id := range list {
		if console(id) == nil {
			return fmt.Errorf("%q isn't a console on this server", cores.CleanText(id, 60))
		}
	}
	return nil
}

// apply puts settings from a request on a core, checking them.
func (a *App) applyCoreSettings(c *CustomCore, b *coreSettings, info *cores.Info) error {
	if b.Name != nil {
		name := cores.CleanText(*b.Name, 120)
		if name == "" {
			return errors.New("a core needs a name")
		}
		c.Name = name
	}
	if b.Note != nil {
		c.Note = cores.CleanText(*b.Note, 2000)
	}
	if b.Consoles != nil {
		if err := a.checkConsoles(*b.Consoles); err != nil {
			return err
		}
		c.Consoles = slices.Compact(slices.Sorted(slices.Values(*b.Consoles)))
	}
	typed := ""
	switch {
	case b.License != nil:
		typed = cores.CleanText(*b.License, 200)
	case c.LicenseSource == "admin":
		typed = c.License
	}
	c.License, c.LicenseSource, c.NonCommercial = coreLicense(c.ID, c.Report.File, c.Report.Name, info, typed)
	return nil
}

// setDefaults makes a core the default for these consoles, and no others.
func (a *App) setCoreDefaults(ctx context.Context, c *CustomCore, list []string) error {
	prefs := a.PreferredCores(ctx)
	for console, id := range prefs {
		if id == c.ID && !slices.Contains(list, console) {
			delete(prefs, console)
		}
	}
	for _, console := range list {
		if !slices.Contains(c.Consoles, console) {
			return fmt.Errorf("it has to be assigned to %s before it can be the default", cores.CleanText(console, 60))
		}
		prefs[console] = c.ID
	}
	return a.SetConfig(ctx, settingCores, prefs)
}

// POST /gaming/custom/staged/{token}/approve {id?, acknowledged, name?, license?,
// note?, consoles?, defaultFor?}. Whoever got here gave their password to start this;
// the token is what ties the two together.
func handleStageApprove(a *App, w http.ResponseWriter, r *http.Request) {
	st := a.takeStage(r)
	if st == nil {
		writeErr(w, 404, "This test has expired, or was for someone else. Add the core again.")
		return
	}
	var b struct {
		coreSettings
		ID           string `json:"id"`
		Acknowledged bool   `json:"acknowledged"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !b.Acknowledged {
		writeErr(w, 400, "Please confirm that you understand this core runs as code on this server.")
		return
	}
	// Windows' Smart App Control would refuse to load an unsigned file: said now, not at Play.
	if err := appcontrol.CheckInstall(st.s.Library, "this core", "Ask the core's author for a signed build."); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	ctx := r.Context()
	id := st.report.ID
	if st.replaces != "" {
		id = st.replaces
	} else if b.ID != "" {
		id = b.ID
	}
	if !cores.ValidCustomID(id) {
		writeErr(w, 400, "A core's name can only have small letters, numbers, dashes and underscores.")
		return
	}
	if cores.Offered(id) {
		writeErr(w, 409, fmt.Sprintf("%q is a core in this server's own list. Give this one another name, so nobody takes it for that.", id))
		return
	}
	existing, err := a.customCoreRow(ctx, id)
	replacing := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 500, err.Error())
		return
	}
	if replacing && st.replaces != id {
		writeErr(w, 409, fmt.Sprintf("A core called %q is already here. Replace it from its own card, or give this one another name.", id))
		return
	}
	now := time.Now().Unix()
	c := &CustomCore{ID: id, AddedAt: now, Consoles: []string{}}
	if replacing {
		c = existing
	}
	c.Name, c.Version, c.SHA256, c.Size, c.Report = st.report.Name, st.report.Version, st.s.SHA256, st.s.Size, st.report
	c.Source, c.SourceURL, c.AddedBy = st.s.Source, st.s.SourceURL, a.userName(ctx, who(r).User)
	c.Report.ID = id
	if err := a.applyCoreSettings(c, &b.coreSettings, st.s.Info); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cs := a.eng().custom
	// Nothing past this point can be cancelled by a second approval.
	cs.mu.Lock()
	if cs.staged[st.s.Token] != st {
		cs.mu.Unlock()
		writeErr(w, 404, "This test has already been used.")
		return
	}
	delete(cs.staged, st.s.Token)
	cs.mu.Unlock()
	added, kept, err := cs.store.Commit(st.s, id)
	if err != nil {
		cs.store.Discard(st.s)
		a.auditCore(ctx, host.For(r), "add-refused", id, err.Error())
		writeErr(w, 422, err.Error())
		return
	}
	for _, f := range kept {
		c.Report.Warnings = append(c.Report.Warnings, fmt.Sprintf("%s is already in the BIOS folder, so that copy was kept and this package's wasn't used.", f))
	}
	c.systemFiles = slices.Compact(slices.Sorted(slices.Values(append(c.systemFiles, added...))))
	if err := a.saveCustomCore(ctx, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO custom_core_versions(core_id, sha256, version, added_at) VALUES(?,?,?,?)
		ON CONFLICT(core_id, sha256) DO UPDATE SET version = excluded.version, added_at = excluded.added_at`, id, c.SHA256, c.Version, now); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if b.DefaultFor != nil {
		if err := a.setCoreDefaults(ctx, c, *b.DefaultFor); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	a.pruneVersions(ctx, c)
	a.publishCustomConsoles(ctx)
	action := "added"
	if replacing {
		action = "replaced"
	}
	a.auditCore(ctx, host.For(r), action, id, fmt.Sprintf("%s %s, sha256 %s, from %s %s, consoles %s", c.Name, c.Version, c.SHA256, c.Source, c.SourceURL, strings.Join(c.Consoles, ",")))
	writeJSON(w, 200, c)
}

// pruneVersions keeps the newest few, so going back is possible and disks don't fill.
func (a *App) pruneVersions(ctx context.Context, c *CustomCore) {
	versions := a.coreVersions(ctx, c)
	kept := 0
	for _, v := range versions {
		if v.Current {
			continue
		}
		kept++
		if kept >= maxKeptVersion {
			store := a.eng().custom.store
			store.RemoveVersion(c.ID, v.SHA256)
			if _, err := a.DB.ExecContext(ctx, `DELETE FROM custom_core_versions WHERE core_id = ? AND sha256 = ?`, c.ID, v.SHA256); err != nil {
				a.Log.Warn("pruning core versions", "err", err)
			}
		}
	}
}

// ---- changing a core ----

func (a *App) customCorePath(w http.ResponseWriter, r *http.Request) *CustomCore {
	id := r.PathValue("id")
	if !cores.ValidCustomID(id) {
		writeErr(w, 404, "not found")
		return nil
	}
	c, err := a.customCoreRow(r.Context(), id)
	if err != nil {
		writeErr(w, 404, "not found")
		return nil
	}
	return c
}

// PUT /gaming/custom/cores/{id}: its name, licence, note and consoles. The code
// doesn't change, so this needs no password.
func handleCustomCorePut(a *App, w http.ResponseWriter, r *http.Request) {
	c := a.customCorePath(w, r)
	if c == nil {
		return
	}
	var b coreSettings
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	ctx := r.Context()
	store := a.eng().custom.store
	if err := a.applyCoreSettings(c, &b, store.ReadVersionInfo(c.ID, c.SHA256)); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := a.saveCustomCore(ctx, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defaults := b.DefaultFor
	if defaults == nil {
		// A core taken off a console stops being its default.
		kept := []string{}
		for console, id := range a.PreferredCores(ctx) {
			if id == c.ID && slices.Contains(c.Consoles, console) {
				kept = append(kept, console)
			}
		}
		defaults = &kept
	}
	if err := a.setCoreDefaults(ctx, c, *defaults); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	a.publishCustomConsoles(ctx)
	a.auditCore(ctx, host.For(r), "changed", c.ID, "consoles "+strings.Join(c.Consoles, ",")+", default for "+strings.Join(*defaults, ",")+", license "+c.License)
	writeJSON(w, 200, c)
}

// DELETE /gaming/custom/cores/{id}. Saves belong to accounts and games, not cores,
// so nothing of anyone's goes with it. This works with custom cores turned off, because
// getting rid of a core should never be harder than having one.
func handleCustomCoreDelete(a *App, w http.ResponseWriter, r *http.Request) {
	c := a.customCorePath(w, r)
	if c == nil {
		return
	}
	ctx := r.Context()
	if n := rpc.Mgr.UsingCore(a.eng().custom.store.CorePath(c.ID, c.SHA256)); n > 0 {
		writeErr(w, 409, "A game is playing with this core right now. Stop it first.")
		return
	}
	prefs := a.PreferredCores(ctx)
	for console, id := range prefs {
		if id == c.ID {
			delete(prefs, console)
		}
	}
	if err := a.SetConfig(ctx, settingCores, prefs); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, q := range []string{`DELETE FROM game_core_choice WHERE core_id = ?`, `DELETE FROM custom_core_versions WHERE core_id = ?`, `DELETE FROM game_core_plays WHERE core_id = ?`, `DELETE FROM custom_cores WHERE id = ?`} {
		if _, err := a.DB.ExecContext(ctx, q, c.ID); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
	}
	if err := a.eng().custom.store.RemoveCore(c.ID, c.systemFiles); err != nil {
		a.Log.Warn("removing a core's files", "core", c.ID, "err", err)
	}
	a.publishCustomConsoles(ctx)
	a.auditCore(ctx, host.For(r), "removed", c.ID, c.Name+" "+c.Version+", sha256 "+c.SHA256)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// POST /gaming/custom/cores/{id}/reapprove {password}: someone has looked at why a
// file changed and wants it. It has to pass a test-load again like a new file.
func handleCustomReapprove(a *App, w http.ResponseWriter, r *http.Request) {
	c := a.customCorePath(w, r)
	if c == nil {
		return
	}
	var b struct {
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !a.customConfirmed(w, r, c.ID) {
		return
	}
	ctx := r.Context()
	cs := a.eng().custom
	state := cs.store.Verify(c.ID, c.SHA256)
	switch state.State {
	case "ok":
		writeJSON(w, 200, c)
		return
	case "changed":
	default:
		writeErr(w, 409, "This core's file is gone or has been swapped for a link. Remove the core and add it again.")
		return
	}
	box, err := os.MkdirTemp(filepath.Dir(cs.store.VersionFolder(c.ID, c.SHA256)), ".box-")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(box)
	res, err := a.testLoad(ctx, cs.store.CorePath(c.ID, c.SHA256), box)
	if err != nil {
		a.auditCore(ctx, host.For(r), "reapprove-failed", c.ID, err.Error())
		writeErr(w, 422, err.Error())
		return
	}
	old := c.SHA256
	if err := os.Rename(cs.store.VersionFolder(c.ID, old), cs.store.VersionFolder(c.ID, state.Found)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	bin, _ := cores.ReadBinary(cs.store.CorePath(c.ID, state.Found))
	c.SHA256, c.Version = state.Found, res.Version
	if res.Name != "" {
		c.Report.Name = res.Name
	}
	c.Report.SHA256, c.Report.Version, c.Report.Extensions, c.Report.Binary, c.Report.Arch = state.Found, res.Version, res.Extensions, bin, bin.Describe()
	if st, err := os.Stat(cs.store.CorePath(c.ID, state.Found)); err == nil {
		c.Size, c.Report.Size = st.Size(), st.Size()
	}
	if err := a.saveCustomCore(ctx, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE custom_core_versions SET sha256 = ?, version = ? WHERE core_id = ? AND sha256 = ?`, state.Found, res.Version, c.ID, old); err != nil {
		a.Log.Warn("recording a core's version", "err", err)
	}
	a.auditCore(ctx, host.For(r), "reapproved", c.ID, "was "+old+", now "+state.Found)
	writeJSON(w, 200, c)
}

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// POST /gaming/custom/cores/{id}/rollback {password, sha256}: back to a version
// kept from before. Nothing is thrown away, so this can be done again the other way.
func handleCustomRollback(a *App, w http.ResponseWriter, r *http.Request) {
	c := a.customCorePath(w, r)
	if c == nil {
		return
	}
	var b struct {
		SHA256 string `json:"sha256"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if !a.customConfirmed(w, r, c.ID) {
		return
	}
	ctx := r.Context()
	if !shaRe.MatchString(b.SHA256) {
		writeErr(w, 400, "that isn't a version of this core")
		return
	}
	var version string
	if err := a.DB.QueryRowContext(ctx, `SELECT version FROM custom_core_versions WHERE core_id = ? AND sha256 = ?`, c.ID, b.SHA256).Scan(&version); err != nil {
		writeErr(w, 404, "that version isn't kept any more")
		return
	}
	cs := a.eng().custom
	if state := cs.store.Verify(c.ID, b.SHA256); state.State != "ok" {
		writeErr(w, 409, "That version's file has changed or gone, so it can't be used.")
		return
	}
	box, err := os.MkdirTemp(filepath.Dir(cs.store.VersionFolder(c.ID, b.SHA256)), ".box-")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(box)
	if _, err := a.testLoad(ctx, cs.store.CorePath(c.ID, b.SHA256), box); err != nil {
		a.auditCore(ctx, host.For(r), "rollback-failed", c.ID, err.Error())
		writeErr(w, 422, err.Error())
		return
	}
	old := c.SHA256
	c.SHA256, c.Version = b.SHA256, version
	c.Report.SHA256, c.Report.Version = b.SHA256, version
	if st, err := os.Stat(cs.store.CorePath(c.ID, b.SHA256)); err == nil {
		c.Size, c.Report.Size = st.Size(), st.Size()
	}
	if err := a.saveCustomCore(ctx, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.auditCore(ctx, host.For(r), "rolled-back", c.ID, "from "+old+" to "+b.SHA256)
	writeJSON(w, 200, c)
}

// POST /gaming/custom/cores/{id}/files: multipart, password first, then file: a
// core's .info file, or a .zip with assets/ or system/ folders. The core's own file
// can't change this way.
func handleCustomFiles(a *App, w http.ResponseWriter, r *http.Request) {
	c := a.customCorePath(w, r)
	if c == nil {
		return
	}
	cs := a.eng().custom
	if !cs.takeSlot() {
		writeErr(w, 429, stageBusy)
		return
	}
	defer cs.freeSlot()
	_, part, ok := a.readUpload(w, r, c.ID)
	if !ok {
		return
	}
	defer part.Close()
	ctx := r.Context()
	name := rawFileName(part)
	added, kept, err := cs.store.AddExtras(part, name, c.ID, c.SHA256)
	if err != nil {
		a.stageError(w, r, err, c.ID)
		return
	}
	c.systemFiles = slices.Compact(slices.Sorted(slices.Values(append(c.systemFiles, added...))))
	if info := cs.store.ReadVersionInfo(c.ID, c.SHA256); info != nil {
		a.applyInfoToReport(c, info)
		typed := ""
		if c.LicenseSource == "admin" {
			typed = c.License
		}
		c.License, c.LicenseSource, c.NonCommercial = coreLicense(c.ID, c.Report.File, c.Report.Name, info, typed)
	}
	if cs.store.AssetsPath(c.ID, c.SHA256) != "" {
		c.Report.Assets = max(c.Report.Assets, 1)
	}
	c.Report.SystemFiles += len(added)
	if err := a.saveCustomCore(ctx, c); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	a.auditCore(ctx, host.For(r), "files-added", c.ID, cores.CleanText(name, 120))
	writeJSON(w, 200, map[string]any{"core": c, "added": added, "kept": kept})
}

func (a *App) applyInfoToReport(c *CustomCore, info *cores.Info) {
	r := &c.Report
	r.HasInfo, r.BIOSKnown, r.System, r.Notes = true, true, info.System, info.Notes
	r.BIOS = []cores.Firmware{}
	for _, f := range info.Firmware {
		if native := filepath.FromSlash(f.File); filepath.IsLocal(native) {
			if _, err := os.Lstat(filepath.Join(a.SystemDir(), native)); err == nil {
				f.Present = true
			}
		}
		r.BIOS = append(r.BIOS, f)
	}
	if info.HWRender && r.NeedsGPU != "yes" {
		r.NeedsGPU, r.GPUWhy = "yes", "Its info file says it wants a graphics card."
	}
}

// ---- custom consoles ----

// publishCustomConsoles makes the stored consoles the ones the scanner and every list
// see. They are data, not code, so they stay even with custom cores turned off.
func (a *App) publishCustomConsoles(ctx context.Context) {
	bg := context.WithoutCancel(ctx)
	customs := a.customCoreRows(bg)
	rows, err := a.DB.QueryContext(bg, `SELECT id, data, aliases, cores FROM custom_consoles ORDER BY created_at, id`)
	if err != nil {
		a.Log.Warn("reading custom consoles", "err", err)
		return
	}
	defer rows.Close()
	list := []wittyplay.Console{}
	for rows.Next() {
		c, listed, err := scanCustomConsole(rows)
		if err != nil {
			a.Log.Warn("a custom console couldn't be read", "err", err)
			continue
		}
		for _, id := range listed {
			if known, ok := wittyplay.CoreLicense(id); ok && cores.Offered(id) {
				c.Cores = append(c.Cores, known)
			}
		}
		for _, core := range customs {
			if slices.Contains(core.Consoles, c.ID) || slices.Contains(listed, core.ID) {
				c.Cores = append(c.Cores, wittyplay.Core{ID: core.ID, Name: core.Name, License: core.License})
			}
		}
		list = append(list, *c)
	}
	wittyplay.SetCustomConsoles(list)
}

func scanCustomConsole(row interface{ Scan(...any) error }) (*wittyplay.Console, []string, error) {
	var id, data, aliases, listed string
	if err := row.Scan(&id, &data, &aliases, &listed); err != nil {
		return nil, nil, err
	}
	var c wittyplay.Console
	if err := json.Unmarshal([]byte(data), &c); err != nil {
		return nil, nil, err
	}
	// Whatever the database holds, a stored console can't become one from the table.
	if !wittyplay.IsCustomConsole(id) {
		return nil, nil, fmt.Errorf("%q isn't a custom console's name", id)
	}
	c.ID = id
	_ = json.Unmarshal([]byte(aliases), &c.Aliases)
	var cs []string
	_ = json.Unmarshal([]byte(listed), &cs)
	c.Cores = nil
	return &c, cs, nil
}

// customConsoleViews are the custom consoles, with how many games and libraries the server has
// for each (as this administrator sees them).
func (a *App) customConsoleViews(ctx context.Context, on host.On) []CustomConsoleView {
	out := []CustomConsoleView{}
	rows, err := a.DB.QueryContext(ctx, `SELECT id, data, aliases, cores FROM custom_consoles ORDER BY created_at, id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	type pending struct {
		c      *wittyplay.Console
		listed []string
	}
	var all []pending
	for rows.Next() {
		if c, listed, err := scanCustomConsole(rows); err == nil {
			all = append(all, pending{c, listed})
		}
	}
	rows.Close()
	if len(all) == 0 {
		return out
	}
	games, _ := a.Host.Counts(ctx, on, "games", "console")
	libs, _ := a.Host.Libraries(ctx)
	for _, p := range all {
		v := CustomConsoleView{CustomConsole: wittyplay.CustomConsoleInput(p.c, p.listed), Games: games[p.c.ID]}
		for _, l := range libs {
			if l.Console == p.c.ID {
				v.Libraries++
			}
		}
		out = append(out, v)
	}
	return out
}

func (a *App) saveCustomConsole(ctx context.Context, id string, r *http.Request, w http.ResponseWriter) (*wittyplay.Console, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, wittyplay.MaxCustomConsoleJSON+1))
	if err != nil {
		writeErr(w, 400, "this is too long to be one console")
		return nil, false
	}
	in, err := wittyplay.ParseCustomConsole(raw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil, false
	}
	others := []wittyplay.Console{}
	for _, c := range wittyplay.CustomConsoles() {
		if c.ID != id {
			others = append(others, c)
		}
	}
	if id == "" && len(others) >= wittyplay.MaxCustomConsoles {
		writeErr(w, 409, "That's as many custom consoles as a server can have.")
		return nil, false
	}
	c, err := in.Validate(id, others)
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil, false
	}
	for _, core := range in.Cores {
		_, isCustom := a.customCoreRow(ctx, core)
		if !cores.Offered(core) && isCustom != nil {
			writeErr(w, 400, fmt.Sprintf("%q isn't a core this server has heard of", core))
			return nil, false
		}
	}
	data, _ := json.Marshal(c)
	now := time.Now().Unix()
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO custom_consoles(id, data, aliases, cores, created_at, updated_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET data = excluded.data, aliases = excluded.aliases, cores = excluded.cores, updated_at = excluded.updated_at`,
		c.ID, string(data), toJSON(c.Aliases), toJSON(in.Cores), now, now); err != nil {
		writeErr(w, 500, err.Error())
		return nil, false
	}
	a.publishCustomConsoles(ctx)
	return c, true
}

// POST /gaming/custom/consoles: one console as JSON, from the form or pasted in.
func handleCustomConsolePost(a *App, w http.ResponseWriter, r *http.Request) {
	c, ok := a.saveCustomConsole(r.Context(), "", r, w)
	if !ok {
		return
	}
	a.auditCore(r.Context(), host.For(r), "console-added", "", c.ID+": "+c.Name)
	writeJSON(w, 201, wittyplay.ByID(c.ID))
}

// PUT /gaming/custom/consoles/{id}
func handleCustomConsolePut(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wittyplay.IsCustomConsole(id) || wittyplay.ByID(id) == nil {
		writeErr(w, 404, "not found")
		return
	}
	c, ok := a.saveCustomConsole(r.Context(), id, r, w)
	if !ok {
		return
	}
	a.auditCore(r.Context(), host.For(r), "console-changed", "", c.ID+": "+c.Name)
	writeJSON(w, 200, wittyplay.ByID(c.ID))
}

// DELETE /gaming/custom/consoles/{id}. A library set to a console has to be changed
// first; its games have nowhere sensible to go otherwise.
func handleCustomConsoleDelete(a *App, w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !wittyplay.IsCustomConsole(id) || wittyplay.ByID(id) == nil {
		writeErr(w, 404, "not found")
		return
	}
	ctx := r.Context()
	all, err := a.Host.Libraries(ctx)
	if err != nil {
		writeErr(w, 503, "The server couldn't say which libraries are set to this console. Try again.")
		return
	}
	libs := 0
	for _, l := range all {
		if l.Console == id {
			libs++
		}
	}
	if libs > 0 {
		writeErr(w, 409, fmt.Sprintf("%d %s set to this console. Change or remove them first.", libs, plural(libs, "library is", "libraries are")))
		return
	}
	if _, err := a.DB.ExecContext(ctx, `DELETE FROM custom_consoles WHERE id = ?`, id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, c := range a.customCoreRows(ctx) {
		if i := slices.Index(c.Consoles, id); i >= 0 {
			c.Consoles = slices.Delete(c.Consoles, i, i+1)
			if err := a.saveCustomCore(ctx, c); err != nil {
				a.Log.Warn("updating a core's consoles", "err", err)
			}
		}
	}
	prefs := a.PreferredCores(ctx)
	if _, ok := prefs[id]; ok {
		delete(prefs, id)
		_ = a.SetConfig(ctx, settingCores, prefs)
	}
	a.publishCustomConsoles(ctx)
	a.auditCore(ctx, host.For(r), "console-removed", "", id)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
