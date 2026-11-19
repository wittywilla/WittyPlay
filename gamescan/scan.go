// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

// Package gamescan is what a scan of a games library finds (module API 5, scan.identify): the
// server walks the library's folders and sends WittyPlay every file; this decides which are
// games, for which console, and which files are one game (a sheet and its tracks, the discs of a
// set, copies of one file, versions of one game). See docs/GAMING.md in the server.
//
// ROM files are only listed here, never opened beyond a look at their headers and at which
// files an archive, .cue or .m3u names. Archives are only read in a worker process.
package gamescan

import (
	"context"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wittywilla/wittymodule"
	"github.com/wittywilla/wittyplay"
	"github.com/wittywilla/wittyplay/gamearchive"
	"github.com/wittywilla/wittyplay/gameid"
)

// gameSkipDirs hold things that belong to an emulator rather than games.
var gameSkipDirs = map[string]bool{"bios": true, "system": true, "saves": true, "savestates": true, "states": true, "screenshots": true,
	"boxart": true, "covers": true, "cover": true, "images": true, "media": true, "manuals": true, "__macosx": true, "@eadir": true}

// dumpGame is the file that stands for a whole game kept as a folder, when dir is one: a PS3
// disc's PS3_DISC.SFB beside its PS3_GAME folder, or a Wii U game's code/*.rpx beside its meta
// folder. Names are matched in any case.
func dumpGame(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 64 {
		return "" // a dump's root holds a handful of things; a folder of games holds more
	}
	var sfb, ps3Game, code, meta string
	for _, e := range entries {
		switch n := strings.ToLower(e.Name()); {
		case n == "ps3_disc.sfb" && !e.IsDir():
			sfb = e.Name()
		case n == "ps3_game" && e.IsDir():
			ps3Game = e.Name()
		case n == "code" && e.IsDir():
			code = e.Name()
		case n == "meta" && e.IsDir():
			meta = e.Name()
		}
	}
	if sfb != "" && ps3Game != "" {
		return filepath.Join(dir, sfb)
	}
	if code != "" && meta != "" {
		list, err := os.ReadDir(filepath.Join(dir, code))
		if err != nil {
			return ""
		}
		for _, e := range list {
			if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".rpx") {
				return filepath.Join(dir, code, e.Name())
			}
		}
	}
	return ""
}

// candidate is a game a scan found: its file (the first disc's, for a game on several), and its
// discs when it has more than one.
type candidate struct {
	path        string
	kind        string
	size, mtime int64
	console     string
	verdict     *gameVerdict
	discs       []gameDisc
}

// scanned is a file the server sent, and what was made of it.
type scanned struct {
	path, root, rel string
	size, mtime     int64
	v               gameVerdict
	judged          bool // v is this scan's verdict on it
	ignored         bool // a person said it isn't a game
}

// archiveIndex is one scan of one library: what's known about its files, remembered so that a
// scan reads a file once and never again until it changes. Nil db: remembers nothing past it.
type archiveIndex struct {
	db        *sql.DB
	libraryID int64
	lister    *gamearchive.Lister
	// reading, when set, is told about each archive that really has to be opened: on a
	// slow network folder the first scan can take a while, and this shows it's moving.
	reading func(path string)

	verdictOnce   sync.Once
	verdicts      map[string]cachedVerdict
	seenVerdicts  map[string]cachedVerdict
	freshVerdicts map[string]bool
	decisions     map[string]gameDecision
	notes         []gameFileNote
	lastLibrary   string
	peekedAt      map[string]bool
	told          map[string]bool

	// What the server has sent so far, in the order its walk found it.
	files  []*scanned
	sent   map[string]bool
	hidden map[string]map[string]bool // by root: what a sheet names, in lower case
	dumps  map[string]string          // a folder's dumpGame, once asked

	// onFile, when set, is told how many files of the batch add is taking in are done.
	onFile func(done int)
	// onGame, when set, is told the console of each file add judges to be a game.
	onGame func(console string)
}

func newArchiveIndex(db *sql.DB, libraryID int64) *archiveIndex {
	return &archiveIndex{db: db, libraryID: libraryID, seenVerdicts: map[string]cachedVerdict{}, freshVerdicts: map[string]bool{},
		peekedAt: map[string]bool{}, told: map[string]bool{}, sent: map[string]bool{}, hidden: map[string]map[string]bool{}, dumps: map[string]string{}}
}

