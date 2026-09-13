package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type executionQuery struct {
	closedDirectory *string
	workspace       *workspacecore.Workspace
	sourceWorkspace *workspacecore.Workspace
	prepared        *providerpool.PreparedView
	sources         []workspacecore.ExecutionSource
	request         workspacecore.ExecutionRequest
	contributors    []workspacecore.ExecutionContributor
	snapshot        workspacecore.AnalysisSnapshot
	epoch           uint64
}

func (h *Handlers) captureExecution(ctx context.Context, w *workspacecore.Workspace, args map[string]any, view *providerpool.PreparedView) (*executionQuery, error) {
	q := &executionQuery{workspace: w, sourceWorkspace: w, prepared: view, epoch: w.Identity().Epoch}
	var err error
	if view != nil {
		q.sourceWorkspace, err = workspacecore.New(workspacecore.KindProject, view.Tree, q.epoch)
		if err != nil {
			return nil, workspacecore.Codedf("graph_source_failed", "prepared source could not be opened")
		}
	}
	sources, revision, coverage, err := q.sourceWorkspace.ExecutionSources(ctx)
	if err != nil {
		return nil, err
	}
	if expected, _ := args["revision"].(string); view == nil && strings.HasPrefix(expected, "content_") && expected != revision {
		return nil, workspacecore.Codedf("graph_source_changed", "requested content revision is no longer current")
	}
	q.sources = sources
	policy, err := workspacecore.LoadPipelinePolicy(q.sourceWorkspace.Identity().Root, "")
	if err != nil {
		return nil, workspacecore.Codedf("workspace_policy_invalid", "execution policy could not be loaded")
	}
	identity := w.Identity()
	q.request = workspacecore.ExecutionRequest{Root: q.sourceWorkspace.Identity().Root,
		Key: workspacecore.AnalysisKey{WorkspaceID: identity.ID, Epoch: identity.Epoch, Revision: revision, Profile: "execution/v1",
			ConfigHash: workspacecore.PipelineFingerprint(policy), ExecutionDomain: "canonical"}}
	enabled := true
	if value, ok := args["use_provider"].(bool); ok {
		enabled = value
	}
	if view == nil {
		q.contributors = h.acquireExecution(ctx, w, sources, revision, enabled)
	} else {
		q.request.Key.ExecutionDomain = "prepared:" + view.PreparedRevision
		facts := workspacecore.AcquireGoExecutionCalls(ctx, revision, sources, workspacecore.DefaultGraphBudget())
		q.bind(ctx, &facts)
		q.contributors = []workspacecore.ExecutionContributor{facts}
	}
	q.contributors = append(q.contributors, workspacecore.AcquireImportExecutionCalls(ctx, q.request, sources, policy), executionBoundary{sources, coverage})
	selected, err := executionFlowSelection(args)
	if err != nil {
		return nil, err
	}
	if err = q.expand(ctx, h, selected, enabled); err != nil {
		return nil, err
	}
	q.snapshot, err = workspacecore.BuildExecutionSnapshot(ctx, q.request, q.contributors...)
	if err != nil {
		return nil, err
	}
	if err = q.validate(ctx, h); err != nil {
		return nil, err
	}
	return q, nil
}
func (q *executionQuery) validate(ctx context.Context, h *Handlers) error {
	if q.closedDirectory != nil && !workspacecore.ClosedGoInventoryComplete(q.request.Root, q.sources, *q.closedDirectory) {
		return workspacecore.Codedf("graph_source_changed", "closed package inventory changed during proof")
	}
	_, revision, _, err := q.sourceWorkspace.ExecutionSources(ctx)
	if err != nil || revision != q.request.Key.Revision || q.workspace.Identity().Epoch != q.epoch {
		return workspacecore.Codedf("graph_source_changed", "source changed during execution acquisition")
	}
	if q.prepared != nil {
		current, failure := h.resolvePrepared("execution_validate", q.workspace, preparedSelector{PlanID: q.prepared.PlanID, PlanRevision: q.prepared.PlanRevision})
		if failure != nil || current.PreparedRevision != q.prepared.PreparedRevision || current.Tree != q.prepared.Tree {
			return workspacecore.Codedf("prepared_revision_unavailable", "prepared execution source was replaced or discarded")
		}
	}
	return nil
}
func (q *executionQuery) expand(ctx context.Context, h *Handlers, selected []string, enabled bool) error {
	if len(selected) == 0 {
		return nil
	}
	if q.prepared == nil {
		contributors, err := h.expandExecutionFlow(ctx, q.workspace, q.sources, &q.request, q.contributors, selected, enabled)
		q.contributors = contributors
		return err
	}
	base, err := workspacecore.BuildExecutionSnapshot(ctx, q.request, q.contributors...)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(selected)
	q.request.Key.Profile += fmt.Sprintf("/flow-%x", sha256.Sum256(encoded))
	facts := workspacecore.AcquireGoExecutionFlow(ctx, q.request.Key.Revision, q.sources, *base.Execution, selected)
	q.bind(ctx, &facts)
	q.contributors = append(q.contributors, facts)
	return nil
}
func (q *executionQuery) bind(ctx context.Context, facts *workspacecore.ExecutionCallFacts) {
	if q.prepared == nil {
		bindExecutionFacts(ctx, q.workspace, q.sources, facts)
		return
	}
	bindPreparedExecutionFacts(ctx, q.workspace, *q.prepared, q.sources, facts)
}
