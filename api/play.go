// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittymodule/service"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/cores"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// Starting and playing a game, moved from the server's api_games.go: getting a game ready
// (prepare), starting it (play), and the game being played (/play/{sid}: what it is, stopping it,
// connecting to its stream, and what a player does to it). How a game plays here (which core,
// its BIOS, the graphics card) is decide.go's PlanGame; this file turns the game's file into one
// a core can open, starts it, and follows it.
//
// The apps call these, so every answer keeps the shape the server gave: docs/GAMING-API.md and
// docs/GAMING-STREAM.md in WittyMediaServer.

func init() {
	addRoutes(func(mux *http.ServeMux, w with) {
		mux.Handle("POST /games/{id}/prepare", w(handleGamePrepare))
		mux.Handle("POST /games/{id}/play", w(handleGamePlay))
		mux.Handle("GET /play/{sid}", w(handlePlayGet))
		mux.Handle("DELETE /play/{sid}", w(handlePlayStop))
		mux.Handle("POST /play/{sid}/stream", w(handlePlayStream))
		mux.Handle("GET /play/{sid}/stream", w(handlePlayStreamWS))
		mux.Handle("POST /play/{sid}/action", w(handlePlayAction))
	})
	// GET /gaming shows how much the unpacked games take, and DELETE /gaming/unpacked empties it.
	unpackedCache = func(a *App) interface {
		Used() int64
		Clear()
	} {
		return a.archives()
	}
}

// rigConsole is the console a game on someone's own PC has (the server's library.RigConsole).
const rigConsole = "rig"

// ---------------------------------------------------------------- the files a core opens

// defaultUnpackGB is the space for unpacked games: room for a good many discs, or the largest
// single disc. An admin can change it (gamingUnpackGB), and free disk space always comes first.
const defaultUnpackGB = 16

var (
	archivesMu sync.Mutex
	archiveSet = map[string]*gamearchive.Cache{} // by WittyPlay's folder: one in a running module
	programs   sync.Mutex                        // the program copied out of a 3DS package, while it's looked at or made
)

// archives is the cache of games unpacked from their archives, in WittyPlay's own folder. What a
// crash or a power cut left half made goes the first time it's needed: no game is running yet.
func (a *App) archives() *gamearchive.Cache {
	archivesMu.Lock()
	c := archiveSet[a.Dir]
	if c == nil {
		_ = os.RemoveAll(filepath.Join(a.Dir, "tmp"))
		c = &gamearchive.Cache{Dir: filepath.Join(a.Dir, "unpacked"), Limit: func() int64 {
			gb, err := strconv.Atoi(a.configText(context.Background(), "gamingUnpackGB", ""))
			if err != nil || gb < 1 {
				gb = defaultUnpackGB
			}
			return int64(gb) << 30
		}}
		archiveSet[a.Dir] = c
	}
	archivesMu.Unlock()
	// Which version of a game an archive holds goes by the owner's order of regions.
	gamearchive.SetRegionOrder(a.configText(context.Background(), "gamingRegionOrder", ""))
	return c
}

// gameDisc is one disc of a game on several.
type gameDisc struct {
	Disc int
	Path string
}

// itemDiscs are a game's discs, in order: the item's files, when the server names more than one
// (it sends each disc of a game on several). Empty for a game on one disc.
func itemDiscs(it *service.Item) []gameDisc {
	if it == nil || len(it.Files) < 2 {
		return nil
	}
	out := make([]gameDisc, len(it.Files))
	for i, f := range it.Files {
		out[i] = gameDisc{Disc: i + 1, Path: f.Path}
	}
	return out
}

// itemEntry is the file in a game's archive someone chose to play, or "" to choose by the rules.
func itemEntry(it *service.Item) string {
	if it == nil || it.Facets == nil {
		return ""
	}
	return it.Facets["entry"]
}

// prepareROM returns the file a core should open. An archive is unpacked first, or already has
// been: see gamearchive.Cache. This waits for that, and cleanup lets the cache have the space
// back once the game has ended.
func (a *App) prepareROM(ctx context.Context, path string, console *wittyplay.Console, entry string) (rom string, cleanup func(), err error) {
	if !gamearchive.IsArchive(path) {
		return path, nil, nil
	}
	return a.archives().AcquireEntry(ctx, path, console, entry)
}