func (ix *archiveIndex) close() {
	if ix.lister != nil {
		ix.lister.Close()
		ix.lister = nil
	}
}

func (ix *archiveIndex) open(path string) *gamearchive.Lister {
	if ix.lister == nil {
		ix.lister = gamearchive.NewLister()
	}
	if ix.reading != nil && !ix.told[path] {
		ix.told[path] = true
		ix.reading(path)
	}
	return ix.lister
}

// listing reads what an archive holds, or says why it can't be read.
func (ix *archiveIndex) listing(path string) (*gamearchive.Listing, string) {
	l, err := ix.open(path).List(path, gamearchive.ScanBudget)
	if err != nil {
		code := gamearchive.CodeOf(err)
		if code == "" {
			code = gamearchive.ErrDamaged.Code
		}
		return nil, code
	}
	return l, ""
}

// peek looks at the start of one file in an archive. A file that can't be reached says
// nothing, and doesn't count against the archive: see peeked.
func (ix *archiveIndex) peek(path string, e gamearchive.Entry) gameid.Finding {
	b, err := ix.open(path).Peek(path, e.Name)
	if err != nil {
		return gameid.Finding{}
	}
	ix.peekedAt[path] = true
	size := e.Size
	if size <= 0 {
		size = int64(len(b))
	}
	return gameid.SniffHead(b, size)
}

// peeked says a look inside this archive really did read something.
func (ix *archiveIndex) peeked(path string) bool { return ix.peekedAt[path] }

// ---------------------------------------------------------------- taking files in

// wanted says a file the server sent is one to look at, and where it sits under its root. The
// server sends every file but what's under a name starting "." or "$"; folders that belong to an
// emulator, and everything inside a game kept as a folder but the one file that stands for it,
// are left out here, as the server's own walk used to.
func (ix *archiveIndex) wanted(f wittymodule.ScanFile) (rel string, ok bool) {
	root := filepath.Clean(f.Root)
	p := filepath.Clean(f.Path)
	rel, err := filepath.Rel(root, p)
	if err != nil || !filepath.IsLocal(rel) {
		return "", false
	}
	if strings.HasPrefix(filepath.Base(p), ".") {
		return "", false
	}
	// Each folder on the way down, as the walk met it: one that's skipped, or a dump whose one
	// file this is (or isn't).
	dir := root
	parts := strings.Split(filepath.Dir(rel), string(filepath.Separator))
	if filepath.Dir(rel) == "." {
		parts = nil
	}
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			name := parts[i]
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") || gameSkipDirs[strings.ToLower(name)] {
				return "", false
			}
			dir = filepath.Join(dir, name)
		}
		game, asked := ix.dumps[dir]
		if !asked {
			game = dumpGame(dir)
			ix.dumps[dir] = game
		}
		if game != "" {
			return rel, game == p
		}
	}
	// A link to a file is only followed when it stays inside the library's folder.
	st, err := os.Lstat(p)
	if err != nil {
		return "", false
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		if !resolvesInside(p, root) {
			return "", false
		}
	} else if !st.Mode().IsRegular() {
		return "", false
	}
	return rel, true
}

// resolvesInside says where a link leads is inside root.
func resolvesInside(p, root string) bool {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	r, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(r, real)
	if err != nil || !filepath.IsLocal(rel) {
		return false
	}
	st, err := os.Stat(real)
	return err == nil && st.Mode().IsRegular()
}

func hiddenKey(p string) string { return strings.ToLower(filepath.Clean(p)) }

// add takes in a batch of the files the server's walk found, judging each as it comes. It stops
// between files, never within one, when ctx ends: a file not looked at must not be judged, as
// these verdicts are saved and believed by every scan after this one.
func (ix *archiveIndex) add(ctx context.Context, console string, files []wittymodule.ScanFile) error {
	ix.verdictOnce.Do(ix.loadVerdicts)
	ix.lastLibrary = console
	for i, f := range files {
		if ctx.Err() != nil {
			ix.saveVerdicts(context.Background())
			return ctx.Err()
		}
		if ix.onFile != nil && i > 0 {
			ix.onFile(i) // the files before this one are judged
		}
		if ix.sent[f.Path] {
			continue
		}
		rel, ok := ix.wanted(f)
		if !ok {
			continue
		}
		ix.sent[f.Path] = true
		s := &scanned{path: f.Path, root: filepath.Clean(f.Root), rel: rel, size: f.Size, mtime: f.MTime}
		// What a sheet names is part of what it describes, whatever else it might be.
		switch strings.ToLower(filepath.Ext(f.Path)) {
		case ".cue", ".m3u", ".gdi", ".ccd", ".toc":
			if sheet, err := gameid.ReadSheet(f.Path); err == nil {
				h := ix.hidden[s.root]
				if h == nil {
					h = map[string]bool{}
					ix.hidden[s.root] = h
				}
				for _, named := range sheet.Files {
					h[hiddenKey(named)] = true
				}
			}
		}
		ix.files = append(ix.files, s)
		if ix.hidden[s.root][hiddenKey(s.path)] {
			continue // part of a sheet already seen
		}
		ix.judgeScanned(s, console)
		if ix.onGame != nil && s.judged && s.v.isGame() {
			ix.onGame(s.v.Console)
		}
	}
	return nil
}

