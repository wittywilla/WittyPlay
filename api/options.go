// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/ipc"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// A person's game settings, at each level, and what a game's core offers. Moved from the server
// (api_game_options.go, library/game_options.go). A person's levels are in wittyplay_settings;
// what everyone starts with (the server's gamingDefaults and gamingDefaults.<console> settings)
// and the server's limits (gamingMaxSpeed, gamingRewindMB, gamingRunAhead) are WittyPlay's own
// settings now (a.Config). What a core last said it offers is in wittyplay_core_options.
// docs/GAMING.md has the reasoning.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("GET /games/settings/schema", w(handleGameSettingsSchema))
		mux.Handle("GET /games/settings", w(handleGameSettingsGet))
		mux.Handle("PUT /games/settings", w(handleGameSettingsPut))
		mux.Handle("POST /games/settings/validate", w(handleGameSettingsValidate))
		mux.Handle("GET /games/{id}/core-options", w(handleGameCoreOptions))
	})
}

// Hooks the other route groups fill in.
var (
	// gameCore is the core a console's games, or this game, use here, chosen the way a game
	// chooses one (the cores group sets it). ok is false when there is none, or it may not be
	// loaded (an unapproved or changed custom core). Until it's set only the test console has
	// a core.
	gameCore func(ctx context.Context, a *App, console string, item int64) (id, path string, ok bool)
	// rewindByDefault says which consoles' games rewind unless someone says otherwise (the
	// group that has the consoles' traits sets it). Until then none do.
	rewindByDefault = func(console string) bool { return false }
)

const gameDefaultsKey = "gamingDefaults"

func gameDefaultsSetting(console string) string {
	if console == "" {
		return gameDefaultsKey
	}
	return gameDefaultsKey + "." + console
}

