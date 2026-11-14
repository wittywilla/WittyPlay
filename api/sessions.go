// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
	"github.com/wittywilla/wittyplay/rpc"
	"github.com/wittywilla/wittyplay/stream"
)

// Games being played, as the server sees them. Each game a person starts here, by a core or in a
// standalone emulator, is a host session (host.StartSession, Kind "game"): the server counts it
// for screen time, play time and "playing now", ends it when its sign-in ends, the profile's time
// runs out or games are turned off for it, and says so with the event session.stop, on which the
// game is stopped here. That replaces the server's recordPlayTime, its live-streams list, and its
// emulator_sessions.go (which watched screen time and sign-outs for emulator games only).

// liveGame is a game this module started, under the server's session.
type liveGame struct {
	host string // the server's id for it
	sid  string // ours: the stream session's, or the emulator session's
	user int64
	item int64
	stop func()
}

type liveGames struct {
	mu     sync.Mutex
	byHost map[string]*liveGame
	bySID  map[string]*liveGame
}

// live is every game being played here now. The games themselves are rpc.Mgr's (cores) and
// rpc.Docker's (standalone emulators): one process, one of each.
var live liveGames

func (l *liveGames) add(g *liveGame) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byHost == nil {
		l.byHost, l.bySID = map[string]*liveGame{}, map[string]*liveGame{}
	}
	l.byHost[g.host] = g
	l.bySID[g.sid] = g
}

func (l *liveGames) remove(g *liveGame) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byHost[g.host] == g {
		delete(l.byHost, g.host)
	}
	if l.bySID[g.sid] == g {
		delete(l.bySID, g.sid)
	}
}

func (l *liveGames) byHostID(id string) *liveGame {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byHost[id]
}

// has says whether a game of ours (by our id) is under a host session still.
func (l *liveGames) has(sid string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bySID[sid] != nil
}

// errSessionRefused is the server saying no to a game starting: the person may not play now.
var errSessionRefused = errors.New("This profile can’t play games right now.")

// beginSession tells the server a game is about to start for the person asking, before anything
// runs: if it refuses (screen time used up, games off, the sign-in gone), nothing starts. The
// answer is the server's id for it.
func (a *App) beginSession(r *http.Request, item int64, title string) (string, error) {
	id := who(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	hid, err := a.Host.StartSession(ctx, host.Session{Kind: "game", User: id.User, Item: item, Title: title, SignIn: id.SignIn})
	if err != nil {
		var he *host.Error
		if errors.As(err, &he) && he.Status >= 400 && he.Status < 500 {
			return "", errSessionRefused
		}
		return "", err
	}
	if hid == "" {
		return "", errors.New("the server gave the game no session")
	}
	return hid, nil
}

// abandonSession lets the server's session go when the game it was for never started.
func (a *App) abandonSession(hid string) {
	if hid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.Host.EndSession(ctx, hid); err != nil && !host.IsNotFound(err) {
		a.Log.Warn("wittyplay: could not tell the server a game didn't start", "cat", "games", "err", err)
	}
}

// followSession ties a started game to the server's session: when the game ends the session
// does, and when the server ends the session (session.stop) stop ends the game.
func (a *App) followSession(hid, sid string, user, item int64, done <-chan struct{}, stop func()) {
	g := &liveGame{host: hid, sid: sid, user: user, item: item, stop: stop}
	live.add(g)
	go func() {
		started := time.Now()
		<-done
		live.remove(g)
		a.abandonSession(hid)
		a.Log.Info("wittyplay: a game ended", "cat", "games", "session", sid, "item", strconv.FormatInt(item, 10),
			"played", time.Since(started).Round(time.Second).String())
	}()
}

func init() {
	// The server ended a game's session: its person signed out or went, their time ran out, or
	// games were turned off for them. The server has already let the session go.
	onEvent(host.EventSessionStop, func(a *App, data json.RawMessage) {
		var s host.SessionStop
		if json.Unmarshal(data, &s) != nil || s.ID == "" {
			return
		}
		if g := live.byHostID(s.ID); g != nil {
			a.Log.Info("wittyplay: the server stopped a game", "cat", "games", "session", g.sid, "why", s.Reason)
			go g.stop()
		}
	})
}

// ---------------------------------------------------------------- finding a game being played

// session finds a game for the account that started it. To anyone else it doesn't exist.
func session(r *http.Request) *stream.Session {
	sess := rpc.Mgr.Get(r.PathValue("sid"))
	if sess == nil || sess.UserID() != who(r).User {
		return nil
	}
	return sess
}

// watchSession finds a game this account may watch and play: its own, or one it joined with a
// code (seats.go). Everything else about a game stays with its host (session).
func watchSession(r *http.Request) *stream.Session {
	if sess := session(r); sess != nil {
		return sess
	}
	sess := rpc.Mgr.Get(r.PathValue("sid"))
	if sess == nil || !seats.holds(sess.ID(), who(r).User) {
		return nil
	}
	return sess
}

// sessionFor is the game a person is playing of an item now, or nil.
func sessionFor(userID, itemID int64) *stream.Session {
	if s := rpc.Mgr.ForUser(userID); s != nil && s.ItemID() == itemID {
		return s
	}
	return nil
}

// saveDir is one account's folder for one game: its saves, battery save, screenshots and
// patches (the person group's personGameDir, which was the server's gameService.saveDir).
func (a *App) saveDir(userID, itemID int64) string { return a.personGameDir(userID, itemID) }

// shotsDir is where a profile keeps a game's screenshots.
func (a *App) shotsDir(userID, itemID int64) string {
	return filepath.Join(a.saveDir(userID, itemID), "shots")
}

// awayFromHome is whether a device counts as away from home, which a stream starts lower for: the
// server's word (X-Witty-Away), never the address's.
func awayFromHome(r *http.Request) bool { return who(r).Away }
