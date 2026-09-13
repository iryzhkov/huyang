package handlers

import (
	"context"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// executionGraph is deliberately experimental. Before an acquisition adapter
// exists, source boundaries are explicit unresolved nodes, not an empty graph
// that could be mistaken for proof that nothing calls anything.
func (h *Handlers) executionGraph(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	before := workspace.Identity()
	sources, revision, coverage, err := workspace.ExecutionSources(ctx)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
	policy, err := workspacecore.LoadPipelinePolicy(before.Root, "")
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_policy_invalid", err)
	}
	request := workspacecore.ExecutionRequest{
		Key: workspacecore.AnalysisKey{WorkspaceID: before.ID, Epoch: before.Epoch, Revision: revision,
			Profile: "execution/v1", ConfigHash: workspacecore.PipelineFingerprint(policy), ExecutionDomain: "canonical"},
		Root: before.Root,
	}
	snapshot, err := workspacecore.BuildExecutionSnapshot(ctx, request, executionBoundary{sources, coverage})
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
	_, afterRevision, _, err := workspace.ExecutionSources(ctx)
	if err != nil || afterRevision != revision || workspace.Identity().Epoch != before.Epoch {
		return mcpapi.Envelope(requestID, workspace, "conflict", "graph_source_changed", "Source changed during graph acquisition; retry against current content", map[string]any{})
	}
	result := mcpapi.Envelope(requestID, workspace, "partial", "execution_coverage_incomplete",
		"Execution snapshot records source boundaries; call coverage is unavailable", map[string]any{
			"snapshot": snapshot, "status": workspacecore.ExecutionUnknown,
		})
	result["api_version"] = mcpapi.APIVersionExperimental
	return result
}

type executionBoundary struct {
	sources  []workspacecore.ExecutionSource
	coverage workspacecore.Coverage
}

func (executionBoundary) Name() string    { return "source_boundary" }
func (executionBoundary) Version() string { return "1" }
func (c executionBoundary) ContributeExecution(ctx context.Context, request workspacecore.ExecutionRequest, builder *workspacecore.ExecutionBuilder) error {
	for _, source := range c.sources {
		if ctx.Err() != nil {
			return workspacecore.Coded("analysis_cancelled", ctx.Err())
		}
		evidence := workspacecore.ExecutionEvidence{Revision: request.Key.Revision,
			Producer:   workspacecore.ProducerVersion{Name: c.Name(), Version: c.Version()},
			Confidence: "unknown", Classification: "static",
			SourceHandles: []string{}, EvidenceIDs: []string{},
			Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"call_acquisition_unavailable"}, Limits: []string{}},
		}
		if err := builder.Node(workspacecore.ExecutionNode{ID: "source:" + source.Path, Kind: "unresolved", Path: source.Path, Evidence: []workspacecore.ExecutionEvidence{evidence}}); err != nil {
			return err
		}
	}
	builder.Covered(workspacecore.ExecutionCoverage{Complete: false, Capped: c.coverage.Capped, Gaps: append([]string{"call_acquisition_unavailable"}, c.coverage.Skipped...)})
	return nil
}
