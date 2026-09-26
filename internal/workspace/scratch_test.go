package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useScratchRoot points the command scratch at a fresh directory, keeps the
// legacy sweep inside the test's own temp directory, and restores both.
func useScratchRoot(t *testing.T) (root, tempDir string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "scratch")
	tempDir = t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	SetCommandScratchRoot(root)
	t.Cleanup(func() { SetCommandScratchRoot("") })
	return root, tempDir
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// A command's throwaway home is created inside this process's owner
// directory under the scratch root, never in the system temp directory.
func TestCommandEnvironmentLivesInTheOwnerDirectory(t *testing.T) {
	root, tempDir := useScratchRoot(t)
	t.Setenv("HUYANG_COMMAND_CACHE", "off")
	environment, cleanup, err := isolatedCommandEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var home string
	for _, entry := range environment {
		if value, found := strings.CutPrefix(entry, "HOME="); found {
			home = value
		}
	}
	owner, err := filepath.Rel(root, home)
	if err != nil || strings.HasPrefix(owner, "..") {
		t.Fatalf("HOME %q is not under the scratch root %q", home, root)
	}
	pid, _, ok := parseScratchOwner(strings.Split(owner, string(filepath.Separator))[0])
	if !ok || pid != os.Getpid() {
		t.Fatalf("HOME %q is not in this process's owner directory", home)
	}
	if entries, _ := os.ReadDir(tempDir); len(entries) != 0 {
		t.Fatalf("the system temp directory was used: %v", entries)
	}
}

// The sweep removes the owner directories of dead processes and nothing
// else: a live owner's directory, this process's own and foreign entries
// stay.
func TestSweepCommandScratchRemovesOnlyDeadOwners(t *testing.T) {
	root, _ := useScratchRoot(t)
	own, err := commandScratchDir("huyang-verification-command-*")
	if err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(root, scratchOwnerName(999999, "42"))
	live := filepath.Join(root, scratchOwnerName(4242, "7"))
	foreign := filepath.Join(root, "not-an-owner")
	mkdirs(t, filepath.Join(dead, "huyang-verification-command-1", "tree"), filepath.Join(live, "huyang-command-env-1"), foreign)
	previous := scratchOwnerAlive
	scratchOwnerAlive = func(pid int, _ string) bool { return pid == os.Getpid() || pid == 4242 }
	t.Cleanup(func() { scratchOwnerAlive = previous })
	removed, err := SweepCommandScratch()
	if err != nil || removed != 1 {
		t.Fatalf("sweep removed %d, %v; want 1", removed, err)
	}
	if pathExists(dead) {
		t.Fatal("a dead owner's directory survived the sweep")
	}
	for _, path := range []string{own, live, foreign} {
		if !pathExists(path) {
			t.Fatalf("%s was removed", path)
		}
	}
}

// Directories earlier versions left in the system temp directory have no
// owner, so only age decides: past a day they go, younger ones and anything
// not named like them stay.
func TestSweepLegacyScratchHonoursAge(t *testing.T) {
	tempDir := t.TempDir()
	now := time.Now()
	old := now.Add(-legacyScratchMaxAge - time.Hour)
	paths := map[string]time.Time{
		"huyang-command-env-old":          old,
		"huyang-verification-command-old": old,
		"huyang-command-env-young":        now.Add(-time.Hour),
		"unrelated-old":                   old,
	}
	for name, modified := range paths {
		path := filepath.Join(tempDir, name)
		mkdirs(t, filepath.Join(path, "home"))
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := sweepLegacyScratch(tempDir, now)
	if err != nil || removed != 2 {
		t.Fatalf("legacy sweep removed %d, %v; want 2", removed, err)
	}
	for name := range paths {
		kept := !strings.HasSuffix(name, "-old") || strings.HasPrefix(name, "unrelated")
		if pathExists(filepath.Join(tempDir, name)) != kept {
			t.Fatalf("%s: kept = %v, want %v", name, !kept, kept)
		}
	}
}

func TestParseScratchOwnerRejectsOtherNames(t *testing.T) {
	for _, name := range []string{"owner-", "owner-x-1", "owner-0-1", "owner-12", "sandbox-1"} {
		if _, _, ok := parseScratchOwner(name); ok {
			t.Fatalf("%q parsed as an owner directory", name)
		}
	}
	if pid, start, ok := parseScratchOwner(scratchOwnerName(12, "345")); !ok || pid != 12 || start != "345" {
		t.Fatalf("round trip = %d %q %v", pid, start, ok)
	}
}
