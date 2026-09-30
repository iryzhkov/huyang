package handlers

import (
	"context"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangesViewMissingHandleExplainsWhereToObtainIt(t *testing.T) {
	h, id, root := literalFixture(t, map[string]string{"one.txt": "first\n"})
	for _, args := range [][]string{{"init", "-q"}, {"add", "one.txt"}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "initial"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	for _, target := range []map[string]any{{"path": "one.txt"}, {"handle": "HEAD"}} {
		result := h.Execute(context.Background(), "changes_recovery", "read", map[string]any{"workspace_id": id, "view": "changes", "target": target})
		next, _ := result["next"].([]any)
		if len(next) == 0 || !strings.Contains(result["summary"].(string), "history") {
			t.Fatalf("changes refusal cannot recover: %#v", result)
		}
	}
}

func TestPartialBatchCopyRefusalRetainsCanonicalMutationState(t *testing.T) {
	h, id, root := literalFixture(t, map[string]string{"one.txt": "first\n"})
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, workspacecore.MaxTransferBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	result := h.Execute(context.Background(), "partial_copy", "edit_apply", map[string]any{"workspace_id": id, "format": false, "operations": []any{map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "first", "new": "second"}, map[string]any{"kind": "copy_file", "from": "large.bin", "to": "copied.bin"}}})
	data := result["data"].(map[string]any)
	content, err := os.ReadFile(filepath.Join(root, "one.txt"))
	if err != nil || string(content) != "second\n" || data["canonical_changed"] != true || data["applied_operations"] != 1 {
		t.Fatalf("partial batch lost mutation state: %#v bytes=%q err=%v", result, content, err)
	}
}

func TestCopyBoundRefusalHasExplicitRecovery(t *testing.T) {
	h, id, root := literalFixture(t, map[string]string{"one.txt": "first\n"})
	if err := os.WriteFile(filepath.Join(root, "large.bin"), make([]byte, workspacecore.MaxTransferBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	result := h.Execute(context.Background(), "copy_bound_recovery", "edit_apply", map[string]any{"workspace_id": id, "operation": map[string]any{"kind": "copy_file", "from": "large.bin", "to": "copied.bin"}})
	if result["code"] != workspacecore.CodeCopySourceTooLarge || len(result["next"].([]any)) == 0 {
		t.Fatalf("copy refusal lacks bounded next: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "copied.bin")); !os.IsNotExist(err) {
		t.Fatalf("copy refusal changed destination: %v", err)
	}
}