// judgeScanned decides about one file the server sent: what a person said, or its verdict.
func (ix *archiveIndex) judgeScanned(s *scanned, console string) {
	d, decided := ix.decisions[s.path]
	if decided && d.Action == "ignore" {
		s.ignored = true
		return
	}
	switch {
	case decided && d.Action == "add" && consoles().byID(d.Console) != nil:
		s.v = accept(d.Console, howChosen, 100, "You said what this is.")
	default:
		s.v = ix.verdictFor(s.path, s.rel, console, s.size, s.mtime)
	}
	s.judged = true
}

// games works out the games under one root from the files sent: multi-file games become one,
// as a .cue hides the tracks it names and an .m3u the discs it names, whatever the case of the
// names in it; then copies and discs (mergeGames).
func (ix *archiveIndex) games(root, console string) []candidate {
	return ix.mergeGames(ix.gameFiles(root, console, nil), nil)
}

// gameFiles is the first half of games: the files under one root that are games, each on its
// own. step, when set, is told about each file of the root looked at.
func (ix *archiveIndex) gameFiles(root, console string, step func()) []*gameFile {
	var found []*gameFile
	for _, f := range ix.files {
		if f.root != root {
			continue
		}
		if step != nil {
			step()
		}
		if ix.hidden[root][hiddenKey(f.path)] {
			// A sheet that came later names it: it was never a file of its own.
			key := console + "|" + f.path
			delete(ix.seenVerdicts, key)
			delete(ix.freshVerdicts, key)
			continue
		}
		if f.ignored {
			ix.noteFile(f.path, f.size, reviewIgnored, "You said this isn't a game.", "", nil)
			continue
		}
		if !f.judged {
			continue
		}
		v := f.v
		if !v.isGame() {
			if v.Left != "" {
				ix.noteFile(f.path, f.size, v.Left, v.Why, "", v.Suggest)
			}
			continue
		}
		c := &candidate{path: f.path, kind: "game", size: f.size, mtime: f.mtime, console: v.Console, verdict: &v}
		if v.Payload == 0 {
			v.Payload = sizeOf(mainFileOf(f.path))
		}
		g := &gameFile{cand: c, verdict: v, dir: filepath.Dir(f.path)}
		g.key, g.disc, g.total = gameKey(fileBaseName(f.path, v))
		if g.disc == 0 {
			// Discs are often kept in folders of their own: Game/Disc 2/game.cue.
			if parent := filepath.Base(g.dir); parent != "." {
				if disc, total, rest := gameid.DiscIn(parent); disc > 0 {
					g.disc, g.total = disc, total
					g.key, _, _ = gameKey(rest)
					g.dir = filepath.Dir(g.dir)
				}
			}
		}
		found = append(found, g)
		if v.Problem != "" {
			ix.noteFile(f.path, f.size, reviewBlocked, problemText(v)+" "+v.Proof, v.Console, nil)
		}
	}
	return found
}

// DecryptedCopyPlays is what a person with an encrypted game can do: nothing here decrypts one,
// or looks for keys, but a copy they decrypted themselves plays like any other game.
const DecryptedCopyPlays = "A copy you decrypted yourself, from a console you own, will play."

// problemText says why a game on the list can't play as it is.
func problemText(v gameVerdict) string {
	switch v.Problem {
	case "encrypted":
		return "This game file is encrypted, as it came from the console or the store. It can't be played here, and this server won't decrypt it. " + DecryptedCopyPlays
	}
	return v.Proof
}

