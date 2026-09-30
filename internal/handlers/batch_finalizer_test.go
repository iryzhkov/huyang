package handlers

import (
	"context"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	"os"
	"path/filepath"
	"testing"
)

func TestBatchResumeSurvivesMCPFinalization(t *testing.T) {
	for _, preview := range []bool{false, true} {
		h, id, root := literalFixture(t, map[string]string{"one.txt": "first\n"})
		result := h.Execute(context.Background(), "finalized_batch", "edit_apply", map[string]any{"workspace_id": id, "format": false, "preview_only": preview, "operations": []any{map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "first", "new": "second"}, map[string]any{"kind": "replace_literal", "path": "one.txt", "old": "absent", "new": "third"}}})
		result = mcpapi.FinalizeEnvelope("edit_apply", result)
		next := result["next"].([]any)
		step := next[0].(map[string]any)
		data := result["data"].(map[string]any)
		leaves, _ := data["operation_recovery"].([]any)
		if len(next) > mcpapi.MaxNextEntries || len(leaves) != 2 || data["failed_operation"] != 1 {
			t.Fatalf("bounded recovery lost leaf evidence: %#v", result)
		}
		if leaves[0].(map[string]any)["action"] != "locate_a_shorter_distinctive_line" || leaves[1].(map[string]any)["action"] != "read_known_path" {
			t.Fatalf("leaf actions lost: %#v", leaves)
		}
		expected := "second\n"
		if preview {
			expected = "first\n"
			if step["action"] != "correct_failed_operation_and_retry_the_full_preview" || step["skip_applied_operations"] != nil || data["applied_operations"] != 0 || data["canonical_changed"] != false {
				t.Fatalf("preview resume wrong: %#v", result)
			}
		} else if step["action"] != "retry_from_failed_operation_after_correcting_it" || step["skip_applied_operations"] != 1 || data["applied_operations"] != 1 || data["canonical_changed"] != true {
			t.Fatalf("real resume hidden: %#v", result)
		}
		content, err := os.ReadFile(filepath.Join(root, "one.txt"))
		if err != nil || string(content) != expected {
			t.Fatalf("bytes=%q err=%v", content, err)
		}
	}
}
