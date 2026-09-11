package bridge

import (
	"strings"
	"testing"
)

func TestChangePlanInspectAcceptsOmittedOptionalRevision(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "create-inspect-optional",
		"action":          "create",
		"operations":      []any{map[string]any{"op_id": "new", "kind": "create_file", "path": "new.txt", "content": "new\n"}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	inspected := callModern(t, session, "change_plan", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "inspect-without-revision",
		"action":          "inspect",
		"plan_id":         plan["plan_id"],
	})
	if inspected["outcome"] != "ok" {
		t.Fatalf("inspect without optional plan_revision = %#v", inspected)
	}
}

func TestUnknownEditHandleReturnsRestartRecovery(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "edit_apply", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "expired-handle",
		"operation":       map[string]any{"kind": "replace_range", "target": map[string]any{"handle": "rng_pre_restart"}, "content": "after"},
	})
	if result["code"] != "handle_resolve_failed" {
		t.Fatalf("unknown handle result = %#v", result)
	}
	next := result["next"].([]any)
	if len(next) != 2 || !strings.Contains(next[1].(map[string]any)["action"].(string), "fresh_handle") {
		t.Fatalf("unknown handle recovery = %#v", result)
	}
}
