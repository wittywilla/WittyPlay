// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package gamearchive

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// goSafe runs fn on its own goroutine, and a panic in it is logged rather than taking the
// module down (the server's logging.Go, which this package used there).
func goSafe(where string, fn func(), attrs ...any) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				reportPanic(where, p, attrs...)
			}
		}()
		fn()
	}()
}

// reportPanic logs a recovered panic with its stack.
func reportPanic(where string, value any, attrs ...any) {
	if value == http.ErrAbortHandler { //nolint:errorlint // the standard library's sentinel
		return
	}
	args := append([]any{"where", where, "panic", fmt.Sprint(value), "stack", string(debug.Stack())}, attrs...)
	slog.Error("a part of WittyPlay crashed and was recovered", args...)
}
