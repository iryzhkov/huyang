package handlers

import (
	"context"
	"testing"
)

func TestReadBatchRecoveryIdentifiesEveryFailedTarget(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"ok.txt": "ok\n"})
	result := h.Execute(context.Background(), "recovery_batch", "read", map[string]any{"workspace_id": id, "targets": []any{map[string]any{"path": "missing-one.txt"}, map[string]any{"path": "ok.txt"}, map[string]any{"path": "missing-two.txt"}}})
	files := result["data"].(map[string]any)["files"].([]map[string]any)
	if files[0]["coverage"] == nil || files[2]["coverage"] == nil {
		t.Fatalf("failed target coverage lost: %#v", files)
	}
	seen := map[int]bool{}
	for _, raw := range result["next"].([]any) {
		if step, ok := raw.(map[string]any); ok {
			if index, ok := step["target_index"].(int); ok {
				seen[index] = true
			}
		}
	}
	if !seen[0] || !seen[2] || seen[1] {
		t.Fatalf("recovery lost target identity: %#v", result["next"])
	}
}

func TestPreviewBatchFailureNeverSuggestsSkippingCanonicalEdits(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"one.txt": "first\n"})
	result := h.Execute(context.Background(), "preview_resume", "edit_apply", map[string]any{"workspace_id": id, "preview_only": true, "operations": []any{map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "first", "new": "second"}, map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "absent", "new": "third"}}})
	data := result["data"].(map[string]any)
	if data["applied_operations"] != 0 || data["previewed_operations"] != 1 || data["canonical_changed"] != false {
		t.Fatalf("preview failure claims mutation: %#v", result)
	}
	for _, raw := range result["next"].([]any) {
		if step, ok := raw.(map[string]any); ok && step["skip_applied_operations"] != nil {
			t.Fatalf("preview skips unapplied edits: %#v", step)
		}
	}
}

func TestPartialEditBatchRecoveryDoesNotReplayAppliedOperations(t *testing.T) {
	h, id, _ := literalFixture(t, map[string]string{"one.txt": "first\n"})
	result := h.Execute(context.Background(), "partial_resume", "edit_apply", map[string]any{"workspace_id": id, "format": false, "operations": []any{map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "first", "new": "second"}, map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "absent", "new": "third"}}})
	for _, raw := range result["next"].([]any) {
		if step, ok := raw.(map[string]any); ok && step["action"] == "retry_from_failed_operation_after_correcting_it" && step["failed_operation"] == 1 {
			return
		}
	}
	t.Fatalf("no bounded resume guidance: %#v", result)
}
