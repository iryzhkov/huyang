package workspace

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeRecord(path, before, after string) journalRecord {
	return journalRecord{
		Version: 1, Path: path, Mode: uint32(0o644), PreExists: true, PostExists: true,
		Preimage:  base64.StdEncoding.EncodeToString([]byte(before)),
		Postimage: base64.StdEncoding.EncodeToString([]byte(after)),
	}
}

func nativeJournals(t *testing.T, stateDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "native-") {
			names = append(names, entry.Name())
		}
	}
	return names
}

// Startup recovery resolves every journal in the shared state directory, not
// only the ones of one workspace: cut-short journals are discarded, a target
// still at its preimage is cleared wherever it lives, an owned postimage is
// rolled back, and a postimage outside every workspace is left alone.
func TestRecoverNativeJournalsResolvesLeftovers(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	stateDir := filepath.Join(t.TempDir(), "state")
	owned, untouched := filepath.Join(root, "owned.txt"), filepath.Join(root, "untouched.txt")
	foreignBefore, foreignAfter := filepath.Join(outside, "before.txt"), filepath.Join(outside, "after.txt")
	writeFile(t, owned, "after")
	writeFile(t, untouched, "before")
	writeFile(t, foreignBefore, "before")
	writeFile(t, foreignAfter, "after")
	writeJournalFixture(t, stateDir, "native-1.json", nativeRecord(owned, "before", "after"))
	writeJournalFixture(t, stateDir, "native-2.json", nativeRecord(untouched, "before", "after"))
	writeJournalFixture(t, stateDir, "native-3.json", nativeRecord(foreignBefore, "before", "after"))
	writeJournalFixture(t, stateDir, "native-4.json", nativeRecord(foreignAfter, "before", "after"))
	for name, content := range map[string]string{"native-5.json": "", "native-6.json": `{"version":1,"pa`} {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := RecoverNativeJournals(stateDir, []*Workspace{ws})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Recovered) != 1 || len(result.Cleared) != 2 || len(result.Conflicts) != 1 || len(result.Discarded) != 2 {
		t.Fatalf("recovery = %+v", result)
	}
	for path, want := range map[string]string{owned: "before", untouched: "before", foreignBefore: "before", foreignAfter: "after"} {
		if content, _ := os.ReadFile(path); string(content) != want {
			t.Fatalf("%s = %q, want %q", path, content, want)
		}
	}
	if left := nativeJournals(t, stateDir); len(left) != 1 || left[0] != "native-4.json" {
		t.Fatalf("journals left = %v, want only the unowned postimage", left)
	}
}

// A mutation refused before it wrote anything removes its journal instead of
// leaving it for a recovery that has nothing to do.
func TestMutateFileRemovesJournalWhenPreconditionFails(t *testing.T) {
	root := t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	path := filepath.Join(root, "file.txt")
	writeFile(t, path, "changed by someone else")
	if err := ws.mutateFile(path, true, []byte("expected"), true, []byte("new"), 0o644); err == nil {
		t.Fatal("a stale precondition was accepted")
	}
	if left := nativeJournals(t, ws.stateDir); len(left) != 0 {
		t.Fatalf("journals left = %v", left)
	}
	if content, _ := os.ReadFile(path); string(content) != "changed by someone else" {
		t.Fatalf("target changed: %q", content)
	}
}
