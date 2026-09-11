package bridge

import "testing"

func TestApplyFailureRecommendsSafeReprepare(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "create-for-recovery", "action": "create",
		"operations": []any{map[string]any{"op_id": "new", "kind": "create_file", "path": "new.txt", "content": "new\n"}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	failed := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "apply-without-prepare", "action": "apply",
		"plan_id": plan["plan_id"], "plan_revision": plan["plan_revision"], "prepared_revision": "prep_missing",
	})
	next := failed["next"].([]any)
	if len(next) != 2 || next[0].(map[string]any)["action"] != "prepare" {
		t.Fatalf("apply recovery = %#v", failed)
	}
}
