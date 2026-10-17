package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitTopLevelAcceptsALinkedWorktreeFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if top := GitTopLevel(filepath.Join(dir, "sub", "deeper")); top != dir {
		t.Fatalf("GitTopLevel = %q, want %q", top, dir)
	}
	if top := GitTopLevel(filepath.Join(dir, "missing")); top != "" {
		t.Fatalf("GitTopLevel of a missing directory = %q, want none", top)
	}
}
