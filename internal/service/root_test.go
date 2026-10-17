package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A call that names root instead of workspace_id is routed through the
// service like any other: the workspace is opened before the scheduler
// lane and the replay key, so a read and a stateful call both work without
// a preceding workspace_open and land in the same workspace.
func TestRootResolvesBeforeSchedulingAndReplay(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nvar needle = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(t.TempDir())
	searched := direct.call(context.Background(), "search", map[string]any{"root": root, "query": "needle"})
	if searched["outcome"] != "ok" || searched["workspace"].(map[string]any)["id"] == nil {
		t.Fatalf("search through root = %#v", searched)
	}
	created := direct.call(context.Background(), "change_plan", map[string]any{
		"root": root, "action": "create",
		"operations": []any{map[string]any{"op_id": "add", "kind": "create_file", "path": "added.go", "content": "package main\n"}},
	})
	if created["code"] == "workspace_not_found" || created["workspace"].(map[string]any)["id"] != searched["workspace"].(map[string]any)["id"] {
		t.Fatalf("stateful call through root = %#v", created)
	}
}