// prepareGame gives the file a core should open: one disc or cartridge, or a list of a game's
// discs. Those can be files, archives, or some of each.
func (a *App) prepareGame(ctx context.Context, it *service.Item, console *wittyplay.Console) (string, func(), error) {
	discs := itemDiscs(it)
	if len(discs) < 2 || !console.Accepts(".m3u") {
		return a.prepareROM(ctx, gamePath(it), console, itemEntry(it))
	}
	return a.prepareDiscs(ctx, discs, console)
}

// prepareDiscs makes a folder holding a playlist of a game's discs, and the discs or links to
// them. A core reads a playlist's names relative to itself, and can't follow one across drives,
// so what's beside it has to be real, or as near as the system allows.
func (a *App) prepareDiscs(ctx context.Context, discs []gameDisc, console *wittyplay.Console) (rom string, cleanup func(), err error) {
	var ends []func()
	finish := func() {
		for _, end := range ends {
			end()
		}
	}
	defer func() {
		if err != nil {
			finish()
		}
	}()
	tmpRoot := filepath.Join(a.Dir, "tmp")
	if err = os.MkdirAll(tmpRoot, 0o700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(tmpRoot, "rom-")
	if err != nil {
		return "", nil, err
	}
	ends = append(ends, func() { _ = os.RemoveAll(dir) })
	// Every disc is made ready at once: unpacking is what takes the time, and the cache unpacks
	// two at a time.
	type ready struct {
		rom     string
		release func()
		err     error
	}
	got := make([]ready, len(discs))
	var wg sync.WaitGroup
	for i, d := range discs {
		wg.Add(1)
		got[i].err = errors.New("this disc could not be made ready")
		go func() {
			defer wg.Done()
			got[i].rom, got[i].release, got[i].err = a.prepareROM(ctx, d.Path, console, "")
		}()
	}
	wg.Wait()
	for _, r := range got {
		if r.release != nil {
			ends = append(ends, r.release)
		}
	}
	var list strings.Builder
	for i, d := range discs {
		if got[i].err != nil {
			return "", nil, got[i].err
		}
		one := got[i].rom
		sub := "disc" + strconv.Itoa(d.Disc)
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			return "", nil, err
		}
		files := append([]string{one}, sheetFiles(one)...)
		for _, f := range files {
			if err := linkOrCopy(f, filepath.Join(dir, sub, filepath.Base(f))); err != nil {
				return "", nil, err
			}
		}
		list.WriteString(sub + "/" + filepath.Base(one) + "\n")
	}
	m3u := filepath.Join(dir, "game.m3u")
	if err := os.WriteFile(m3u, []byte(list.String()), 0o600); err != nil {
		return "", nil, err
	}
	var once sync.Once
	return m3u, func() { once.Do(finish) }, nil
}

