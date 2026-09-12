package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A call that names root instead of workspace_id is routed through the
// service like any other: the workspace is opened before the scheduler
// lane and the verify phases, so verify_run and search both work without a
// preceding workspace_open.
func TestRootResolvesBeforeSchedulingAndVerification(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nvar needle = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	searched := direct.call(context.Background(), "search", map[string]any{"root": root, "query": "needle"})
	if searched["outcome"] != "ok" || searched["workspace"].(map[string]any)["id"] == nil {
		t.Fatalf("search through root = %#v", searched)
	}
	verified := direct.call(context.Background(), "verify_run", map[string]any{"root": root, "stages": []any{"parser"}, "revision_or_transaction": "current"})
	if verified["code"] == "workspace_not_found" || verified["workspace"].(map[string]any)["id"] != searched["workspace"].(map[string]any)["id"] {
		t.Fatalf("verify through root = %#v", verified)
	}
}
