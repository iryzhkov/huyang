package service

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Workspaces open on first use, so startup journal recovery must open the
// registered workspace that confines a journal's target itself; otherwise
// a postimage left by a killed mutation would stay as a conflict.
func TestStartupRecoversAJournalInAWorkspaceNotYetOpened(t *testing.T) {
	direct, stateDir, root, id := sweepFixture(t)
	direct.closeProviders()
	if err := os.RemoveAll(filepath.Join(stateDir, "commit-journals", string(id))); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "notes.txt")
	writeSweepFile(t, target, "after\n")
	journal, err := json.Marshal(map[string]any{
		"version": 1, "path": target, "mode": 0o644, "pre_exists": true, "post_exists": true,
		"preimage":  base64.StdEncoding.EncodeToString([]byte("before\n")),
		"postimage": base64.StdEncoding.EncodeToString([]byte("after\n")),
	})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(stateDir, "native-1.json")
	writeSweepFile(t, journalPath, string(journal))

	restarted := newDirectWorkspaces(stateDir)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "before\n" {
		t.Fatalf("target = %q, %v; want the preimage restored", got, err)
	}
	if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
		t.Fatalf("journal still present: %v", err)
	}
	if restarted.registry.loaded(id) == nil {
		t.Fatal("the workspace confining the journal was not opened for recovery")
	}
}
