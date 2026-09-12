package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A native move carries the exact bytes and mode, so the destination's
// content revision equals the source's; a binary file moves too; a
// directory or symlink is refused, and so is a source over the bound.
func TestApplyMoveKeepsContentRevisionAndRefusesUnsupportedSources(t *testing.T) {
	root := t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	writeFile(t, filepath.Join(root, "exec.sh"), "#!/bin/sh\r\necho  drift\n")
	if err := os.Chmod(filepath.Join(root, "exec.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := ws.Snapshot("exec.sh", ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := ws.ApplyMove(ws.Identity().ID, "exec.sh", before.Revision, "bin/run.sh", "")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Destination.ContentSHA256 != before.ContentSHA256 || moved.SourceAfter.Disk.Kind != ObjectMissing {
		t.Fatalf("move result = %+v", moved)
	}
	info, err := os.Stat(filepath.Join(root, "bin", "run.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("destination mode = %v, %v", info, err)
	}
	binary := []byte{0, 1, 2, 0xff}
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	blob, err := ws.Snapshot("blob.bin", ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ApplyMove(ws.Identity().ID, "blob.bin", blob.Revision, "moved.bin", ""); err != nil {
		t.Fatalf("binary move: %v", err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "moved.bin")); !bytes.Equal(content, binary) {
		t.Fatalf("binary bytes = %v", content)
	}
	if err := os.Symlink("moved.bin", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	link, err := ws.Snapshot("link", ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.ApplyMove(ws.Identity().ID, "link", link.Revision, "link2", ""); ErrorCode(err) != CodeTransferUnsupported {
		t.Fatalf("symlink move error = %v", err)
	}
	large := make([]byte, MaxTransferBytes+1)
	for index := range large {
		large[index] = 'a'
	}
	if err := os.WriteFile(filepath.Join(root, "large.txt"), large, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.TransferSource("large.txt", ""); ErrorCode(err) != CodeCopySourceTooLarge {
		t.Fatalf("large source error = %v", err)
	}
	if _, err := ws.ApplyMove(ws.Identity().ID, "large.txt", "docrev_stale", "x.txt", ""); err == nil {
		t.Fatal("stale source revision was accepted")
	}
}

// TransferSource reads a workspace document with its revision and an
// absolute path outside the workspace without one; a relative path outside
// the workspace is refused.
func TestTransferSourceInsideAndOutside(t *testing.T) {
	root := t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	writeFile(t, filepath.Join(root, "in.txt"), "inside")
	inside, err := ws.TransferSource("in.txt", "")
	if err != nil || !inside.Inside || inside.Revision == "" || string(inside.Content) != "inside" {
		t.Fatalf("inside source = %+v, %v", inside, err)
	}
	external := filepath.Join(t.TempDir(), "out.txt")
	writeFile(t, external, "outside")
	outside, err := ws.TransferSource(external, hashBytes([]byte("outside")))
	if err != nil || outside.Inside || outside.Revision != "" || outside.Path != external {
		t.Fatalf("outside source = %+v, %v", outside, err)
	}
	if _, err := ws.TransferSource("../elsewhere.txt", ""); ErrorCode(err) != CodeCopySourceMissing {
		t.Fatalf("relative outside error = %v", err)
	}
	if _, _, err := ws.ApplyCopy(ws.Identity().ID, outside, "copied.txt", ""); err != nil {
		t.Fatal(err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "copied.txt")); string(content) != "outside" {
		t.Fatalf("copied = %q", content)
	}
	if _, _, err := ws.ApplyCopy(ws.Identity().ID, outside, "copied.txt", ""); ErrorCode(err) != CodeCreateTargetExists {
		t.Fatalf("copy onto existing error = %v", err)
	}
}

// TrackedState reads the index and the ignore rules without writing either,
// and reports not_a_repository outside Git.
func TestTrackedStateClassifiesWithoutWritingTheIndex(t *testing.T) {
	ws, root := newGitWorkspace(t)
	writeGitTestFile(t, root, ".gitignore", []byte("*.log\n"))
	writeGitTestFile(t, root, "new.txt", []byte("new\n"))
	writeGitTestFile(t, root, "build.log", []byte("log\n"))
	gitTestRun(t, root, "add", ".gitignore")
	indexBefore, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	states := ws.TrackedState([]string{"note.txt", "new.txt", "build.log", "absent.txt"})
	want := map[string]string{"note.txt": TrackedStateTracked, "new.txt": TrackedStateUntracked, "build.log": TrackedStateIgnored, "absent.txt": TrackedStateUntracked}
	for path, state := range want {
		if states[path] != state {
			t.Fatalf("state of %s = %q, want %q (all: %#v)", path, states[path], state, states)
		}
	}
	if indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index")); !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("TrackedState wrote the index")
	}
	plain := newNativeWorkspace(t, KindProject, t.TempDir(), nil, Limits{})
	if states := plain.TrackedState([]string{"a.txt"}); states["a.txt"] != TrackedStateNotRepository {
		t.Fatalf("outside git = %#v", states)
	}
}
