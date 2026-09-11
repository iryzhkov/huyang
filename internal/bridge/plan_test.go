package bridge

import (
	"context"
	"errors"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// change_plan inspect accepts an omitted plan_revision and reads the
// current revision of the plan.
func TestChangePlanInspectWithoutPlanRevisionReadsCurrentRevision(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
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

// A plan operation addressed by symbol locator resolves through the
// provider on create, without a prior symbol_find, and is normalised to an
// exact range.
func TestChangePlanCreateResolvesSymbolLocatorThroughProvider(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"model.go": "package dispatch\n\ntype Shipment struct {\n\tID string\n}\n",
	})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	result := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "symbol-plan", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-shipment", "kind": "replace_symbol",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "model.go", "name_path": "Shipment"}},
			"content": "type Shipment struct {\n\tID string\n\tPriority int\n}\n",
		}},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("provider-backed locator failed without prior symbol_find: %#v", result)
	}
	plan := result["data"].(map[string]any)["plan"].(map[string]any)
	operation := plan["operations"].([]any)[0].(map[string]any)
	target := operation["target"].(map[string]any)
	if target["file_range"] == nil {
		t.Fatalf("symbol locator was not normalized to an exact range: %#v", operation)
	}
}

type failingPrepareStager struct {
	rollbacks int
}

func (s *failingPrepareStager) Epoch() uint64 { return 1 }

func (s *failingPrepareStager) Stage(context.Context, workspacecore.PlanStageRequest) error {
	return errors.New("prepare failed")
}

func (s *failingPrepareStager) Commit(context.Context, string) error { return nil }

func (s *failingPrepareStager) Rollback(context.Context, string) error {
	s.rollbacks++
	return nil
}

// A plan whose prepare failed and whose sandbox was already rolled back can
// still be discarded; the missing sandbox is not an error.
func TestChangePlanDiscardsFailedPrepareWithoutRemainingSandbox(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"sample.py": "value = 1\n"})
	workspace := direct.get(workspacecore.ID(workspaceID))
	target, err := workspace.NewRange("sample.py", 8, 9)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := workspace.CreatePlan([]workspacecore.PlanOperation{{
		OpID: "replace-value", Kind: workspacecore.OperationReplaceRange,
		Target: &workspacecore.PlanTarget{FileRange: &target}, Content: "2",
	}})
	if err != nil {
		t.Fatal(err)
	}
	stager := &failingPrepareStager{}
	if _, err := workspace.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err == nil {
		t.Fatal("prepare unexpectedly succeeded")
	}
	if stager.rollbacks != 1 {
		t.Fatalf("prepare rollback count = %d, want 1", stager.rollbacks)
	}

	failed, err := workspace.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	result := direct.handlers.changePlan(context.Background(), "req_discard_failed_prepare", workspace, map[string]any{
		"action": "discard", "plan_id": plan.PlanID, "plan_revision": float64(failed.PlanRevision),
	})
	if result["outcome"] != "ok" {
		t.Fatalf("discard failed after prepare cleanup: %#v", result)
	}
	transaction := result["transaction"].(map[string]any)
	if transaction["state"] != workspacecore.PlanDiscarded {
		t.Fatalf("discard state = %#v, want %s", transaction["state"], workspacecore.PlanDiscarded)
	}
}