// configNumber reads one of WittyPlay's settings as a number, written as one or as words.
func (a *App) configNumber(ctx context.Context, key string) (float64, bool) {
	var raw json.RawMessage
	if ok, err := a.Config(ctx, key, &raw); !ok || err != nil {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// configBool reads one of WittyPlay's settings as yes or no (true, 1 or "1"), or def.
func (a *App) configBool(ctx context.Context, key string, def bool) bool {
	var raw json.RawMessage
	if ok, err := a.Config(ctx, key, &raw); !ok || err != nil {
		return def
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	if f, ok := a.configNumber(ctx, key); ok {
		return f != 0
	}
	return def
}

// gameLimits is what this server allows, whatever anyone's settings say.
func (a *App) gameLimits(ctx context.Context) wittyplay.Limits {
	// A small machine has less to spare, unless its administrator says otherwise.
	speed, rewind := 4.0, int64(32)
	if runtime.NumCPU() < 4 {
		speed, rewind = 2, 16
	}
	if v, ok := a.configNumber(ctx, "gamingMaxSpeed"); ok && v >= 1 {
		speed = min(v, ipc.SpeedMax)
	}
	if v, ok := a.configNumber(ctx, "gamingRewindMB"); ok && v >= 0 {
		rewind = min(int64(v), ipc.RewindBytesMax>>20)
	}
	return wittyplay.Limits{MaxSpeed: speed, RewindBytes: rewind << 20, RunAhead: a.configBool(ctx, "gamingRunAhead", true)}
}

// serverGameLayer reads what an administrator set for everyone.
func (a *App) serverGameLayer(ctx context.Context, console string) *wittyplay.Layer {
	l := &wittyplay.Layer{}
	if _, err := a.Config(ctx, gameDefaultsSetting(console), l); err != nil {
		return &wittyplay.Layer{}
	}
	return l
}

// withRewindDefault turns rewind on in the everyone-on-this-console layer of a console that
// rewinds by default, unless an administrator has said either way. It changes what's read, never
// what's stored.
func withRewindDefault(console string, server, serverConsole *wittyplay.Layer) {
	const key = "emulation.rewind"
	if serverConsole == nil || !rewindByDefault(console) {
		return
	}
	if _, set := server.Values[key]; set {
		return
	}
	if _, set := serverConsole.Values[key]; set {
		return
	}
	if serverConsole.Values == nil {
		serverConsole.Values = map[string]any{}
	}
	serverConsole.Values[key] = true
}

// gameLayer reads one level of a person's settings. An empty console and item is everything they
// play; a console is that console's games; an item is one game.
func (a *App) gameLayer(ctx context.Context, userID int64, console string, itemID int64) (*wittyplay.Layer, error) {
	var data string
	err := a.DB.QueryRowContext(ctx, `SELECT data FROM wittyplay_settings WHERE user_id = ? AND console = ? AND item_id = ?`, userID, console, itemID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return &wittyplay.Layer{}, nil
	}
	if err != nil {
		return nil, err
	}
	var l wittyplay.Layer
	if json.Unmarshal([]byte(data), &l) != nil {
		return &wittyplay.Layer{}, nil // something unreadable never stops a game starting
	}
	return &l, nil
}

// setGameLayer replaces one level. An empty one takes no row.
func (a *App) setGameLayer(ctx context.Context, userID int64, console string, itemID int64, l *wittyplay.Layer) error {
	if l == nil || l.Empty() {
		_, err := a.DB.ExecContext(ctx, `DELETE FROM wittyplay_settings WHERE user_id = ? AND console = ? AND item_id = ?`, userID, console, itemID)
		return err
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = a.DB.ExecContext(ctx, `INSERT INTO wittyplay_settings(user_id, console, item_id, data, updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(user_id, console, item_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, userID, console, itemID, string(b), time.Now().Unix())
	return err
}

// gameLayers reads every level that applies, least specific first.
func (a *App) gameLayers(ctx context.Context, userID int64, console string, itemID int64) []wittyplay.NamedLayer {
	server := a.serverGameLayer(ctx, "")
	out := []wittyplay.NamedLayer{{Level: wittyplay.LevelServer, Layer: server}}
	if console != "" {
		everyone := a.serverGameLayer(ctx, console)
		withRewindDefault(console, server, everyone)
		out = append(out, wittyplay.NamedLayer{Level: wittyplay.LevelServerConsole, Layer: everyone})
	}
	add := func(level, c string, id int64) {
		l, err := a.gameLayer(ctx, userID, c, id)
		if err != nil {
			a.Log.Warn("reading game settings", "cat", "games", "err", err)
			l = &wittyplay.Layer{}
		}
		out = append(out, wittyplay.NamedLayer{Level: level, Layer: l})
	}
	add(wittyplay.LevelPerson, "", 0)
	if console != "" {
		add(wittyplay.LevelConsole, console, 0)
	}
	if itemID != 0 {
		add(wittyplay.LevelGame, "", itemID)
	}
	return out
}

// applyGameSettings gives a game about to start what its player has chosen (for the group that
// starts games), unless they asked for safe mode: then it gets nothing but defaults.
func (a *App) applyGameSettings(ctx context.Context, o *stream.Options, safe bool) {
	lim := a.gameLimits(ctx)
	o.MaxSpeed = lim.MaxSpeed
	o.SafeMode = safe
	o.OnCoreOptions = func(started string, list []ipc.Option) { a.keepCoreOptions(started, list) }
	if safe {
		return
	}
	res := wittyplay.Resolve(a.gameLayers(ctx, o.UserID, o.Console, o.ItemID))
	control := wittyplay.ControlFor(res.Values, lim)
	o.Control = &control
	o.CoreOptions = overOptions(o.CoreOptions, res.CoreOptions)
	o.NoAutoSave = res.Values["states.autoSave"] == false
	if meta, err := a.gameMeta(ctx, o.UserID, o.ItemID); err == nil {
		o.Cheats = wittyplay.CheatsForCore(meta.Cheats)
	}
	rom, cleanup, notices := a.patchROM(ctx, o.UserID, o.ItemID, o.ROM, o.Cleanup)
	o.ROM, o.Cleanup, o.Notices = rom, cleanup, append(o.Notices, notices...)
}

// overOptions lays a person's core options over the ones a game starts with.
func overOptions(base, mine map[string]string) map[string]string {
	if len(base) == 0 {
		return mine
	}
	out := make(map[string]string, len(base)+len(mine))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range mine {
		out[k] = v
	}
	return out
}

func optionInfo(o ipc.Option) wittyplay.OptionInfo {
	return wittyplay.OptionInfo{Key: o.Key, Label: o.Label, Info: o.Info, Category: o.Category, Choices: o.Choices, Labels: o.Labels, Default: o.Default, Hidden: o.Hidden}
}

func optionInfos(list []ipc.Option) []wittyplay.OptionInfo {
	out := make([]wittyplay.OptionInfo, 0, len(list))
	for _, o := range list {
		out = append(out, optionInfo(o))
	}
	return out
}

// keepCoreOptions keeps what a core said it offers, so settings screens can show it with no game
// running.
func (a *App) keepCoreOptions(coreID string, list []ipc.Option) {
	b, err := json.Marshal(optionInfos(list))
	if err == nil {
		_, err = a.DB.ExecContext(context.Background(), `INSERT INTO wittyplay_core_options(core, data, updated_at) VALUES(?,?,?)
			ON CONFLICT(core) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`, coreID, string(b), time.Now().Unix())
	}
	if err != nil {
		a.Log.Warn("keeping a core's options", "cat", "games", "err", err)
	}
}

func (a *App) coreOptionList(ctx context.Context, core string) ([]wittyplay.OptionInfo, bool) {
	var data string
	if err := a.DB.QueryRowContext(ctx, `SELECT data FROM wittyplay_core_options WHERE core = ?`, core).Scan(&data); err != nil {
		return nil, false
	}
	var list []wittyplay.OptionInfo
	if json.Unmarshal([]byte(data), &list) != nil {
		return nil, false
	}
	return list, true
}

// runningGame is the game this person is playing, if it's that one (itemID 0: any).
func runningGame(userID, itemID int64) *stream.Session {
	sess := rpc.Mgr.ForUser(userID)
	if sess == nil || (itemID != 0 && sess.ItemID() != itemID) {
		return nil
	}
	return sess
}

// applyToRunningGame tells this person's running game what's changed, so that a setting changed
// from a menu takes effect at once. A change for some other game, or for a console it isn't,
// isn't its concern.
func (a *App) applyToRunningGame(ctx context.Context, userID int64, console string, itemID int64) {
	sess := runningGame(userID, itemID)
	if sess == nil || sess.Safe() {
		return
	}
	info := sess.Info()
	if console != "" && info.Console != console {
		return
	}
	res := wittyplay.Resolve(a.gameLayers(ctx, userID, info.Console, sess.ItemID()))
	sess.SetAutoSave(res.Values["states.autoSave"] != false)
	// What the game started with stays underneath, so a change doesn't put it back to the core's
	// raw defaults.
	var resolution string
	_, _ = a.Config(ctx, "gamingResolution", &resolution)
	base := wittyplay.StartOptions(info.Console, sess.CoreID(), resolution, filepath.Join(a.Dir, "system"))
	if err := sess.Apply(wittyplay.ControlFor(res.Values, a.gameLimits(ctx)), overOptions(base, res.CoreOptions)); err != nil {
		a.Log.Debug("applying game settings", "cat", "games", "err", err)
	}
}

// Asking a core for its options loads it: one at a time, and a core that has nothing to say
// without a game isn't asked again for a while.
var (
	probeSlot   = make(chan struct{}, 1)
	silentCores sync.Map // core id → time.Time
)

const silentCoreFor = time.Hour

// coreOptionsFor finds what the core for a console offers: from the running game, then what was
// kept, then by asking the core. It returns which core that was, too.
func (a *App) coreOptionsFor(ctx context.Context, userID int64, console string, itemID int64) ([]wittyplay.OptionInfo, string) {
	var coreID, corePath string
	switch {
	case console == wittyplay.TestConsole:
		coreID, corePath = "test", ipc.TestCore
	case gameCore != nil:
		var ok bool
		if coreID, corePath, ok = gameCore(ctx, a, console, itemID); !ok {
			corePath = "" // not to be loaded: only what's kept is shown
		}
	}
	if coreID == "" {
		return nil, ""
	}
	if sess := runningGame(userID, itemID); sess != nil {
		sess.WaitOptions(ctx)
		if opts := sess.CoreOptions(); len(opts) > 0 {
			return optionInfos(opts), coreID
		}
	}
	if list, ok := a.coreOptionList(ctx, coreID); ok {
		return list, coreID
	}
	if corePath == "" {
		return nil, coreID
	}
	if at, ok := silentCores.Load(coreID); ok && time.Since(at.(time.Time)) < silentCoreFor {
		return nil, coreID
	}
	select {
	case probeSlot <- struct{}{}:
		defer func() { <-probeSlot }()
	case <-ctx.Done():
		return nil, coreID
	}
	opts, err := rpc.Mgr.ProbeOptions(ctx, corePath, filepath.Join(a.Dir, "system"), filepath.Join(a.Dir, "tmp"))
	if err != nil {
		a.Log.Debug("asking a core for its options", "cat", "games", "core", coreID, "err", err)
		return nil, coreID
	}
	if len(opts) == 0 {
		silentCores.Store(coreID, time.Now())
		return nil, coreID // this core only says with a game loaded
	}
	a.keepCoreOptions(coreID, opts)
	return optionInfos(opts), coreID
}

func handleGameSettingsSchema(a *App, w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"settings": wittyplay.Settings, "presets": wittyplay.BuiltinPresets,
		"levels":  []string{wittyplay.LevelServer, wittyplay.LevelServerConsole, wittyplay.LevelPerson, wittyplay.LevelConsole, wittyplay.LevelGame},
		"maxSlot": stream.MaxSlot, "undoSlot": stream.SlotUndo, "prevAutoSlot": stream.SlotPrevAuto})
}

// gameScope works out which console and game a request is about, from item or console.
func (a *App) gameScope(r *http.Request, consoleQ string, itemQ int64) (string, int64, bool) {
	if itemQ != 0 {
		it, err := a.visibleGame(r.Context(), r, itemQ)
		if err != nil {
			return "", 0, false
		}
		return itemConsole(it), it.ID, true
	}
	if consoleQ != "" && !consoleKnown(consoleQ) {
		return "", 0, false
	}
	return consoleQ, 0, true
}

func (a *App) settingsView(ctx context.Context, userID int64, console string, itemID int64) map[string]any {
	layers := a.gameLayers(ctx, userID, console, itemID)
	byLevel := map[string]*wittyplay.Layer{}
	for _, l := range layers {
		byLevel[l.Level] = l.Layer
	}
	return map[string]any{"resolved": wittyplay.Resolve(layers), "layers": byLevel, "limits": a.gameLimits(ctx), "console": console, "item": itemID}
}

func handleGameSettingsGet(a *App, w http.ResponseWriter, r *http.Request) {
	item, _ := strconv.ParseInt(r.URL.Query().Get("item"), 10, 64)
	console, itemID, ok := a.gameScope(r, r.URL.Query().Get("console"), item)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	writeJSON(w, 200, a.settingsView(r.Context(), who(r).User, console, itemID))
}

// handleGameSettingsPut changes one level: a patch (null removes a value), a whole settings file,
// or a reset. Anyone may change their own; only an administrator can change what everyone
// starts with.
func handleGameSettingsPut(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Level       string             `json:"level"`
		Console     string             `json:"console"`
		Item        int64              `json:"item"`
		Values      map[string]any     `json:"settings"`
		CoreOptions map[string]*string `json:"coreOptions"`
		Document    *string            `json:"document"`
		Reset       bool               `json:"reset"`
	}
	if err := readJSON(capBody(w, r, wittyplay.MaxCustomSize*2), &b); err != nil {
		writeErr(w, 400, "these settings can't be read")
		return
	}
	ctx, u := r.Context(), who(r)
	console, itemID, ok := a.gameScope(r, b.Console, b.Item)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	// Every level below sets both, or answers and returns.
	var read func() (*wittyplay.Layer, error)
	var write func(*wittyplay.Layer) error
	switch b.Level {
	case wittyplay.LevelServer, wittyplay.LevelServerConsole:
		if !u.Admin {
			writeErr(w, 403, "only an administrator can change what everyone starts with")
			return
		}
		key := gameDefaultsSetting("")
		if b.Level == wittyplay.LevelServerConsole {
			if console == "" {
				writeErr(w, 400, "which console?")
				return
			}
			key = gameDefaultsSetting(console)
		}
		read = func() (*wittyplay.Layer, error) {
			l := &wittyplay.Layer{}
			_, _ = a.Config(ctx, key, l)
			return l, nil
		}
		write = func(l *wittyplay.Layer) error {
			if l.Empty() {
				return a.SetConfig(ctx, key, nil)
			}
			return a.SetConfig(ctx, key, l)
		}
	case wittyplay.LevelPerson, wittyplay.LevelConsole, wittyplay.LevelGame:
		c, id := "", int64(0)
		switch b.Level {
		case wittyplay.LevelConsole:
			if console == "" {
				writeErr(w, 400, "which console?")
				return
			}
			c = console
		case wittyplay.LevelGame:
			if itemID == 0 {
				writeErr(w, 400, "which game?")
				return
			}
			id = itemID
		}
		read = func() (*wittyplay.Layer, error) { return a.gameLayer(ctx, u.User, c, id) }
		write = func(l *wittyplay.Layer) error { return a.setGameLayer(ctx, u.User, c, id, l) }
	default:
		writeErr(w, 400, "unknown level")
		return
	}
	layer := &wittyplay.Layer{}
	ignored := []wittyplay.Issue{}
	how := "patch"
	switch {
	case b.Reset:
		how = "reset"
	case b.Document != nil:
		how = "document"
		parsed, rep := wittyplay.ParseCustom([]byte(*b.Document), a.customInput(ctx, u.User, console, itemID))
		if !rep.OK() {
			writeJSON(w, 422, map[string]any{"error": "This settings file has mistakes, so nothing was changed.", "report": rep})
			return
		}
		layer, ignored = parsed, rep.Ignored
	default:
		cur, err := read()
		if err != nil {
			a.Log.WarnContext(ctx, "could not read game settings", "cat", "games", "level", b.Level, "console", console, "item", itemID, "user", u.User, "err", err)
			writeErr(w, 500, err.Error())
			return
		}
		layer = cur
		if ig := layer.Patch(b.Values, b.CoreOptions); ig != nil {
			ignored = ig
		}
	}
	if err := write(layer); err != nil {
		a.Log.WarnContext(ctx, "could not save game settings", "cat", "games", "level", b.Level, "console", console, "item", itemID, "user", u.User, "err", err)
		writeErr(w, 500, err.Error())
		return
	}
	a.Log.DebugContext(ctx, "game settings saved", "cat", "games", "kind", "settings", "level", b.Level, "console", console, "item", itemID,
		"how", how, "ignored", len(ignored), "user", u.User)
	a.applyToRunningGame(ctx, u.User, console, itemID)
	view := a.settingsView(ctx, u.User, console, itemID)
	view["ignored"] = ignored
	writeJSON(w, 200, view)
}

// customInput gathers what's known about the core a settings file is for.
func (a *App) customInput(ctx context.Context, userID int64, console string, itemID int64) wittyplay.CustomInput {
	if itemID == 0 && console == "" {
		return wittyplay.CustomInput{}
	}
	list, _ := a.coreOptionsFor(ctx, userID, console, itemID)
	return wittyplay.CustomInput{Offered: list}
}

func handleGameSettingsValidate(a *App, w http.ResponseWriter, r *http.Request) {
	var b struct {
		Document string `json:"document"`
		Console  string `json:"console"`
		Item     int64  `json:"item"`
	}
	if err := readJSON(capBody(w, r, wittyplay.MaxCustomSize*2), &b); err != nil {
		writeErr(w, 400, "this can't be read")
		return
	}
	console, itemID, ok := a.gameScope(r, b.Console, b.Item)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	layer, rep := wittyplay.ParseCustom([]byte(b.Document), a.customInput(r.Context(), who(r).User, console, itemID))
	out := map[string]any{"ok": rep.OK(), "report": rep}
	if layer != nil {
		out["layer"] = layer
	}
	writeJSON(w, 200, out)
}

// handleGameCoreOptions lists what this game's core offers, what's chosen, and anything this
// person has set that it won't take.
func handleGameCoreOptions(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	ctx, user, console := r.Context(), who(r).User, itemConsole(it)
	list, core := a.coreOptionsFor(ctx, user, console, it.ID)
	res := wittyplay.Resolve(a.gameLayers(ctx, user, console, it.ID))
	running := map[string]string{}
	if sess := runningGame(user, it.ID); sess != nil {
		for _, o := range sess.CoreOptions() {
			running[o.Key] = o.Value
		}
	}
	ignored := wittyplay.CheckOptions(res.CoreOptions, list)
	if list == nil {
		list = []wittyplay.OptionInfo{}
	}
	if ignored == nil {
		ignored = []wittyplay.Issue{}
	}
	writeJSON(w, 200, map[string]any{"core": core, "known": len(list) > 0, "options": list, "values": res.CoreOptions, "sources": res.CoreSources, "running": running, "ignored": ignored, "controls": res.Controls})
}
