package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
)

func TestApplyFailureRecommendsSafeReprepare(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
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

func TestPreparedPlanRecoversAcrossServiceRestart(t *testing.T) {
	previousFactory := providerpool.DefaultFactory
	providerpool.DefaultFactory = providerpool.ConfiguredFactory{Backend: "embed"}
	defer func() { providerpool.DefaultFactory = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUYANG_RUNTIME_PATH", runtimeRoot)
	t.Setenv("HUYANG_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	first := newDirectWorkspaces(stateDir)
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, first)
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "restart-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "new", "kind": "create_file", "path": "new.txt", "content": "after\n",
		}},
	})
	createdPlan := created["data"].(map[string]any)["plan"].(map[string]any)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "restart-prepare", "action": "prepare",
		"plan_id": createdPlan["plan_id"], "plan_revision": createdPlan["plan_revision"],
	})
	if prepared["outcome"] != "ok" && prepared["outcome"] != "provisional" {
		t.Fatalf("initial prepare failed: %#v", prepared)
	}
	preparedPlan := prepared["data"].(map[string]any)["plan"].(map[string]any)
	preparedRevision := preparedPlan["preparation"].(map[string]any)["prepared_revision"].(string)
	cleanup()
	first.closeProviders()

	second := newDirectWorkspaces(stateDir)
	defer second.closeProviders()
	restartedSession, restartedCleanup := connectOfficialClient(t, mcpapi.ProfileEdit, second)
	defer restartedCleanup()
	reopened := callModern(t, restartedSession, "workspace_open", map[string]any{"kind": "project", "root": root})
	if got := reopened["workspace"].(map[string]any)["id"]; got != workspaceID {
		t.Fatalf("workspace id changed across restart: got %v want %v", got, workspaceID)
	}
	verified := callModern(t, restartedSession, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "restart-verify",
		"revision_or_transaction": preparedRevision, "stages": []any{"format_gate"},
	})
	if verified["outcome"] == "conflict" || verified["code"] == "revision_changed" {
		t.Fatalf("prepared verification was not recovered: %#v", verified)
	}
	applied := callModern(t, restartedSession, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "restart-apply", "action": "apply",
		"plan_id": createdPlan["plan_id"], "plan_revision": createdPlan["plan_revision"],
		"prepared_revision": preparedRevision, "accept_provisional": true,
	})
	if applied["outcome"] != "ok" && applied["outcome"] != "provisional" {
		t.Fatalf("prepared apply was not recovered: %#v", applied)
	}
	content, err := os.ReadFile(filepath.Join(root, "new.txt"))
	if err != nil || string(content) != "after\n" {
		t.Fatalf("recovered apply content = %q, %v", content, err)
	}
}
