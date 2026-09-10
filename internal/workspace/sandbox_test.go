package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestSandboxMaterializesExactTreeAndPreparedBytesWithoutAliases(t *testing.T) {
	source, base := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".git", "HEAD"), []byte("ref: fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "dirty.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dirty.txt", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	sandbox, err := MaterializeSandbox(context.Background(), source, base, ID("ws_0123456789abcdef0123456789abcdef"), "plan_fixture", 1, "wsrev_1", DefaultSandboxLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Cleanup() })
	sourceInfo, _ := os.Stat(filepath.Join(source, "dirty.txt"))
	copyInfo, _ := os.Stat(filepath.Join(sandbox.Tree, "dirty.txt"))
	if os.SameFile(sourceInfo, copyInfo) {
		t.Fatal("sandbox aliases canonical inode")
	}
	if sandbox.Backend != "reflink" && sandbox.Backend != "safe_copy" {
		t.Fatalf("backend = %q", sandbox.Backend)
	}
	baseFiles, err := sandbox.BaseStageFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(baseFiles) != 1 || baseFiles[0].Path != "dirty.txt" {
		t.Fatalf("verification base files included metadata or binary content: %+v", baseFiles)
	}
	if target, err := os.Readlink(filepath.Join(sandbox.Tree, "link")); err != nil || target != "dirty.txt" {
		t.Fatalf("symlink = %q, %v", target, err)
	}
	request := PlanStageRequest{PlanID: "plan_fixture", PlanRevision: 1, Files: []PlanStageFile{{
		Path: "dirty.txt", Before: []byte("before\n"), After: []byte("after\n"),
		BeforeExists: true, AfterExists: true,
		BeforeDisk: DiskSnapshot{Kind: ObjectRegularText, Size: 7, Mode: 0o600},
		AfterDisk:  DiskSnapshot{Kind: ObjectRegularText, Size: 6, Mode: 0o600},
	}}}
	if err := sandbox.ApplyPrepared(request); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(sandbox.Tree, "dirty.txt"))
	if err != nil || !bytes.Equal(got, []byte("after\n")) {
		t.Fatalf("prepared = %q, %v", got, err)
	}
	canonical, _ := os.ReadFile(filepath.Join(source, "dirty.txt"))
	if !bytes.Equal(canonical, []byte("before\n")) {
		t.Fatalf("canonical changed: %q", canonical)
	}
	root := sandbox.Root
	if err := sandbox.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("sandbox retained: %v", err)
	}
}

func TestSandboxPreservesReadOnlyFileModesUnderUmask(t *testing.T) {
	source, base := t.TempDir(), t.TempDir()
	path := filepath.Join(source, "packed-object")
	if err := os.WriteFile(path, []byte("immutable\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
	sandbox, err := MaterializeSandbox(context.Background(), source, base, ID("ws_0123456789abcdef0123456789abcdef"), "plan_readonly", 1, "wsrev_1", DefaultSandboxLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Cleanup() })
	info, err := os.Stat(filepath.Join(sandbox.Tree, "packed-object"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o444 {
		t.Fatalf("sandbox mode = %o, want 444", info.Mode().Perm())
	}
}

func TestStreamingTextDetectorHandlesSplitUTF8AndNUL(t *testing.T) {
	text := &streamingTextDetector{}
	for _, chunk := range [][]byte{{'a', 0xe2}, {0x82}, {0xac, 'z'}} {
		if _, err := text.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if !text.valid() {
		t.Fatal("valid UTF-8 split across chunks was classified as binary")
	}
	binary := &streamingTextDetector{}
	_, _ = binary.Write([]byte{'a', 0, 'b'})
	if binary.valid() {
		t.Fatal("NUL-containing content was classified as text")
	}
}

func TestSandboxRejectsSpecialFilesAndHonorsCancellation(t *testing.T) {
	if runtime.GOOS == "linux" {
		source := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(source, "pipe"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := MaterializeSandbox(context.Background(), source, t.TempDir(), ID("ws_0123456789abcdef0123456789abcdef"), "plan_special", 1, "wsrev_1", DefaultSandboxLimits()); err == nil {
			t.Fatal("special file was accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	limits := DefaultSandboxLimits()
	limits.WallTime = time.Second
	if _, err := MaterializeSandbox(ctx, source, t.TempDir(), ID("ws_0123456789abcdef0123456789abcdef"), "plan_cancel", 1, "wsrev_1", limits); err == nil {
		t.Fatal("cancelled materialization succeeded")
	}
}

func TestReapSandboxesRemovesOnlyOwnedUnreferencedDirectories(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := ID("ws_0123456789abcdef0123456789abcdef")
	sandbox, err := MaterializeSandbox(context.Background(), source, base, id, "plan_reap", 1, "wsrev_1", DefaultSandboxLimits())
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(base, "sandbox-foreign")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ReapSandboxes(base, map[string]bool{"plan_reap": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sandbox.Root); err != nil {
		t.Fatalf("referenced sandbox removed: %v", err)
	}
	if err := ReapSandboxes(base, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sandbox.Root); !os.IsNotExist(err) {
		t.Fatalf("unreferenced sandbox retained: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("foreign directory removed: %v", err)
	}
}

func TestSandboxPreparedChangesCaptureExactLegacyDelta(t *testing.T) {
	source, base := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "replace.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "delete.txt"), []byte("delete\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	sandbox, err := MaterializeSandbox(context.Background(), source, base, ID("ws_0123456789abcdef0123456789abcdef"), "legacy_fixture", 1, "wsrev_1", DefaultSandboxLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.Cleanup() })
	if err := os.WriteFile(filepath.Join(sandbox.Tree, "replace.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(sandbox.Tree, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandbox.Tree, "create.txt"), []byte("create\n"), 0o660); err != nil {
		t.Fatal(err)
	}
	changes, err := sandbox.PreparedChanges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[0].Path != "create.txt" || changes[1].Path != "delete.txt" || changes[2].Path != "replace.txt" {
		t.Fatalf("prepared paths = %+v", changes)
	}
	if changes[0].BeforeExists || !changes[0].AfterExists || !bytes.Equal(changes[0].After, []byte("create\n")) {
		t.Fatalf("create delta = %+v", changes[0])
	}
	if !changes[1].BeforeExists || changes[1].AfterExists || !bytes.Equal(changes[1].Before, []byte("delete\n")) {
		t.Fatalf("delete delta = %+v", changes[1])
	}
	if !bytes.Equal(changes[2].Before, []byte("before\n")) || !bytes.Equal(changes[2].After, []byte("after\n")) {
		t.Fatalf("replace delta = %+v", changes[2])
	}
	if err := os.WriteFile(filepath.Join(source, "replace.txt"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.PreparedChanges(context.Background()); err == nil {
		t.Fatal("canonical drift was accepted")
	}
}
