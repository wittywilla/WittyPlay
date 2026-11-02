package hostsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// With a mount namespace to itself, a runner's shared memory is its own: nothing it writes
// reaches the real folder, nothing in the real folder reaches it, it keeps no capability
// afterwards, and it can't write anywhere else.
func TestPrivateSharedMemoryIsPrivate(t *testing.T) {
	if !landlockAvailable() {
		t.Skip("this kernel has no Landlock")
	}
	if _, err := os.Stat("/dev/shm"); err != nil {
		t.Skip("this system has no /dev/shm")
	}
	const marker = "wittyplay-sandbox-private-test"
	if os.Getenv("WITTYPLAY_PRIVATE_CHILD") != "" {
		l := Limits{SaveDir: os.Getenv("WITTYPLAY_SAVE"), SharedMemory: true, MemoryMB: 2048}
		if err := Confine(l); err != nil {
			t.Fatal(err)
		}
		if err := Apply(l); err != nil {
			t.Logf("some layers are unavailable: %v", err)
		}
		if _, err := os.Stat("/dev/shm/" + marker + "-outside"); err == nil {
			t.Fatal("it can see the real shared memory")
		}
		if err := os.WriteFile("/dev/shm/"+marker, []byte("x"), 0o600); err != nil {
			t.Fatalf("it can't write to its own: %v", err)
		}
		st, err := os.ReadFile("/proc/self/status")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"CapEff:\t0000000000000000", "CapAmb:\t0000000000000000"} {
			if !strings.Contains(string(st), want) {
				t.Errorf("it kept a capability: no %q in\n%s", want, st)
			}
		}
		if err := unix.Mount("tmpfs", "/mnt", "tmpfs", 0, ""); err == nil {
			t.Error("it can still mount things")
		}
		if err := os.WriteFile(filepath.Join(os.TempDir(), "wittyplay-sandbox-escape"), []byte("x"), 0o644); err == nil {
			t.Fatal("writing outside the save folder should still fail")
		}
		return
	}
	outside := "/dev/shm/" + marker + "-outside"
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Skipf("can't write to shared memory here: %v", err)
	}
	defer os.Remove(outside)
	cmd := exec.Command(os.Args[0], "-test.run=TestPrivateSharedMemoryIsPrivate", "-test.v")
	cmd.Env = append(os.Environ(), "WITTYPLAY_PRIVATE_CHILD=1", "WITTYPLAY_SAVE="+t.TempDir(), PrivateSharedMemoryEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS, AmbientCaps: []uintptr{21},
		UidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}}, GidMappingsEnableSetgroups: false}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("namespaces aren't allowed here: %v", err)
		}
		// A namespace can be allowed and mounting in it not, as on GitHub's runners. The runner then
		// falls back to the Landlock rule, which TestSharedMemoryIsOnlyForWhoAsks covers.
		if strings.Contains(string(out), "no private shared memory") {
			t.Skipf("this system doesn't allow a private mount: %s", out)
		}
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := os.Stat("/dev/shm/" + marker); err == nil {
		_ = os.Remove("/dev/shm/" + marker)
		t.Error("what it wrote reached the real shared memory")
	}
}

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