func linkOrCopy(from, to string) error {
	if _, err := os.Lstat(to); err == nil {
		return nil // a track two sheets name
	}
	if os.Link(from, to) == nil {
		return nil
	}
	if os.Symlink(from, to) == nil {
		return nil
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

var (
	cueFileRe = regexp.MustCompile(`(?i)^\s*FILE\s+(?:"([^"]+)"|(\S+))\s+[A-Z0-9]+\s*$`)
	gdiLineRe = regexp.MustCompile(`^\s*[0-9]+\s+[0-9]+\s+[0-9]+\s+[0-9]+\s+(?:"([^"]+)"|(\S+))\s+[0-9]+\s*$`)
	tocFileRe = regexp.MustCompile(`(?i)^\s*(?:DATA)?FILE\s+"([^"]+)"`)
)

// sheetFiles are the files a disc's sheet (.cue, .gdi, .ccd, .toc) names in its own folder: the
// tracks that go beside it (the server's gameid.ReadSheet, for its files only). Names leading out
// of the folder, and files that aren't there, are left out. At most 1 MB and 2,000 lines are read.
func sheetFiles(path string) []string {
	ext := strings.ToLower(filepath.Ext(path))
	dir := filepath.Dir(path)
	var out []string
	add := func(raw string) {
		full := filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(raw, `\`, "/")))
		rel, err := filepath.Rel(dir, full)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		if st, err := os.Stat(full); err != nil || !st.Mode().IsRegular() {
			if found := sameIgnoringCase(full); found != "" {
				full = found
			} else {
				return
			}
		}
		out = append(out, full)
	}
	switch ext {
	case ".ccd":
		add(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".img")
		return out
	case ".cue", ".gdi", ".toc":
	default:
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 1<<20))
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for lines := 0; sc.Scan() && lines < 2000; lines++ {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf"))
		var m []string
		switch ext {
		case ".cue":
			m = cueFileRe.FindStringSubmatch(line)
		case ".gdi":
			m = gdiLineRe.FindStringSubmatch(line)
		case ".toc":
			if t := tocFileRe.FindStringSubmatch(line); t != nil {
				m = []string{t[0], t[1], ""}
			}
		}
		if m != nil {
			add(m[1] + m[2])
		}
	}
	return out
}

func sameIgnoringCase(full string) string {
	dir, want := filepath.Dir(full), filepath.Base(full)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), want) && e.Type().IsRegular() {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// prepare3DS checks a 3DS file before a core sees it. An encrypted one is refused: nothing here
// decrypts or looks for keys. An unencrypted install package holds the game as a plain program,
// which is copied out because the cores can't open a package. On failure the earlier cleanup has
// already run. Whether it's refused goes by the bytes where a program's header belongs, then its
// NoCrypto flag, and not by what the package's table of contents says (threeds.go).
func (a *App) prepare3DS(rom string, cleanup func(), console *wittyplay.Console) (string, func(), bool, error) {
	if console.ID != "3ds" {
		return rom, cleanup, false, nil
	}
	fail := func(err error) (string, func(), bool, error) {
		if cleanup != nil {
			cleanup()
		}
		return "", nil, false, err
	}
	info, err := wittyplay.Inspect3DS(rom)
	if err != nil {
		return fail(err)
	}
	if info.Encrypted {
		return fail(wittyplay.ErrEncrypted3DS)
	}
	// A core can't open a package, and Azahar calls a program decrypted without its NoCrypto flag
	// encrypted: each plays from its program, copied out (with the flag set).
	if !info.Package && !info.Unflagged {
		return rom, cleanup, info.NewOnly, nil
	}
	out, err := a.packageProgram(rom, info)
	if err != nil {
		return fail(err)
	}
	return out, cleanup, info.NewOnly, nil
}

// packageProgram gives the program out of a 3DS package, or out of a file decrypted without its
// NoCrypto flag, as a file of its own. A big game's copy takes a long while and as much room as
// the game, so it stays for next time rather than being made at every start. Only the last one
// stays, unless a game is running that may still be using another. The folder is emptied when
// the cache of unpacked games is first made.
func (a *App) packageProgram(rom string, info wittyplay.ThreeDSFile) (string, error) {
	st, err := os.Stat(rom)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.Dir, "tmp", "3ds")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%d", rom, st.Size(), st.ModTime().UnixNano()))
	out := filepath.Join(dir, hex.EncodeToString(sum[:8])+".cxi")
	programs.Lock()
	defer programs.Unlock()
	if have, err := os.Stat(out); err == nil && have.Size() == info.ProgramLen {
		return out, nil
	}
	if rpc.Mgr.Count() == 0 {
		if ents, err := os.ReadDir(dir); err == nil {
			for _, e := range ents {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	part := out + ".part"
	if err := info.CopyProgram(rom, part); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	if err := os.Rename(part, out); err != nil {
		_ = os.Remove(part)
		return "", err
	}
	return out, nil
}

func withOption(options map[string]string, key, value string) map[string]string {
	out := make(map[string]string, len(options)+1)
	for k, v := range options {
		out[k] = v
	}
	out[key] = value
	return out
}

// ---------------------------------------------------------------- getting ready

// handleGamePrepare gets a game ready without waiting for it, and says how that's going, so a
// client can show progress for a slow unpack. Call it again to follow it (about once a second).
// Starting a game works without it: POST play simply takes as long as unpacking does.
func handleGamePrepare(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	c := console(itemConsole(it))
	if c == nil {
		writeErr(w, 404, "not found")
		return
	}
	ctx := r.Context()
	if c.Unavailable != "" && !a.CustomFor(ctx, c.ID) {
		writeErr(w, 409, c.Unavailable)
		return
	}
	// Not on this server says so here too, as play would, before anything is unpacked for it.
	if c.ID != wittyplay.TestConsole {
		if pick := a.PickCore(ctx, c, it.ID); pick.NoGPU != "" {
			GPURefusal(c, pick.NoGPU).Write(w)
			return
		}
	}
	ready := gamearchive.Status{State: "ready"}
	discs := itemDiscs(it)
	anyArchive := gamearchive.IsArchive(gamePath(it))
	for _, d := range discs {
		anyArchive = anyArchive || gamearchive.IsArchive(d.Path)
	}
	if !anyArchive {
		writeJSON(w, 200, ready)
		return
	}
	// Unpacking a disc only to be told no core is installed would be a poor way to learn it.
	if c.ID != wittyplay.TestConsole && a.Cores().ForConsole(c, a.PreferredCore(ctx, c, it.ID)) == nil {
		writeJSON(w, 409, map[string]any{"error": "No core is installed for " + c.Name + ".", "needCore": true, "console": c.ID})
		return
	}
	st := ready
	if gamearchive.IsArchive(gamePath(it)) {
		st = a.archives().PrepareEntry(gamePath(it), c, itemEntry(it))
	}
	if len(discs) > 1 && c.Accepts(".m3u") {
		// Each disc in an archive has to be there. What's asked about is whichever is furthest behind.
		for _, d := range discs {
			if gamearchive.IsArchive(d.Path) {
				if one := a.archives().Prepare(d.Path, c); one.State != "ready" {
					st = one
					break
				}
			}
		}
	}
	if st.Error != "" {
		writeErr(w, 422, st.Error)
		return
	}
	writeJSON(w, 200, st)
}

// ---------------------------------------------------------------- starting

// gamesFull says this person starting a game would go over the limit (gamingMaxSessions). Their
// own game doesn't count, since starting another ends it. It's only there so that nobody unpacks
// or patches a game with nowhere to run it: the manager's own limits are still checked as it starts.
func (a *App) gamesFull(ctx context.Context, userID int64) bool {
	limit, err := strconv.Atoi(a.configText(ctx, "gamingMaxSessions", "2"))
	if err != nil {
		limit = 2
	}
	running := rpc.Mgr.Count()
	if rpc.Mgr.ForUser(userID) != nil {
		running--
	}
	return running >= limit
}

// awaitHardwareTest waits, the first time a game drawn on the graphics card starts, for the
// server's test of which hardware encoders work: long enough for a working one to answer, and no
// longer. The game can start without it.
func (a *App) awaitHardwareTest(ctx context.Context) {
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		t, err := a.Host.Tools(ctx)
		if err != nil || t.HardwareDone {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// consolePlayers is how many players a console takes, for a room's seats (consoles.go's traits).
func consolePlayers(console string) int {
	return consoleTraitsTable[console].Caps.Input.Ports
}

func handleGamePlay(a *App, w http.ResponseWriter, r *http.Request) {
	it, ok := a.personalGame(r)
	if !ok {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Resume   bool   `json:"resume"`
		Client   string `json:"client"`   // browser, or anything else for a native app
		SafeMode bool   `json:"safeMode"` // with default settings, no cheats and no patches
		// Device names what's starting it, for "Continue here" on the account's other devices.
		Device string `json:"device"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if itemConsole(it) == rigConsole {
		// A rig's game runs on that PC, not here: see POST /rigs/games/{id}/play.
		writeJSON(w, 409, map[string]any{"error": "This game plays on your own PC, not on this server.", "rig": true})
		return
	}
	ctx := r.Context()
	began := time.Now()
	plan, no := a.PlanGame(ctx, it)
	if no != nil {
		no.Write(w)
		return
	}
	c := plan.Console
	if err := os.MkdirAll(plan.SystemDir, 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if plan.GPU {
		a.awaitHardwareTest(ctx)
	}
	if err := a.Configure(ctx); err != nil {
		writeErr(w, 503, err.Error())
		return
	}
	if st, err := os.Stat(gamePath(it)); err != nil || st.IsDir() {
		writeErr(w, 404, "the game file is missing")
		return
	}
	user := who(r).User
	// Full is full, whatever the game. Unpacking it, copying it and applying its patches would
	// all be for nothing.
	if a.gamesFull(ctx, user) {
		writeErr(w, 503, stream.ErrBusy.Error())
		return
	}
	rom, cleanup, err := a.prepareGame(ctx, it, c)
	if err != nil {
		if ctx.Err() != nil {
			return // whoever asked has gone; the unpack carries on for next time
		}
		writeErr(w, 422, err.Error())
		return
	}
	rom, cleanup, newOnly, err := a.prepare3DS(rom, cleanup, c)
	if err != nil {
		if errors.Is(err, wittyplay.ErrEncrypted3DS) {
			writeJSON(w, 422, map[string]any{"error": encryptedMessage(), "encrypted": true, "console": c.ID})
			return
		}
		writeErr(w, 422, err.Error())
		return
	}
	done := func() {
		if cleanup != nil {
			cleanup()
		}
	}
	if why := wittyplay.Refusal(rom, c.ID); why != "" {
		done()
		writeErr(w, 422, why)
		return
	}
	if c.ID == "snes" && wittyplay.IsSatellaview(rom) {
		// No other core will load one, so nothing else is tried.
		core := a.Cores().AllForConsole(c, wittyplay.SatellaviewCore)
		problem := ""
		if _, err := os.Stat(filepath.Join(plan.SystemDir, wittyplay.SatellaviewBIOS)); err != nil {
			problem = "This is a Satellaview broadcast game, which needs the receiver's cartridge. Put a copy of yours named " + wittyplay.SatellaviewBIOS + " in a folder named " + wittyplay.BIOSFolderName + " beside your " + c.Name + " games, and it will be found when you play. BIOS files are never downloaded for you."
		} else if len(core) == 0 || core[0].ID != wittyplay.SatellaviewCore {
			problem = "This is a Satellaview broadcast game, which only plays on Mesen-S. An admin can install that core in Settings, Consoles & cores."
		}
		if problem != "" {
			done()
			writeJSON(w, 409, map[string]any{"error": problem, "console": c.ID, "file": wittyplay.SatellaviewBIOS})
			return
		}
		a.PlanOnly(ctx, plan, core[0])
	}
	// A custom core only runs where an administrator put it, so it's left to them.
	if c.ID == "n64" && (plan.Core == nil || plan.Core.Source != cores.SourceCustom) && wittyplay.IsLibdragon(rom) {
		core := a.Cores().AllForConsole(c, wittyplay.LibdragonCore)
		if len(core) == 0 || core[0].ID != wittyplay.LibdragonCore {
			done()
			writeJSON(w, 409, map[string]any{"error": wittyplay.LibdragonNeedsCore, "otherCore": true, "console": c.ID})
			return
		}
		// It runs on the processor alone: WittyPlay gives it the settings for that.
		a.PlanOnly(ctx, plan, core[0])
	}
	if newOnly {
		// A game made for the New 3DS won't start on the older model, which is what cores are out
		// of the box. A core with no such option refuses the key and carries on.
		plan.CoreOptions = withOption(plan.CoreOptions, wittyplay.NewThreeDSOption, wittyplay.NewThreeDSValue)
		for i := range plan.Fallbacks {
			plan.Fallbacks[i].Options = withOption(plan.Fallbacks[i].Options, wittyplay.NewThreeDSOption, wittyplay.NewThreeDSValue)
		}
	}
	a.Log.DebugContext(ctx, "wittyplay: game prepared", "cat", "games", "item", it.ID, "console", c.ID, "core", plan.CoreID, "seconds", time.Since(began).Seconds())
	// The server counts it from here: its screen time, play time and "playing now". If it says no,
	// nothing starts.
	hid, err := a.beginSession(r, it.ID, it.Title)
	if err != nil {
		done()
		if errors.Is(err, errSessionRefused) {
			writeErr(w, 403, err.Error())
			return
		}
		writeErr(w, 503, "This game could not start: the server didn't answer. Try again.")
		return
	}
	opts := stream.Options{
		UserID: user, ItemID: it.ID, Title: it.Title, Console: c.ID, Core: plan.CorePath, ROM: rom, SystemDir: plan.SystemDir,
		CoreAssets: plan.CoreAssets, LoadLimit: plan.LoadLimit,
		SaveDir: a.saveDir(user, it.ID), Resume: b.Resume, Quality: a.configText(ctx, "gamingQuality", ""), Cleanup: cleanup,
		GPU: plan.GPU, RunnerArgs: plan.RunnerArgs, CoreID: plan.CoreID, CoreOptions: plan.CoreOptions, Fallbacks: plan.Fallbacks,
		PreferDevices: wittyplay.Pad(c.ID).PreferDevices,
	}
	a.applyGameSettings(ctx, &opts, b.SafeMode)
	if plan.Software {
		// Over whatever anyone chose: without these, the core asks for a graphics card that isn't here.
		opts.CoreOptions = overOptions(opts.CoreOptions, plan.CPUOptions)
	}
	opts.Notices = append(opts.Notices, plan.Notices...)
	sess, err := rpc.Mgr.Start(opts)
	if err != nil {
		if opts.Cleanup != nil {
			opts.Cleanup() // it may never have got as far as a session that would
		}
		a.abandonSession(hid)
		if errors.Is(err, stream.ErrBusy) || errors.Is(err, stream.ErrGPUBusy) {
			writeErr(w, 503, err.Error())
			return
		}
		a.Log.WarnContext(ctx, "wittyplay: could not start a game", "cat", "games", "item", it.ID, "game", it.Title, "console", c.ID, "core", plan.CoreID, "err", err)
		// Recorded against the core, so that one console that can't run here never takes every
		// other game off the server with it.
		a.CoreStartFailed(ctx, plan, it.ID, host.For(r), err)
		if no := StartRefusal(c, err); no != nil {
			no.Write(w)
			return
		}
		writeErr(w, 500, "This game could not start. "+err.Error())
		return
	}
	a.followSession(hid, sess.ID(), user, it.ID, sess.Done(), sess.Stop)
	// A game started: this version of WittyPlay works, which clears the server's count of its failures.
	go func() { _ = a.Host.Report(context.Background(), host.Report{Working: true}) }()
	a.Log.InfoContext(ctx, "wittyplay: game started", "cat", "games", "session", sess.ID(), "item", it.ID, "game", it.Title, "console", c.ID, "core", plan.CoreID,
		"gpu", plan.GPU, "software", plan.Software, "resume", b.Resume)
	if plan.Core != nil {
		on := host.For(r)
		go a.WatchCorePlay(sess, plan.Core, on, it.ID)
	}
	hostName := a.personName(ctx, user)
	noteGameStarted(sess, user, hostName, deviceName(b.Device, r), seatGame{Item: it.ID, Title: it.Title, Console: c.ID})
	if err := a.noteGamePlayed(ctx, user, it.ID); err != nil {
		a.Log.WarnContext(ctx, "wittyplay: recording a play", "cat", "games", "session", sess.ID(), "err", err)
	}
	info := sess.Info()
	info.Modes = rpc.Mgr.Modes(gameClient(b.Client))
	writeJSON(w, 201, info)
}

// ---------------------------------------------------------------- the game being played

func handlePlayGet(a *App, w http.ResponseWriter, r *http.Request) {
	sess := watchSession(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	info := sess.Info()
	info.Modes = rpc.Mgr.Modes(gameClient(r.URL.Query().Get("client")))
	writeJSON(w, 200, info)
}

// handlePlayStop ends a game. It answers once the game has saved and gone.
func handlePlayStop(a *App, w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	sess.Stop()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// streamRefused answers a refused connection: docs/GAMING-STREAM.md, Errors.
func streamRefused(w http.ResponseWriter, status int, code, text string) {
	writeJSON(w, status, map[string]string{"error": text, "code": code})
}

// peers limits connections to each game (peers.go).
var peers peerLimits

// handlePlayStream connects a device over WebRTC: its hello and its offer in, what it gets and
// the answer out.
func handlePlayStream(a *App, w http.ResponseWriter, r *http.Request) {
	sess := watchSession(r)
	if sess == nil {
		streamRefused(w, 404, "no-session", "No such game is being played.")
		return
	}
	if !peers.offer(sess.ID(), time.Now()) {
		w.Header().Set("Retry-After", "60")
		streamRefused(w, 429, "busy", "Too many connections to this game. Try again in a minute.")
		return
	}
	var b struct {
		Hello stream.Hello `json:"hello"`
		SDP   string       `json:"sdp"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, stream.MaxOfferSize+8<<10)
	if err := readJSON(r, &b); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			streamRefused(w, 413, "too-large", "The connection offer is too large.")
			return
		}
		streamRefused(w, 400, "bad-hello", "The body must be {\"hello\": {...}, \"sdp\": \"...\"}.")
		return
	}
	if b.Hello.V != stream.StreamVersion {
		streamRefused(w, 400, "bad-hello", fmt.Sprintf("This server speaks version %d of the stream.", stream.StreamVersion))
		return
	}
	// Someone who joined with a code drives the seat it gave them (seats.go).
	if sess.UserID() != who(r).User {
		if seat, ok := seats.seatOf(sess.ID(), who(r).User); ok {
			b.Hello.Seats = joinedSeats(seat)
		}
	}
	got, err := sess.Connect(r.Context(), b.Hello, b.SDP, awayFromHome(r))
	if err != nil {
		var se *stream.StreamError
		if errors.As(err, &se) && se.Status >= 400 && se.Status < 600 {
			streamRefused(w, se.Status, se.Code, se.Text)
			return
		}
		a.Log.WarnContext(r.Context(), "wittyplay: could not connect to a game", "cat", "games", "session", sess.ID(), "err", err)
		streamRefused(w, 500, "failed", err.Error())
		return
	}
	writeJSON(w, 200, got)
}

// handlePlayStreamWS is the stream's fallback: the same messages on a WebSocket.
func handlePlayStreamWS(a *App, w http.ResponseWriter, r *http.Request) {
	// Someone who joined with a code may connect here too. The hello on this socket goes straight
	// to the stream, so their seat is the one their app asks for: GAMING-API.md, Seats.
	sess := watchSession(r)
	if sess == nil {
		streamRefused(w, 404, "no-session", "No such game is being played.")
		return
	}
	release, ok := peers.socket(sess.ID(), time.Now())
	if !ok {
		w.Header().Set("Retry-After", "10")
		streamRefused(w, 429, "busy", "Too many connections to this game. Try again in a minute.")
		return
	}
	defer release()
	sess.ServeStream(w, r, awayFromHome(r))
}

func handlePlayAction(a *App, w http.ResponseWriter, r *http.Request) {
	sess := session(r)
	if sess == nil {
		writeErr(w, 404, "not found")
		return
	}
	var b struct {
		Type   string  `json:"type"`
		Slot   int     `json:"slot"`
		On     bool    `json:"on"`
		Value  float64 `json:"value"`
		Key    string  `json:"key"`
		Text   string  `json:"text"`
		Port   int     `json:"port"`
		Device uint32  `json:"device"`
	}
	if err := readJSON(r, &b); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	var err error
	switch b.Type {
	case "save":
		if b.Slot < 1 {
			err = stream.ErrNoSlot // the automatic slot is written on stop
		} else {
			err = sess.SaveState(r.Context(), b.Slot)
		}
	case "load":
		err = sess.LoadState(b.Slot)
	case "reset":
		err = sess.Reset()
	case "pause":
		err = sess.Pause(b.On)
	case "disc":
		err = sess.ChangeDisc(b.Slot)
	case "device":
		err = sess.SetDevice(b.Port, b.Device)
	case "speed":
		var held float64
		if held, err = sess.SetSpeed(b.Value); err == nil {
			writeJSON(w, 200, map[string]any{"ok": true, "speed": held})
			return
		}
	case "rewind":
		err = sess.SetRewinding(b.On)
	case "advance":
		err = sess.Advance(max(int(b.Value), 1))
	case "undo":
		err = sess.Undo(r.Context())
	case "option":
		err = sess.SetOption(b.Key, b.Text)
	default:
		writeErr(w, 400, "unknown action")
		return
	}
	if err != nil {
		writeErr(w, 422, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
