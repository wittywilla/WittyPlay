// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Defaults for the lifecycle.
const (
	DefaultMaxContainers = 2
	DefaultIdle          = 10 * time.Minute
	DefaultLifetime      = 6 * time.Hour
	reapEvery            = 15 * time.Second
	opTimeout            = 60 * time.Second
)

// ErrBusy is the concurrency limit, in the same words the process sandbox uses.
var ErrBusy = errors.New("this server is already running as many emulator containers as it allows; stop one first")

// ErrNoContainer: that session has no container.
var ErrNoContainer = errors.New("that session has no container")

// Container is one session's container.
type Container struct {
	SessionID string
	UserID    string
	ID        string
	Info      Info
	Started   time.Time

	mu         sync.Mutex
	paused     bool
	lastActive time.Time
	done       chan struct{}
	doneOnce   sync.Once
	reason     string
	lastLog    string
}

// LastLog is the end of what an emulator said before it ended by itself, for the log.
func (c *Container) LastLog() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastLog
}

// Done is closed once the container has been stopped and removed.
func (c *Container) Done() <-chan struct{} { return c.done }

// Reason says why it ended: "stopped", "idle", "lifetime", "exited", "replaced" (the same person
// started another), "unwatched".
func (c *Container) Reason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reason
}

func (c *Container) finish(why string) {
	c.doneOnce.Do(func() {
		c.mu.Lock()
		c.reason = why
		c.mu.Unlock()
		close(c.done)
	})
}

// Lifecycle runs one container per session, holds them to a limit, and ends those that sit idle
// or run too long. It owns only containers carrying WittyPlay's labels.
type Lifecycle struct {
	RT          Runtime
	Max         int           // containers at once; DefaultMaxContainers if 0
	Idle        time.Duration // with no input for this long, it's stopped; DefaultIdle if 0
	MaxLifetime time.Duration // however busy, never longer; DefaultLifetime if 0
	Pull        bool          // pull the image before each create (it's pinned, so a cache hit is free)
	Log         *slog.Logger
	Now         func() time.Time // for tests

	mu       sync.Mutex
	sessions map[string]*Container
	starting int
	runOnce  sync.Once
}

func (l *Lifecycle) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Lifecycle) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func (l *Lifecycle) max() int {
	if l.Max > 0 {
		return l.Max
	}
	return DefaultMaxContainers
}

func (l *Lifecycle) idle() time.Duration {
	if l.Idle > 0 {
		return l.Idle
	}
	return DefaultIdle
}

func (l *Lifecycle) lifetime() time.Duration {
	if l.MaxLifetime > 0 {
		return l.MaxLifetime
	}
	return DefaultLifetime
}

// SweepOrphans removes every container carrying WittyPlay's label that this Lifecycle didn't
// start: what a crash or a killed module left. Run it once, at startup.
func (l *Lifecycle) SweepOrphans(ctx context.Context) (removed int, err error) {
	list, err := l.RT.List(ctx, map[string]string{LabelManaged: "true"})
	if err != nil {
		return 0, err
	}
	l.mu.Lock()
	mine := map[string]bool{}
	for _, c := range l.sessions {
		mine[c.ID] = true
	}
	l.mu.Unlock()
	var errs []error
	for _, c := range list {
		if mine[c.ID] || c.Labels[LabelManaged] != "true" {
			continue
		}
		l.log().Info("docker: removing a container left from before", "id", short(c.ID), "session", c.Labels[LabelSession])
		if c.Running {
			_ = l.RT.Stop(ctx, c.ID, 5*time.Second)
		}
		if err := l.RT.Remove(ctx, c.ID); err != nil {
			errs = append(errs, err)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// Start creates and starts the container for a session. A session has one container: starting
// it again stops the old one first.
func (l *Lifecycle) Start(ctx context.Context, s Spec) (*Container, error) {
	if err := Check(s); err != nil {
		return nil, err
	}
	sid := s.Labels[LabelSession]
	if old := l.Get(sid); old != nil {
		_ = l.Stop(ctx, sid)
	}
	// One game at a time for each person, as with cores: starting another ends the one before.
	if old := l.ForUser(s.Labels[LabelUser]); old != nil && s.Labels[LabelUser] != "" {
		_ = l.stop(ctx, old.SessionID, "replaced")
	}
	l.mu.Lock()
	if l.sessions == nil {
		l.sessions = map[string]*Container{}
	}
	if len(l.sessions)+l.starting >= l.max() {
		l.mu.Unlock()
		return nil, ErrBusy
	}
	l.starting++
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.starting--
		l.mu.Unlock()
	}()

	if l.Pull && !s.Local && !LocalImage(s.Image) {
		if err := l.RT.Pull(ctx, s.Image); err != nil {
			return nil, fmt.Errorf("pulling %s: %w", s.Image, err)
		}
	}
	id, err := l.RT.Create(ctx, s)
	if err != nil {
		return nil, err
	}
	// From here on, a failure must not leave the container behind.
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
		defer cancel()
		_ = l.RT.Remove(cctx, id)
	}
	if err := l.RT.Start(ctx, id); err != nil {
		cleanup()
		return nil, err
	}
	info, err := l.RT.Inspect(ctx, id)
	if err != nil {
		cleanup()
		return nil, err
	}
	now := l.now()
	c := &Container{SessionID: sid, UserID: s.Labels[LabelUser], ID: id, Info: info, Started: now, lastActive: now, done: make(chan struct{})}
	l.mu.Lock()
	l.sessions[sid] = c
	l.mu.Unlock()
	return c, nil
}

// Get returns a session's container, or nil.
func (l *Lifecycle) Get(sessionID string) *Container {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sessions[sessionID]
}

// Count is how many containers are running.
func (l *Lifecycle) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sessions)
}

