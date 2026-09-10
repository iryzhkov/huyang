package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	ws, err := New(KindProject, root, 7)
	if err != nil {
		t.Fatal(err)
	}
	return ws, root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func conflictCode(t *testing.T, err error) ConflictCode {
	t.Helper()
	var conflict *Conflict
	if !errors.As(err, &conflict) {
		t.Fatalf("got %T %v, want *Conflict", err, err)
	}
	return conflict.Code
}

func TestWorkspaceIdentityAndLayeredSnapshot(t *testing.T) {
	ws, root := testWorkspace(t)
	other, err := New(KindProject, root, 7)
	if err != nil {
		t.Fatal(err)
	}
	identity := ws.Identity()
	if !strings.HasPrefix(string(identity.ID), "ws_") || len(identity.ID) != len("ws_")+32 {
		t.Fatalf("workspace ID %q is not a prefixed 128-bit value", identity.ID)
	}
	if identity.ID == other.Identity().ID {
		t.Fatal("two workspaces received the same ID")
	}
	if identity.Kind != KindProject || identity.Epoch != 7 || identity.StateSeq != 1 {
		t.Fatalf("unexpected initial identity: %+v", identity)
	}

	path := filepath.Join(root, "main.go")
	writeFile(t, path, "package main\n")
	layer := ProviderLayer{
		ChangedTick: 4,
		Dirty:       true,
		Content:     []byte("package main\n\nvar staged = true\n"),
		LSPVersions: map[string]int64{"gopls#1": 9},
	}
	snapshot, err := ws.Snapshot(path, layer)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Workspace.ID != identity.ID || snapshot.Workspace.Kind != KindProject {
		t.Fatalf("snapshot lost workspace identity: %+v", snapshot.Workspace)
	}
	if snapshot.Disk.Kind != ObjectRegularText || snapshot.ChangedTick != 4 || !snapshot.Dirty ||
		snapshot.LSPVersions["gopls#1"] != 9 {
		t.Fatalf("snapshot lost a layer: %+v", snapshot)
	}
	if snapshot.ContentSHA256 != hashBytes(layer.Content) {
		t.Fatalf("snapshot hashed disk instead of dirty provider content: %s", snapshot.ContentSHA256)
	}
	layer.Content[0] = 'X'
	layer.LSPVersions["gopls#1"] = 99
	if snapshot.ContentSHA256 != hashBytes([]byte("package main\n\nvar staged = true\n")) ||
		snapshot.LSPVersions["gopls#1"] != 9 {
		t.Fatal("snapshot aliases caller-owned provider data")
	}
}

func TestMutationRequiresWorkspaceAndRevision(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "a.txt")
	writeFile(t, path, "old")
	snapshot, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = ws.ValidateMutation("", path, snapshot.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictWorkspaceIDRequired {
		t.Fatalf("missing workspace ID: got %s", got)
	}
	_, err = ws.ValidateMutation(ws.Identity().ID, path, "", ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictRevisionRequired {
		t.Fatalf("missing revision: got %s", got)
	}
	_, err = ws.ValidateMutation("ws_wrong", path, snapshot.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictWorkspaceIDMismatch {
		t.Fatalf("wrong workspace ID: got %s", got)
	}
	if _, err := ws.ValidateMutation(ws.Identity().ID, path, snapshot.Revision, ProviderLayer{}); err != nil {
		t.Fatalf("valid mutation precondition failed: %v", err)
	}
}

func TestMutationDetectsSameMetadataRewrite(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "same.txt")
	writeFile(t, path, "first")
	initial, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "other")
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterInfo.Size() != info.Size() || !afterInfo.ModTime().Equal(info.ModTime()) {
		t.Skip("filesystem did not preserve the requested same-size/same-mtime adversary")
	}

	current, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("same-metadata rewrite: got %s", got)
	}
	if current.ContentSHA256 == initial.ContentSHA256 {
		t.Fatal("forced mutation validation reused the metadata hash cache")
	}
	if current.Workspace.StateSeq <= initial.Workspace.StateSeq {
		t.Fatal("external content change did not advance workspace state")
	}
}

func TestMutationDetectsAtomicSave(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "atomic.txt")
	writeFile(t, path, "before")
	initial, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(root, ".atomic.txt.new")
	writeFile(t, replacement, "after!")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	current, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("atomic save: got %s", got)
	}
	if current.Disk.Inode == initial.Disk.Inode && current.Disk.Device == initial.Disk.Device {
		t.Fatal("atomic save did not change the recorded disk identity")
	}
}

func TestMutationDetectsDeleteAndRecreate(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "recreated.txt")
	writeFile(t, path, "same")
	initial, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentDeleted {
		t.Fatalf("delete: got %s", got)
	}
	if missing.Disk.Kind != ObjectMissing {
		t.Fatalf("deleted file reported as %s", missing.Disk.Kind)
	}

	writeFile(t, path, "same")
	recreated, err := ws.ValidateMutation(ws.Identity().ID, path, missing.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("recreate: got %s", got)
	}
	if recreated.Revision == initial.Revision {
		t.Fatal("delete/recreate rebound the original revision")
	}
}

func TestMutationDetectsSymlinkRetarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs additional privileges on Windows")
	}
	ws, root := testWorkspace(t)
	writeFile(t, filepath.Join(root, "one.txt"), "same")
	writeFile(t, filepath.Join(root, "two.txt"), "same")
	link := filepath.Join(root, "current.txt")
	if err := os.Symlink("one.txt", link); err != nil {
		t.Fatal(err)
	}
	initial, err := ws.Snapshot(link, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Disk.Kind != ObjectSymlink {
		t.Fatalf("symlink reported as %s", initial.Disk.Kind)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("two.txt", link); err != nil {
		t.Fatal(err)
	}
	current, err := ws.ValidateMutation(ws.Identity().ID, link, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("symlink retarget: got %s", got)
	}
	if current.Disk.SymlinkTarget != "two.txt" {
		t.Fatalf("new symlink target = %q", current.Disk.SymlinkTarget)
	}
}

func TestProviderRestartInvalidatesRevision(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "epoch.txt")
	writeFile(t, path, "stable")
	initial, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	before := ws.Identity()
	after := ws.SyncProviderEpoch(before.Epoch + 1)
	if after.StateSeq != before.StateSeq+1 {
		t.Fatalf("provider restart state sequence: before=%d after=%d", before.StateSeq, after.StateSeq)
	}
	current, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictWorkspaceEpoch {
		t.Fatalf("provider restart: got %s", got)
	}
	if current.Revision == initial.Revision {
		t.Fatal("provider restart retained an old revision")
	}
}

func TestSnapshotClassifiesBinaryAndConfinesPaths(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "data.bin")
	if err := os.WriteFile(path, []byte{0xff, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Disk.Kind != ObjectBinary {
		t.Fatalf("binary file reported as %s", snapshot.Disk.Kind)
	}
	if _, err := ws.Snapshot(filepath.Join(root, "..", "outside"), ProviderLayer{}); err == nil {
		t.Fatal("path outside workspace root was accepted")
	}
}

func TestRefreshObservesLayerChanges(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "layer.txt")
	writeFile(t, path, "text")
	first, err := ws.Snapshot(path, ProviderLayer{ChangedTick: 1})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, err := ws.Refresh(path, ProviderLayer{ChangedTick: 2, LSPVersions: map[string]int64{"lsp": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if second.Workspace.StateSeq != first.Workspace.StateSeq+1 || second.Revision == first.Revision {
		t.Fatalf("provider layer change was not observed: first=%+v second=%+v", first, second)
	}
}
