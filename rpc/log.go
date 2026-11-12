// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Log is this process's one logger. It writes slog's text lines to stderr, which the server reads
// as its own records: level, msg, cat, session and topic become fields there. What's written
// follows WITTYMODULE_LOG (debug, info, warn, error or off) and WITTYMODULE_LOG_TOPICS (the
// topics in module.json's capabilities.logTopics that are on). Libretro games log with
// "cat" "games", standalone emulators with "cat" "emulators".
var Log = NewLog(os.Stderr, os.Getenv("WITTYMODULE_LOG"), os.Getenv("WITTYMODULE_LOG_TOPICS"))

// levelOff is past every level slog has: nothing but an audit record gets through.
const levelOff = slog.Level(100)

// NewLog makes a logger writing to w at the level named and with the topics listed on.
func NewLog(w io.Writer, level, topics string) *slog.Logger {
	min := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		min = slog.LevelDebug
	case "warn", "warning":
		min = slog.LevelWarn
	case "error":
		min = slog.LevelError
	case "off":
		min = levelOff
	}
	on := map[string]bool{}
	for _, t := range strings.Split(topics, ",") {
		if t = strings.TrimSpace(t); t != "" {
			on[t] = true
		}
	}
	return slog.New(&topicHandler{next: slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}), min: min, on: on})
}

// topicHandler lets a record through when it's at the level asked for, when its topic is one
// that's on, or when it's an audit record ("audit" "yes"), which the server keeps whatever the
// level.
type topicHandler struct {
	next  slog.Handler
	min   slog.Level
	on    map[string]bool
	topic string // from With
	audit bool   // from With
}

func (h *topicHandler) Enabled(_ context.Context, l slog.Level) bool {
	// A quiet line costs nothing: below the level, only a topic or an audit can let it through.
	return l >= h.min || len(h.on) > 0 || l >= slog.LevelInfo
}

func (h *topicHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level < h.min {
		topic, audit := h.topic, h.audit
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "topic":
				topic = a.Value.String()
			case "audit":
				audit = a.Value.String() == "yes"
			}
			return true
		})
		if !audit && (topic == "" || !h.on[topic]) {
			return nil
		}
	}
	return h.next.Handle(ctx, r)
}

func (h *topicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	for _, a := range attrs {
		switch a.Key {
		case "topic":
			c.topic = a.Value.String()
		case "audit":
			c.audit = a.Value.String() == "yes"
		}
	}
	c.next = h.next.WithAttrs(attrs)
	return &c
}

func (h *topicHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.next = h.next.WithGroup(name)
	return &c
}
