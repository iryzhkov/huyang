package bridge

import (
	"context"
	"errors"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (d *directWorkspaces) recoverPreparedStager(
	ctx context.Context,
	workspace *workspacecore.Workspace,
	reference string,
) (*sandboxPlanStager, workspacecore.PlanRecord, bool, error) {
	plan, ok := workspace.RecoverablePreparedPlan(reference)
	if !ok {
		return nil, workspacecore.PlanRecord{}, false, nil
	}
	originalRevision := plan.Preparation.PreparedRevision
	raw, err := d.planStager(workspace, plan.PlanID, plan.PlanRevision, true)
	if err != nil {
		return nil, plan, true, err
	}
	recovered, err := workspace.PreparePlan(ctx, plan.PlanID, plan.PlanRevision, raw)
	if err != nil {
		return nil, plan, true, err
	}
	stager, ok := raw.(*sandboxPlanStager)
	if !ok {
		return nil, recovered, true, errors.New("provider_unavailable: recovered plan stager has an unexpected type")
	}
	if recovered.Preparation == nil || recovered.Preparation.PreparedRevision != originalRevision {
		_ = stager.Rollback(context.Background(), plan.PlanID)
		return nil, recovered, true, errors.New("prepared_revision_changed: restart recovery produced different exact bytes")
	}
	return stager, recovered, true, nil
}
