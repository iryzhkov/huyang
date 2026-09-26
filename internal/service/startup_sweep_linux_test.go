package service

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// A service that starts after its predecessor was killed removes what the
// predecessor could not: the scratch of a dead process, legacy scratch in
// the temp directory past its age, and an edit journal whose mutation never
// reached its target. A live owner's scratch stays.
func TestStartupSweepsWhatAKilledServiceLeft(t *testing.T) {
	stateDir, tempDir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	exited := exec.Command("true")
	if err := exited.Run(); err != nil {
		t.Skipf("cannot run true: %v", err)
	}
	dead := filepath.Join(stateDir, "scratch", "owner-"+strconv.Itoa(exited.Process.Pid)+"-1")
	live := filepath.Join(stateDir, "scratch", "owner-"+strconv.Itoa(os.Getpid())+"-")
	legacy := filepath.Join(tempDir, "huyang-command-env-123")
	for _, path := range []string{filepath.Join(dead, "huyang-command-env-1", "home"), live, filepath.Join(legacy, "home")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stale := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(legacy, stale, stale); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "note.md")
	if err := os.WriteFile(target, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	journal, err := json.Marshal(map[string]any{
		"version": 1, "path": target, "mode": 0o644, "pre_exists": true, "post_exists": true,
		"preimage":  base64.StdEncoding.EncodeToString([]byte("before")),
		"postimage": base64.StdEncoding.EncodeToString([]byte("after")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "native-864743651.json"), journal, 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	if direct.loadErr != nil {
		t.Fatal(direct.loadErr)
	}
	for path, want := range map[string]bool{dead: false, legacy: false, live: true, filepath.Join(stateDir, "native-864743651.json"): false} {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Fatalf("%s: exists = %v, want %v", path, err == nil, want)
		}
	}
	if content, _ := os.ReadFile(target); string(content) != "before" {
		t.Fatalf("journal target changed: %q", content)
	}
}
