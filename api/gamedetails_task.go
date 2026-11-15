// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/wittywilla/wittymodule/host"
)

// What finding out about games is doing, in the server's activity panel (host Task).
const (
	phaseLocal    = "Names from the local list"
	phaseOnline   = "Asking online sources and fetching box art"
	phasePictures = "Fetching box art"
)

// The task with the server's tasks: its name (letters, digits, - _ and . only, which the host's
// tasks route takes) and the title the activity panel shows.
const (
	gameMetaTaskName  = "game-details"
	gameMetaTaskTitle = "Finding out about games"
)

// hostTask reports a task of WittyPlay's to the server's activity panel, under its title. end
// finishes it (so does done reaching a total above 0). The host client's own Task sends no
// title, and only a name the route takes is shown at all.
func (a *App) hostTask(ctx context.Context, name, title string, done, total int, end bool, failed error) error {
	body := map[string]any{"title": title, "done": done, "total": total, "end": end}
	if failed != nil {
		body["error"] = failed.Error()
	}
	return a.Host.Do(ctx, host.On{}, "POST", "/tasks/"+name, body, nil)
}

// gameMetaTask reports one round of the lookup worker. The worker wakes every few minutes, so
// nothing shows until a round has found something to do. Pictures go on beside the details, so
// either can be the first to find some.
type gameMetaTask struct {
	g  *gameMeta
	mu sync.Mutex
	// What the panel's bar counts: the stage's games. Pictures count once they're all that's left.
	phase       string
	done, total int
	started     bool
	sent        time.Time
}

// stage starts the next stage, saying how much it has ahead of it.
func (t *gameMetaTask) stage(ctx context.Context, phase string) {
	total := t.g.a.gameWorkAhead(ctx, phase)
	t.mu.Lock()
	defer t.mu.Unlock()
	if total == 0 && t.started {
		return // nothing left: the bar stays as the last stage left it, full
	}
	t.phase, t.done, t.total = phase, 0, total
	if t.started {
		t.report(true)
	}
}

// report tells the server, now and then: every game would be a request each.
func (t *gameMetaTask) report(force bool) {
	if !force && time.Since(t.sent) < 2*time.Second {
		return
	}
	t.sent = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = t.g.a.hostTask(ctx, gameMetaTaskName, gameMetaTaskTitle, t.done, t.total, false, nil)
}

// did says n games of this stage have been dealt with.
func (t *gameMetaTask) did(n int) {
	if t == nil || n == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	t.done += n
	t.total = max(t.total, t.done)
	t.report(false)
}

// picture says n games have had their pictures fetched. It only moves the bar once they're all
// that's left, as they aren't the same games the details are counting.
func (t *gameMetaTask) picture(n int) {
	if n == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.started = true
	if t.phase == phasePictures {
		t.done += n
		t.total = max(t.total, t.done)
	}
	t.report(false)
}

func (t *gameMetaTask) finish(ctx context.Context, failed error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.started {
		return
	}
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if ctx.Err() == nil && failed == nil {
		t.g.log().Info("wittyplay: "+gameMetaResult(t.g.status(rctx)), "cat", "games")
		_ = t.g.a.hostTask(rctx, gameMetaTaskName, gameMetaTaskTitle, t.total, t.total, true, nil)
		return
	}
	if failed == nil {
		failed = errors.New("stopped")
	}
	_ = t.g.a.hostTask(rctx, gameMetaTaskName, gameMetaTaskTitle, t.done, t.total, true, failed)
}

// gameMetaResult says what's known about the games now, for the log.
func gameMetaResult(st gameMetaStatus) string {
	out := fmt.Sprintf("Recognised %d of %d games, %d of them by checksum. %d have box art.", st.Matched, st.Total, st.ByChecksum, st.WithPoster)
	if st.Pending > 0 {
		out += fmt.Sprintf(" %d are still waiting for an online source, and they'll be asked later.", st.Pending)
	}
	return out
}

// gameWorkAhead counts what a stage has to do, for its bar.
func (a *App) gameWorkAhead(ctx context.Context, phase string) int {
	where := pendingWhere
	switch phase {
	case phaseLocal:
		where += ` AND NOT EXISTS (SELECT 1 FROM wittyplay_game_answers s WHERE s.item_id = d.item_id AND s.source = 'libretro')`
	case phasePictures:
		where = `art_wanted = 1`
	}
	var n int
	_ = a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM wittyplay_game_details d WHERE `+where).Scan(&n)
	return n
}
