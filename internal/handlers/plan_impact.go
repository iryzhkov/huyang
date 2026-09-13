package handlers

// change_plan(view=impact): what a plan would reach, before it is prepared.
//
// The plan says what an agent wants to change. The snapshot says what the code
// is. This answers the question between them - who calls these declarations,
// which tests are associated with the files, what generated or configuration
// files are involved, and what nobody could see - and it answers it without
// writing a byte or running a discovered command.
//
// The facts come from the canonical revision, because that is where the
// callers are: whoever calls a declaration today is who a change to it would
// reach. The plan's preview revision identifies the proposal being weighed.
// Both appear in the answer, because a claim that confused them would be a
// claim about code nobody has written.

import (
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// planImpact answers a preview or inspection asked for as impact.
func (h *Handlers) planImpact(ctx context.Context, requestID string, workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) map[string]any {
	identity := workspace.Identity()
	canonical := fmt.Sprintf("wsrev_%d", identity.StateSeq)
	targets := planTargets(workspace, plan)
	changed := make([]string, 0, len(targets))
	for _, target := range targets {
		changed = append(changed, target.Path)
	}
	policy, _ := workspacecore.LoadPipelinePolicy(identity.Root, "")
	snapshot, err := workspacecore.BuildAnalysisSnapshot(ctx, workspacecore.AnalysisRequest{
		Key: workspacecore.AnalysisKey{
			WorkspaceID: identity.ID, Epoch: identity.Epoch, Revision: canonical,
			Profile: "impact_preview/v1", ConfigHash: workspacecore.PipelineFingerprint(policy),
		},
		Root: identity.Root, Changed: changed,
	}, append([]workspacecore.AnalysisContributor{
		workspacecore.NativeImportContributor{Policy: policy.Impact, Variants: policy.Variants},
	}, h.analysisContributors(ctx, workspace, canonical)...)...)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "analysis_failed", err)
	}
	previewRevision := ""
	if plan.Preview != nil {
		previewRevision = plan.Preview.PreviewRevision
	}
	impact := workspacecore.PreviewImpact(snapshot, previewRevision, targets, policy.Variants)
	summary := fmt.Sprintf("%d target(s), %d caller(s) at %s", len(impact.Targets), len(impact.Callers), canonical)
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, map[string]any{
		"plan_id": plan.PlanID, "plan_revision": plan.PlanRevision, "impact": impact,
	})
	if !impact.Coverage.Complete {
		result["outcome"], result["code"] = "partial", "analysis_coverage_incomplete"
	}
	return result
}

// planTargets is what the plan's operations would change, in the vocabulary
// the impact preview reads: a path, and a declaration when the operation
// names one.
func planTargets(workspace *workspacecore.Workspace, plan workspacecore.PlanRecord) []workspacecore.ImpactTarget {
	targets := make([]workspacecore.ImpactTarget, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		target := workspacecore.ImpactTarget{OpID: operation.OpID, Kind: string(operation.Kind)}
		switch {
		case operation.Path != "":
			target.Path = workspacePath(workspace, operation.Path)
		case operation.From != "":
			target.Path = workspacePath(workspace, operation.From)
		case operation.Target != nil && operation.Target.SymbolLocator != nil:
			target.Path = workspacePath(workspace, operation.Target.SymbolLocator.Path)
			target.NamePath = operation.Target.SymbolLocator.NamePath
		case operation.Target != nil && operation.Target.FileRange != nil:
			target.Path = operation.Target.FileRange.Path
		}
		if target.Path == "" {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}
