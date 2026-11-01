package hostsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A core that maps its code through /dev/shm crashes without it, so it's asked for by name. Nothing
// else gets it, and having it opens nothing beyond that one folder.
func TestSharedMemoryIsOnlyForWhoAsks(t *testing.T) {
	if !landlockAvailable() {
		t.Skip("this kernel has no Landlock")
	}
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("this system has no /dev/shm")
	}
	if os.Getenv("WITTYPLAY_SHM_CHILD") != "" {
		allowed := os.Getenv("WITTYPLAY_SHM_CHILD") == "yes"
		l := Limits{SaveDir: os.Getenv("WITTYPLAY_SAVE"), SharedMemory: allowed}
		if err := Confine(l); err != nil {
			t.Fatal(err)
		}
		if err := Apply(l); err != nil {
			t.Logf("some layers are unavailable: %v", err)
		}
		p := fmt.Sprintf("/dev/shm/wittyplay-sandbox-test-%d", os.Getpid())
		err := os.WriteFile(p, []byte("x"), 0o600)
		if allowed && err != nil {
			t.Fatalf("asked for shared memory, and was refused: %v", err)
		}
		if !allowed && err == nil {
			_ = os.Remove(p)
			t.Fatal("wrote to shared memory without asking for it")
		}
		if allowed {
			if err := os.Remove(p); err != nil {
				t.Fatalf("it can't clean up after itself: %v", err)
			}
		}
		if err := os.WriteFile(filepath.Join(os.TempDir(), "wittyplay-sandbox-escape"), []byte("x"), 0o644); err == nil {
			t.Fatal("writing outside the save folder should still fail")
		}
		return
	}
	for _, asked := range []string{"yes", "no"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestSharedMemoryIsOnlyForWhoAsks", "-test.v")
		cmd.Env = append(os.Environ(), "WITTYPLAY_SHM_CHILD="+asked, "WITTYPLAY_SAVE="+t.TempDir())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("asked=%s: %v\n%s", asked, err, out)
		}
	}
}