// ProblemText says why a game can't play as it is, from what the scan found ("encrypted").
func ProblemText(problem, proof string) string {
	return problemText(gameVerdict{Problem: problem, Proof: proof})
}

// gameFileTitle reads a title from a file's name. A disc's number isn't part of its
// title, and neither is the way a web address writes it. What's inside says more than the
// archive's name does about its region, as sets name archives after the game alone.
func gameFileTitle(stem string, v gameVerdict) (title, tags string) {
	disc, _, rest := gameid.DiscIn(stem)
	if gameid.IsSlug(stem) {
		rest = titleCase(gameid.Unslug(rest))
	}
	title, tags = CleanName(rest)
	if tags == "" && v.Entry != "" {
		_, tags = CleanName(strings.TrimSuffix(pathBase(v.Entry), pathExt(v.Entry)))
	}
	if tags == "" {
		tags = v.Region
	}
	if disc > 0 && tags == "" {
		tags = "Disc " + itoa(int64(disc))
	}
	return title, tags
}

func pathBase(s string) string { return filepath.Base(filepath.FromSlash(s)) }
func pathExt(s string) string  { return filepath.Ext(filepath.FromSlash(s)) }

// ---------------------------------------------------------------- remembering

// loadVerdicts reads what the last scan decided about each file, and what people said.
func (ix *archiveIndex) loadVerdicts() {
	ix.verdicts = map[string]cachedVerdict{}
	ix.decisions = map[string]gameDecision{}
	if ix.db == nil {
		return
	}
	ctx := context.Background()
	rows, err := ix.db.QueryContext(ctx, `SELECT path, console, size, mtime, version, verdict FROM game_files WHERE library = ?`, ix.libraryID)
	if err == nil {
		for rows.Next() {
			var p, console, raw string
			var c cachedVerdict
			var ver int
			if rows.Scan(&p, &console, &c.size, &c.mtime, &ver, &raw) == nil && ver == gameJudgeVersion && json.Unmarshal([]byte(raw), &c.v) == nil {
				ix.verdicts[console+"|"+p] = c
			}
		}
		rows.Close()
	}
	dec, err := ix.db.QueryContext(ctx, `SELECT path, action, console FROM game_decisions WHERE library = ?`, ix.libraryID)
	if err == nil {
		for dec.Next() {
			var p string
			var d gameDecision
			if dec.Scan(&p, &d.Action, &d.Console) == nil {
				ix.decisions[p] = d
			}
		}
		dec.Close()
	}
}

type cachedVerdict struct {
	size, mtime int64
	v           gameVerdict
}

// gameDecision is what a person said about a file.
type gameDecision struct {
	Action  string // add, ignore, or own (never treat as a version of another)
	Console string
}

// verdictFor gives the verdict on a file, working it out only if it's new or changed.
func (ix *archiveIndex) verdictFor(p, rel, library string, size, mtime int64) gameVerdict {
	ix.verdictOnce.Do(ix.loadVerdicts)
	key := library + "|" + p
	if c, ok := ix.verdicts[key]; ok && c.size == size && c.mtime == mtime {
		ix.seenVerdicts[key] = c
		return c.v
	}
	v := ix.judge(p, rel, library, size)
	c := cachedVerdict{size: size, mtime: mtime, v: v}
	ix.seenVerdicts[key] = c
	ix.freshVerdicts[key] = true
	if len(ix.freshVerdicts) >= 200 {
		ix.saveVerdicts(context.Background())
	}
	return v
}

