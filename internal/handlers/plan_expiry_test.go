package handlers

import (
	"errors"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// An apply of an expired plan finds no stager. The refusal must say the plan
// expired and offer prepare, not blame the provider or offer apply again.
func TestApplyOfExpiredPlanAsksForAFreshPrepare(t *testing.T) {
	plan := workspacecore.PlanRecord{PlanID: "plan_x", PlanRevision: 3, State: workspacecore.PlanExpired}
	gone := workspacecore.Coded(workspacecore.CodeProviderUnavailable, errors.New("prepared sandbox is not available"))
	err := missingPreparation(gone, plan, nil)
	if workspacecore.ErrorCode(err) != workspacecore.CodePlanStateInvalid || !strings.Contains(err.Error(), "expired") ||
		!strings.Contains(err.Error(), "change_plan action=prepare plan_id=plan_x plan_revision=3") {
		t.Fatalf("refusal = %v", err)
	}
	next := planFailureNext("apply", workspacecore.CodePlanStateInvalid, plan)
	if len(next) == 0 || next[0].(map[string]any)["action"] != "prepare" {
		t.Fatalf("next = %#v", next)
	}
}
