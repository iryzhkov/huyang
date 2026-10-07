//go:build linux

package embed

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/runtimedir"
)

// isolateLockDirectories removes XDG_RUNTIME_DIR, points TMPDIR and the
// /run/user seam at fresh temporary directories, and returns them.
func isolateLockDirectories(t *testing.T) (temp, runUser string) {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", "")
	if err := os.Unsetenv("XDG_RUNTIME_DIR"); err != nil {
		t.Fatal(err)
	}
	temp = t.TempDir()
	t.Setenv("TMPDIR", temp)
	runUser = t.TempDir()
	previous := runtimedir.RunUserRoot
	runtimedir.RunUserRoot = runUser
	t.Cleanup(func() { runtimedir.RunUserRoot = previous })
	return temp, runUser
}

func useLockUID(t *testing.T, uid int) {
	t.Helper()
	previous := lockUID
	lockUID = func() int { return uid }
	t.Cleanup(func() { lockUID = previous })
}

func TestRootLockDirectoryIsPerUser(t *testing.T) {
	temp, _ := isolateLockDirectories(t)
	first, second := rootLockDir(1000), rootLockDir(1001)
	if first == second {
		t.Fatalf("UIDs 1000 and 1001 share the lock directory %s", first)
	}
	if want := filepath.Join(temp, "agent99-huyang-1000"); first != want {
		t.Fatalf("lock directory for UID 1000 = %s, want %s", first, want)
	}
	if want := filepath.Join(temp, "agent99-huyang-1001"); second != want {
		t.Fatalf("lock directory for UID 1001 = %s, want %s", second, want)
	}
}

func TestRootLockDirectoryFollowsTheRuntimeDirectory(t *testing.T) {
	_, runUser := isolateLockDirectories(t)
	uid := os.Getuid()
	private := filepath.Join(runUser, strconv.Itoa(uid))
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	if got, want := rootLockDir(uid), filepath.Join(private, "agent99-huyang"); got != want {
		t.Fatalf("lock directory = %s, want %s", got, want)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/xdg/runtime")
	if got, want := rootLockDir(uid), "/xdg/runtime/agent99-huyang"; got != want {
		t.Fatalf("lock directory = %s, want %s", got, want)
	}
}

func TestRootLockRefusesALockDirectoryOwnedBySomeoneElse(t *testing.T) {
	temp, _ := isolateLockDirectories(t)
	other := os.Getuid() + 1
	// This process creates the directory, so for the simulated UID it is a
	// directory another user left behind in the shared temporary directory.
	foreign := filepath.Join(temp, "agent99-huyang-"+strconv.Itoa(other))
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	useLockUID(t, other)
	lock, err := acquireRootLock("/some/root")
	if err == nil {
		lock.release()
		t.Fatal("lock acquired in a directory owned by another UID")
	}
	for _, want := range []string{"embedded provider lock directory", foreign, "is owned by UID " + strconv.Itoa(os.Getuid())} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestRootLockRefusesAGroupWritableLockDirectory(t *testing.T) {
	temp, _ := isolateLockDirectories(t)
	shared := filepath.Join(temp, "agent99-huyang-"+strconv.Itoa(os.Getuid()))
	if err := os.Mkdir(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, err := rootLockPath("/some/root"); err == nil || !strings.Contains(err.Error(), "group- or world-writable") {
		t.Fatalf("group-writable lock directory: err = %v, want a refusal", err)
	}
}

func TestRootLockCreatesAPrivatePerUserDirectory(t *testing.T) {
	temp, _ := isolateLockDirectories(t)
	lock, err := acquireRootLock("/some/root")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	directory := filepath.Join(temp, "agent99-huyang-"+strconv.Itoa(os.Getuid()))
	if filepath.Dir(lock.path) != directory {
		t.Fatalf("lock %s is not in %s", lock.path, directory)
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("lock directory %v (err %v), want mode 0700", info, err)
	}
}