// saveVerdicts keeps what's been worked out, as it goes, so that a scan that's stopped
// on a huge library carries on from where it got to.
func (ix *archiveIndex) saveVerdicts(ctx context.Context) {
	if ix.db == nil || len(ix.freshVerdicts) == 0 {
		return
	}
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	for key := range ix.freshVerdicts {
		c := ix.seenVerdicts[key]
		b, err := json.Marshal(c.v)
		if err != nil {
			continue
		}
		console, p, _ := strings.Cut(key, "|")
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_files(library, path, console, size, mtime, version, verdict) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(library, path) DO UPDATE SET console = excluded.console, size = excluded.size, mtime = excluded.mtime, version = excluded.version, verdict = excluded.verdict`,
			ix.libraryID, p, console, c.size, c.mtime, gameJudgeVersion, string(b)); err != nil {
			return
		}
	}
	if tx.Commit() == nil {
		ix.freshVerdicts = map[string]bool{}
	}
}

// forgetVerdicts drops what's kept about files that have gone: every folder could be reached,
// so what wasn't seen is really gone.
func (ix *archiveIndex) forgetVerdicts(ctx context.Context) {
	if ix.db == nil {
		return
	}
	for key := range ix.verdicts {
		if _, ok := ix.seenVerdicts[key]; !ok {
			console, p, _ := strings.Cut(key, "|")
			_, _ = ix.db.ExecContext(ctx, `DELETE FROM game_files WHERE library = ? AND path = ? AND console = ?`, ix.libraryID, p, console)
		}
	}
}

// save keeps what this scan worked out. prune: every folder was reachable, so a file
// that wasn't seen is gone.
func (ix *archiveIndex) save(ctx context.Context, prune bool) {
	ix.verdictOnce.Do(ix.loadVerdicts)
	ix.saveVerdicts(ctx)
	if prune {
		ix.forgetVerdicts(ctx)
	}
}

// ---------------------------------------------------------------- the review list

// gameFileNote is one file a scan didn't list as a game, or listed with something to say.
type gameFileNote struct {
	path    string
	size    int64
	kind    string
	reason  string
	suggest []string
	console string
}

// noteFile adds to what this scan has to say.
func (ix *archiveIndex) noteFile(p string, size int64, kind, reason, console string, suggest []string) {
	ix.notes = append(ix.notes, gameFileNote{path: p, size: size, kind: kind, reason: reason, suggest: suggest, console: console})
}

// saveNotes replaces what the last scan of these folders had to say. keepUnreachable: some
// folders couldn't be read, so what was said about their files stays.
func (ix *archiveIndex) saveNotes(ctx context.Context, keepUnreachable bool) error {
	if ix.db == nil {
		return nil
	}
	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if !keepUnreachable {
		if _, err := tx.ExecContext(ctx, `DELETE FROM game_review WHERE library = ?`, ix.libraryID); err != nil {
			return err
		}
	}
	for _, n := range ix.notes {
		b, _ := json.Marshal(n.suggest)
		if n.suggest == nil {
			b = []byte("[]")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_review(library, path, size, kind, reason, suggest, console) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(library, path) DO UPDATE SET size = excluded.size, kind = excluded.kind, reason = excluded.reason, suggest = excluded.suggest, console = excluded.console`,
			ix.libraryID, n.path, n.size, n.kind, n.reason, string(b), n.console); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---------------------------------------------------------------- one scan

// Scan is one scan of one library, from its first batch of files to its last call (Done).
type Scan struct {
	ID      string
	Library wittymodule.ScanLibrary
	Started time.Time

	// Report, when set, is told how far the scan has got whenever that moves: the files of
	// a batch judged, of the scan's total, and then Finish's work, also counted in files; with
	// the name of the console it's sorting games into now, or "". It's called often, from the
	// call's goroutine, and has to be quick (it's for the server's progress and its watch for a
	// stuck scan: see api/scan_progress.go).
	Report func(done, total int, console string)

	mu       sync.Mutex
	ix       *archiveIndex
	db       *sql.DB
	total    atomic.Int64
	received atomic.Int64
	current  atomic.Value // string
	last     time.Time

	// What the files judged so far are: how many games for each console, and the console of the
	// latest. Kept apart from mu, which a batch holds the whole time it's judged, so the pages
	// can show the consoles as they're found.
	foundMu sync.Mutex
	found   map[string]int
	sorting string
}

// Begin starts a scan of a library. db may be nil: nothing is remembered past the scan.
func Begin(db *sql.DB, id string, lib wittymodule.ScanLibrary) *Scan {
	freshConsoles()
	ix := newArchiveIndex(db, lib.ID)
	s := &Scan{ID: id, Library: lib, Started: time.Now(), ix: ix, db: db, last: time.Now()}
	ix.reading = func(path string) { s.current.Store(filepath.Base(path)) }
	ix.onGame = func(console string) {
		s.foundMu.Lock()
		if s.found == nil {
			s.found = map[string]int{}
		}
		s.found[console]++
		s.sorting = console
		s.foundMu.Unlock()
	}
	return s
}

// libraryConsole is the console the library is held to, or Auto.
func (s *Scan) libraryConsole() string {
	if s.Library.Facet == "" {
		return wittyplay.Auto
	}
	return s.Library.Facet
}

// Add takes in a batch of files, in the order the walk found them. total is how many the walk
// found in all, when it says.
func (s *Scan) Add(ctx context.Context, files []wittymodule.ScanFile, total int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	if total > 0 {
		s.total.Store(int64(total))
	}
	base := int(s.received.Load())
	all := max(int(s.total.Load()), base+len(files))
	if s.Report != nil {
		s.ix.onFile = func(done int) { s.Report(base+done, all, s.sortingName()) }
		defer func() { s.ix.onFile = nil }()
	}
	err := s.ix.add(ctx, s.libraryConsole(), files)
	s.received.Add(int64(len(files)))
	if s.Report != nil && err == nil {
		s.Report(base+len(files), all, s.sortingName())
	}
	return err
}

// Found is how many games the files judged so far hold for each console, and in all: a count
// for the pages to show while the scan goes on, before the games are put together (a game on
// several discs counts once for each until then).
func (s *Scan) Found() (byConsole map[string]int, all int) {
	s.foundMu.Lock()
	defer s.foundMu.Unlock()
	byConsole = make(map[string]int, len(s.found))
	for c, n := range s.found {
		byConsole[c] = n
		all += n
	}
	return byConsole, all
}

// sortingName is the name of the console the latest game judged is for, or "".
func (s *Scan) sortingName() string {
	s.foundMu.Lock()
	c := s.sorting
	s.foundMu.Unlock()
	if c == "" {
		return ""
	}
	return consoleName(c)
}

// finishing counts Finish's work in files, for Report: each file looked at as games are put
// together, then each file read for its checksum, which a big library of loose discs can take
// a while over. The second count is only known once the first is done, and is added to the
// total then.
type finishing struct {
	report      func(done, total int, console string)
	done, total int
}

func (f *finishing) step() {
	f.done++
	if f.report != nil {
		f.report(f.done, f.total, "")
	}
}

func (f *finishing) more(n int) { f.total += n }

// Progress says how far the scan has got: the files received, of how many, and the archive
// being read.
func (s *Scan) Progress() (done, total int, current string) {
	current, _ = s.current.Load().(string)
	return int(s.received.Load()), int(s.total.Load()), current
}

// LastHeard is when the server last sent this scan something.
func (s *Scan) LastHeard() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Close lets go of what the scan holds open (its archive reader).
func (s *Scan) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ix.close()
}

