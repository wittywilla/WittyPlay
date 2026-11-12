// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package rpc

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewLogLevelsAndTopics(t *testing.T) {
	var buf bytes.Buffer
	log := NewLog(&buf, "warn", "capture, firmware").With("cat", "games")
	log.Info("quiet info")
	log.Debug("quiet debug", "topic", "core")
	log.Debug("topic on", "topic", "capture")
	log.With("topic", "firmware").Debug("topic from With")
	log.Info("kept install", "audit", "yes")
	log.Warn("a warning")
	out := buf.String()
	for _, want := range []string{"topic on", "topic from With", "kept install", "a warning", "cat=games"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, not := range []string{"quiet info", "quiet debug"} {
		if strings.Contains(out, not) {
			t.Errorf("%q should be left out:\n%s", not, out)
		}
	}

	buf.Reset()
	off := NewLog(&buf, "off", "")
	off.Error("an error")
	off.Info("an audit", "audit", "yes")
	if out := buf.String(); strings.Contains(out, "an error") || !strings.Contains(out, "an audit") {
		t.Errorf("off should keep only audits:\n%s", out)
	}

	buf.Reset()
	def := NewLog(&buf, "", "")
	def.Debug("debug by default")
	def.Info("info by default")
	if out := buf.String(); strings.Contains(out, "debug by default") || !strings.Contains(out, "info by default") {
		t.Errorf("the default is info:\n%s", out)
	}
}
