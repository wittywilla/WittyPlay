package stream

import (
	"strings"
	"testing"
)

// The profile has to let sandbox-exec start the runner, or no game ever starts on a Mac, and a
// folder's name can't add a rule.
func TestRunnerProfile(t *testing.T) {
	got := runnerProfile("/app/wittyplay-game-runner", `/data/x"))(allow network*)(allow file-write* (subpath "/`)
	if !strings.Contains(got, `(deny process-exec*)(allow process-exec (literal "/app/wittyplay-game-runner"))`) {
		t.Errorf("the runner itself can't start: %s", got)
	}
	if strings.Contains(got, `(allow network*)(`) && !strings.Contains(got, `\"))(allow network*)`) {
		t.Errorf("a folder's name added a rule: %s", got)
	}
	if !strings.Contains(got, `(deny network*)`) || !strings.Contains(got, `(deny file-write*)`) {
		t.Errorf("a limit went missing: %s", got)
	}
}