// listedGame is a game the scan answers, with which game it's another version of.
type listedGame struct {
	c            candidate
	versionOf    string
	versionLabel string
}

// Finish works out every game the library holds now, keeps what the scan learned, and answers
// the server. offline are folders that couldn't be read this time: what's known about their
// files stays.
func (s *Scan) Finish(ctx context.Context, offline []string) (wittymodule.ScanIdentified, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.ix.close()
	ix := s.ix
	console := s.libraryConsole()
	// Roots in the library's order, then any the files name that it doesn't.
	var roots []string
	seen := map[string]bool{}
	for _, r := range s.Library.Paths {
		if r = filepath.Clean(r); !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	for _, f := range ix.files {
		if !seen[f.root] {
			seen[f.root] = true
			roots = append(roots, f.root)
		}
	}
	prog := &finishing{report: s.Report, total: len(ix.files)}
	found := make([][]*gameFile, len(roots))
	for i, root := range roots {
		if ctx.Err() != nil {
			break
		}
		found[i] = ix.gameFiles(root, console, prog.step)
	}
	prog.done = len(ix.files) // files under no root count as looked at
	for _, f := range found {
		prog.more(ix.checksumsWanted(f))
	}
	var games []*listedGame
	for i := range roots {
		if ctx.Err() != nil {
			break
		}
		for _, c := range ix.mergeGames(found[i], prog.step) {
			games = append(games, &listedGame{c: c})
		}
	}
	if ctx.Err() != nil {
		ix.saveVerdicts(context.Background())
		return wittymodule.ScanIdentified{}, ctx.Err()
	}
	own := map[string]bool{}
	for p, d := range ix.decisions {
		if d.Action == "own" {
			own[p] = true
		}
	}
	heads := s.previousHeads(ctx)
	linkVersions(games, own, heads)

	ix.save(ctx, len(offline) == 0)
	if err := ix.saveNotes(ctx, len(offline) > 0); err != nil {
		return wittymodule.ScanIdentified{}, err
	}
	out := wittymodule.ScanIdentified{Items: make([]wittymodule.ScanItem, 0, len(games))}
	for _, g := range games {
		out.Items = append(out.Items, scanItem(g))
	}
	if err := s.saveListed(ctx, games, out.Items, offline); err != nil {
		return wittymodule.ScanIdentified{}, err
	}
	out.Result = s.result(ctx, games)
	if s.Report != nil {
		s.Report(prog.total, prog.total, "")
	}
	return out, nil
}

// scanItem is a game as the server lists it.
func scanItem(g *listedGame) wittymodule.ScanItem {
	c := g.c
	v := gameVerdict{}
	if c.verdict != nil {
		v = *c.verdict
	}
	it := wittymodule.ScanItem{Kind: "game", Size: c.size, MTime: c.mtime, VersionOf: g.versionOf, VersionLabel: g.versionLabel}
	if len(c.discs) > 1 {
		for _, d := range c.discs {
			it.Files = append(it.Files, d.path)
		}
	} else {
		it.Files = []string{c.path}
	}
	it.Title, it.Tagline = gameFileTitle(fileBaseName(c.path, v), v)
	it.SortTitle = SortTitle(it.Title)
	it.Container = strings.TrimPrefix(strings.ToLower(filepath.Ext(c.path)), ".")
	if c.console != "" {
		it.Facets = map[string]string{"console": c.console}
	}
	return it
}

// previousHeads are the games other versions were under after the last scan.
func (s *Scan) previousHeads(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.db == nil {
		return out
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT version_of FROM game_items WHERE library = ? AND version_of <> ''`, s.Library.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out
}

type savedDisc struct {
	Disc int    `json:"disc"`
	Path string `json:"path"`
}

// saveListed keeps what the scan answered: the games, their versions and discs, and the serial
// each file says it is. A game under a folder that couldn't be read stays as it was.
func (s *Scan) saveListed(ctx context.Context, games []*listedGame, items []wittymodule.ScanItem, offline []string) error {
	if s.db == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	keep := map[string]bool{}
	for i, g := range games {
		keep[g.c.path] = true
		discs := ""
		if len(g.c.discs) > 1 {
			list := make([]savedDisc, 0, len(g.c.discs))
			for _, d := range g.c.discs {
				list = append(list, savedDisc{Disc: d.disc, Path: d.path})
			}
			b, _ := json.Marshal(list)
			discs = string(b)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_items(library, path, version_of, label, tagline, discs) VALUES(?,?,?,?,?,?)
			ON CONFLICT(library, path) DO UPDATE SET version_of = excluded.version_of, label = excluded.label, tagline = excluded.tagline, discs = excluded.discs`,
			s.Library.ID, g.c.path, g.versionOf, g.versionLabel, items[i].Tagline, discs); err != nil {
			return err
		}
		if g.c.verdict != nil && g.c.verdict.Serial != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO game_serials(library, path, serial) VALUES(?,?,?)
				ON CONFLICT(library, path) DO UPDATE SET serial = excluded.serial`, s.Library.ID, g.c.path, g.c.verdict.Serial); err != nil {
				return err
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT path FROM game_items WHERE library = ?`, s.Library.ID)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && !keep[p] && !underAny(p, offline) {
			gone = append(gone, p)
		}
	}
	rows.Close()
	for _, p := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM game_items WHERE library = ? AND path = ?`, s.Library.ID, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if rel, err := filepath.Rel(filepath.Clean(d), p); err == nil && filepath.IsLocal(rel) || filepath.Clean(d) == p {
			return true
		}
	}
	return false
}

// result says what came of the scan: what someone comes to the activity panel to find out is
// how many games there are, and whether any of them want their attention.
func (s *Scan) result(ctx context.Context, games []*listedGame) string {
	n := 0
	for _, g := range games {
		if g.versionOf == "" {
			n++
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return formatCount(n) + " " + many
	}
	out := "Found " + plural(n, "game", "games") + " in " + time.Since(s.Started).Round(time.Second).String() + "."
	counts := map[string]int{}
	if s.db != nil {
		counts, _ = ReviewCounts(ctx, s.db, s.Library.ID)
	} else {
		for _, note := range s.ix.notes {
			counts[note.kind]++
		}
	}
	if n := counts[reviewNeeds]; n > 0 {
		out += " " + plural(n, "file needs", "files need") + " a look."
	}
	if n := counts[reviewBlocked]; n > 0 {
		out += " " + plural(n, "game", "games") + " can't play."
	}
	return out
}

// formatCount writes a number with commas: 2,292.
func formatCount(n int) string {
	s := itoa(int64(n))
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
