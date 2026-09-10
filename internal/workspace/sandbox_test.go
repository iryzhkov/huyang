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
