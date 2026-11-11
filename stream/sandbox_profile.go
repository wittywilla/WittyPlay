// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 WittyWilla and contributors

package stream

import (
	"path/filepath"
	"strings"
)

// sbplString quotes text for a sandbox profile, so a folder's name can't end its string and add a
// rule of its own.
func sbplString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// runnerProfile is a Mac's sandbox for a runner: no network, writing only to the save folder, and
// no starting programs, other than the runner itself, which sandbox-exec is about to start from
// inside the sandbox. Without saying so that refuses too, and no game would ever start.
func runnerProfile(runner, saveDir string) string {
	return `(version 1)(allow default)(deny network*)(deny process-exec*)(allow process-exec (literal ` + sbplString(runner) + `))(deny file-write*)` +
		`(allow file-write* (subpath ` + sbplString(saveDir) + `))(allow file-write-data (literal "/dev/null"))`
}

// realPath is a path as a sandbox sees it: on a Mac, /tmp, /var and /etc are links, and a rule
// about the name of a link matches nothing.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
