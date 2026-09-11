package workspace

import (
	"errors"
	"fmt"
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

func TestWorkspaceRestoresServiceOwnedIdentity(t *testing.T) {
	root := t.TempDir()
	restoredID := ID("ws_00112233445566778899aabbccddeeff")
	restored, err := Open(OpenOptions{
		Kind: KindProject, Root: root, Identity: restoredID, StateSeq: 9, ProviderEpoch: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	identity := restored.Identity()
	if identity.ID != restoredID || identity.StateSeq != 9 || identity.Epoch != 4 {
		t.Fatalf("restored identity = %+v", identity)
	}
	if _, err := Open(OpenOptions{Kind: KindProject, Root: root, Identity: "ws_bad"}); err == nil {
		t.Fatal("invalid restored ID was accepted")
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
	// The revision derives from content: recreating identical bytes yields
	// the original token again even though the inode changed.
	if recreated.Revision != initial.Revision {
		t.Fatalf("delete/recreate of identical content changed the revision: %s vs %s", recreated.Revision, initial.Revision)
	}
	if recreated.Disk.Inode == initial.Disk.Inode && recreated.Disk.Device == initial.Disk.Device {
		t.Fatal("recreated file was expected to carry a new disk identity")
	}
	if _, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{}); err != nil {
		t.Fatalf("original revision no longer validates against identical content: %v", err)
	}
}

func TestMetadataOnlyChangesKeepRevisionAndHandles(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "stable.txt")
	writeFile(t, path, "stable content\n")
	initial, err := ws.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := ws.NewRange(path, 0, 6)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ws.RegisterRangeHandle(handle, HandleRange, "")
	if err != nil {
		t.Fatal(err)
	}

	// A touch moves the mtime without changing bytes.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	touched, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if err != nil {
		t.Fatalf("touch invalidated the revision: %v", err)
	}
	if touched.Revision != initial.Revision || touched.Disk.MTimeNS == initial.Disk.MTimeNS {
		t.Fatalf("touch: revision=%s initial=%s mtime=%d", touched.Revision, initial.Revision, touched.Disk.MTimeNS)
	}
	if touched.Workspace.StateSeq != initial.Workspace.StateSeq {
		t.Fatalf("touch advanced the state sequence from %d to %d", initial.Workspace.StateSeq, touched.Workspace.StateSeq)
	}

	// An editor atomic save replaces the inode with identical bytes.
	replacement := filepath.Join(root, ".stable.txt.swp")
	writeFile(t, replacement, "stable content\n")
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	saved, err := ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if err != nil {
		t.Fatalf("atomic save of identical bytes invalidated the revision: %v", err)
	}
	if saved.Revision != initial.Revision {
		t.Fatalf("atomic save changed the revision: %s vs %s", saved.Revision, initial.Revision)
	}
	resolution, err := ws.ResolveHandle(record.Handle)
	if err != nil || resolution.Status != ResolutionExact {
		t.Fatalf("range handle after metadata-only change = %+v, %v", resolution, err)
	}

	// A permission change is a real change of the object and must conflict.
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = ws.ValidateMutation(ws.Identity().ID, path, initial.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("mode change: got %s", got)
	}
}

func TestConfinementRefusesSymlinkedParentsThatLeaveTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs additional privileges on Windows")
	}
	ws, root := testWorkspace(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "real", "kept.go"), "package real\n")

	escaped := filepath.Join(root, "linkdir", "x.go")
	if _, err := ws.Snapshot(escaped, ProviderLayer{}); err == nil {
		t.Fatal("snapshot through an escaping symlinked directory was accepted")
	}
	if _, err := ws.Read("linkdir/x.go"); err == nil {
		t.Fatal("read through an escaping symlinked directory was accepted")
	}
	writeFile(t, filepath.Join(outside, "x.go"), "package outside\n")
	_, _, err := ws.ApplyFile(ws.Identity().ID, "linkdir/x.go", RevisionID("docrev_any"), FileReplace, []byte("package hacked\n"))
	if err == nil {
		t.Fatal("write through an escaping symlinked directory was accepted")
	}
	content, readErr := os.ReadFile(filepath.Join(outside, "x.go"))
	if readErr != nil || string(content) != "package outside\n" {
		t.Fatalf("file outside the root was modified: %q, %v", content, readErr)
	}

	// A symlinked directory that stays inside the root is fine, and the
	// final element may itself be a symlink because it is observed, not
	// followed.
	if _, err := ws.Read("alias/kept.go"); err != nil {
		t.Fatalf("in-root symlinked directory refused: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.go"), filepath.Join(root, "leaf")); err != nil {
		t.Fatal(err)
	}
	leaf, err := ws.Snapshot("leaf", ProviderLayer{})
	if err != nil || leaf.Disk.Kind != ObjectSymlink {
		t.Fatalf("leaf symlink snapshot = %+v, %v", leaf, err)
	}
	if _, _, err := ws.ApplyFile(ws.Identity().ID, "leaf", leaf.Revision, FileReplace, []byte("x")); err == nil {
		t.Fatal("replace through a leaf symlink was accepted")
	}
}

func TestUnknownRevisionIsReportedAsEpochConflictNotContentChange(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "known.txt")
	writeFile(t, path, "one")
	if _, err := ws.Snapshot(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	foreign := RevisionID("docrev_0000000000000000000000000000000000000000000000000000000000000000")
	_, err := ws.ValidateMutation(ws.Identity().ID, path, foreign, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictWorkspaceEpoch {
		t.Fatalf("unknown revision: got %s", got)
	}
	var conflict *Conflict
	if !errors.As(err, &conflict) || conflict.Detail == "" {
		t.Fatalf("unknown revision conflict lacks detail: %v", err)
	}

	// A revision this instance issued and still retains is a content change.
	writeFile(t, path, "two")
	second, err := ws.Refresh(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "three")
	_, err = ws.ValidateMutation(ws.Identity().ID, path, second.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("retained revision: got %s", got)
	}
}

func TestRevisionHistoryIsBoundedPerDocument(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "churn.txt")
	var first DocumentSnapshot
	for index := 0; index < maxRevisionsPerDocument*2; index++ {
		writeFile(t, path, fmt.Sprintf("content %d\n", index))
		snapshot, err := ws.Refresh(path, ProviderLayer{})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = snapshot
		}
	}
	if got := ws.RevisionHistoryLength(path); got != maxRevisionsPerDocument {
		t.Fatalf("retained revisions = %d, want %d", got, maxRevisionsPerDocument)
	}
	ws.mu.Lock()
	total := len(ws.revisions)
	_, retained := ws.revisions[first.Revision]
	ws.mu.Unlock()
	if total != maxRevisionsPerDocument || retained {
		t.Fatalf("revision map holds %d entries, first retained=%v", total, retained)
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
