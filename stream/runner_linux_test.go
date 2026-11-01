package stream

import (
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// What a core asks for by name reaches its runner, and nothing else gets it. /bin/true stands
// in for the runner: only how it was started is looked at.
func TestWhatARunnerIsStartedWith(t *testing.T) {
	log := slog.Default()
	save := t.TempDir()
	home := func(rn *Runner) string {
		// As exec does: the last one set is the one that counts.
		got := ""
		for _, kv := range rn.Cmd.Env {
			if v, ok := strings.CutPrefix(kv, "HOME="); ok {
				got = v
			}
		}
		return got
	}
	start := func(args ...string) *Runner {
		t.Helper()
		rn, err := StartRunner("/bin/true", args, save, 64, log)
		if err != nil {
			t.Fatal(err)
		}
		_ = rn.Cmd.Wait()
		rn.Release()
		return rn
	}
	plain := start()
	if h := home(plain); h != "/nonexistent" {
		t.Errorf("an ordinary core has the home folder %q", h)
	}
	if slices.ContainsFunc(plain.Cmd.Env, func(kv string) bool { return strings.HasPrefix(kv, "WITTYPLAY_PRIVATE_SHM") }) {
		t.Error("an ordinary core was told to make private shared memory")
	}
	if attr := plain.Cmd.SysProcAttr; attr.Cloneflags&syscall.CLONE_NEWNS != 0 || len(attr.AmbientCaps) != 0 {
		t.Errorf("an ordinary core got a mount namespace or a capability: %+v", attr)
	}
	if h := home(start("-own-home")); h != save {
		t.Errorf("a core that asked for a home folder has %q", h)
	}
	private := start("-shared-memory")
	if userNamespacesAllowed() {
		if private.Cmd.SysProcAttr.Cloneflags&syscall.CLONE_NEWNS == 0 {
			t.Skip("a private folder wasn't possible here, and it fell back")
		}
		if !slices.Contains(private.Cmd.Env, "WITTYPLAY_PRIVATE_SHM=1") {
			t.Error("the runner wasn't told it has a mount namespace")
		}
	}
}
