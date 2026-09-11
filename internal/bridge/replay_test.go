package bridge

import (
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// A replayed stateful call says so, reports that it made no new mutation,
// and a different request under the same key is a conflict with recovery.
func TestIdempotentReplayIsExplicitAndKeyReuseIsAConflict(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "only once\n"})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()

	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "once", "mode": "literal",
		"include_ranges": true,
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	arguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "replay-key",
		"operation": map[string]any{
			"kind": "replace_range", "target": map[string]any{"file_range": hit["range"]}, "content": "twice",
		},
	}
	first := callModern(t, session, "edit_apply", arguments)
	replayed := callModern(t, session, "edit_apply", arguments)
	replayData := replayed["data"].(map[string]any)
	if first["idempotency"] != "created" || replayed["idempotency"] != "replayed" ||
		replayData["canonical_changed"] != false || replayData["original_canonical_changed"] != true ||
		!strings.Contains(strings.ToLower(replayed["summary"].(string)), "no new mutation") {
		t.Fatalf("idempotent replay wording is ambiguous: %#v", replayed)
	}
	conflictArguments := map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "replay-key",
		"operation": map[string]any{
			"kind": "replace_range", "target": map[string]any{"file_range": hit["range"]}, "content": "different",
		},
	}
	conflict := callModern(t, session, "edit_apply", conflictArguments)
	if conflict["code"] != "idempotency_key_reused" || len(conflict["next"].([]any)) == 0 {
		t.Fatalf("idempotency conflict has no recovery: %#v", conflict)
	}
}