// Touch notes someone playing, which holds off the idle limit.
func (l *Lifecycle) Touch(sessionID string) {
	if c := l.Get(sessionID); c != nil {
		c.mu.Lock()
		c.lastActive = l.now()
		c.mu.Unlock()
	}
}

// ForUser returns the container a person is playing in, or nil.
func (l *Lifecycle) ForUser(user string) *Container {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.sessions {
		if c.UserID == user {
			return c
		}
	}
	return nil
}

// Paused says a session's emulator is frozen.
func (c *Container) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// Pause freezes a session's emulator, and Resume lets it go on. A paused session still counts
// against the limit, and is still stopped when it has sat for as long as an idle one may.
func (l *Lifecycle) Pause(ctx context.Context, sessionID string) error {
	return l.setPaused(ctx, sessionID, true)
}

// Resume lets a paused emulator go on.
func (l *Lifecycle) Resume(ctx context.Context, sessionID string) error {
	return l.setPaused(ctx, sessionID, false)
}

func (l *Lifecycle) setPaused(ctx context.Context, sessionID string, paused bool) error {
	c := l.Get(sessionID)
	if c == nil {
		return ErrNoContainer
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused == paused {
		return nil
	}
	var err error
	if paused {
		err = l.RT.Pause(ctx, c.ID)
	} else {
		err = l.RT.Unpause(ctx, c.ID)
	}
	if err != nil {
		return err
	}
	c.paused = paused
	c.lastActive = l.now()
	return nil
}

// Stop stops and removes a session's container.
func (l *Lifecycle) Stop(ctx context.Context, sessionID string) error {
	return l.stop(ctx, sessionID, "stopped")
}

// StopBecause is Stop with a reason of the caller's.
func (l *Lifecycle) StopBecause(ctx context.Context, sessionID, why string) error {
	return l.stop(ctx, sessionID, why)
}

func (l *Lifecycle) stop(ctx context.Context, sessionID, why string) error {
	l.mu.Lock()
	c := l.sessions[sessionID]
	delete(l.sessions, sessionID)
	l.mu.Unlock()
	if c == nil {
		return ErrNoContainer
	}
	defer c.finish(why)
	if c.Paused() {
		_ = l.RT.Unpause(ctx, c.ID) // a frozen process can't be asked to stop
	}
	err := l.RT.Stop(ctx, c.ID, DefaultStop)
	if rerr := l.RT.Remove(ctx, c.ID); rerr != nil {
		return errors.Join(err, rerr)
	}
	return nil
}

// StopAll ends every container, as the module goes away.
func (l *Lifecycle) StopAll(ctx context.Context) {
	l.mu.Lock()
	ids := make([]string, 0, len(l.sessions))
	for id := range l.sessions {
		ids = append(ids, id)
	}
	l.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() { defer wg.Done(); _ = l.Stop(ctx, id) }()
	}
	wg.Wait()
}

// Reap ends what has sat idle or run too long, and forgets what has exited by itself. The
// background loop calls it; a test calls it with a clock of its own.
func (l *Lifecycle) Reap(ctx context.Context) {
	now := l.now()
	l.mu.Lock()
	all := make([]*Container, 0, len(l.sessions))
	for _, c := range l.sessions {
		all = append(all, c)
	}
	l.mu.Unlock()
	for _, c := range all {
		c.mu.Lock()
		last := c.lastActive
		c.mu.Unlock()
		switch {
		case now.Sub(c.Started) > l.lifetime():
			l.log().Info("docker: a container reached its longest time", "session", c.SessionID)
			_ = l.stop(ctx, c.SessionID, "lifetime")
		case now.Sub(last) > l.idle():
			l.log().Info("docker: stopping an unattended container", "session", c.SessionID)
			_ = l.stop(ctx, c.SessionID, "idle")
		default:
			if info, err := l.RT.Inspect(ctx, c.ID); err == nil && !info.Running {
				l.log().Info("docker: a container ended by itself", "session", c.SessionID, "state", info.State)
				// What it said last is the only clue to why, and it goes with the container.
				if out, err := l.RT.Logs(ctx, c.ID, 40); err == nil {
					if len(out) > 4096 {
						out = out[len(out)-4096:]
					}
					c.mu.Lock()
					c.lastLog = out
					c.mu.Unlock()
				}
				_ = l.stop(ctx, c.SessionID, "exited")
			}
		}
	}
}

// Run reaps until ctx ends. It's started once, however often it's called.
func (l *Lifecycle) Run(ctx context.Context) {
	l.runOnce.Do(func() {
		go func() {
			t := time.NewTicker(reapEvery)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					rctx, cancel := context.WithTimeout(ctx, opTimeout)
					l.Reap(rctx)
					cancel()
				}
			}
		}()
	})
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
