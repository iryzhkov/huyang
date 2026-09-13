package handlers

import (
	"context"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// executionGraph combines adapter claims about one captured revision. Source
// boundaries remain explicit wherever call coverage is incomplete.
func (h *Handlers) executionGraph(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	selected, err := executionFlowSelection(arguments)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
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
	withProvider := true
	if enabled, ok := arguments["use_provider"].(bool); ok {
		withProvider = enabled
	}
	contributors := h.acquireExecution(ctx, workspace, sources, revision, withProvider)
	contributors = append(contributors, workspacecore.AcquireImportExecutionCalls(ctx, request, sources, policy), executionBoundary{sources, coverage})
	contributors, err = h.expandExecutionFlow(ctx, workspace, sources, &request, contributors, selected, withProvider)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
	snapshot, err := workspacecore.BuildExecutionSnapshot(ctx, request, contributors...)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
	_, afterRevision, _, err := workspace.ExecutionSources(ctx)
	if err != nil || afterRevision != revision || workspace.Identity().Epoch != before.Epoch {
		return mcpapi.Envelope(requestID, workspace, "conflict", "graph_source_changed", "Source changed during graph acquisition; retry against current content", map[string]any{})
	}
	result := mcpapi.Envelope(requestID, workspace, "partial", "execution_coverage_incomplete",
		"Execution snapshot includes static candidates and unresolved frontiers; dynamic call coverage remains incomplete", map[string]any{
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
			Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"source_call_coverage_incomplete"}, Limits: []string{}},
		}
		if err := builder.Node(workspacecore.ExecutionNode{ID: "source:" + source.Path, Kind: "unresolved", Path: source.Path, Evidence: []workspacecore.ExecutionEvidence{evidence}}); err != nil {
			return err
		}
	}
	builder.Covered(workspacecore.ExecutionCoverage{Complete: false, Capped: c.coverage.Capped, Gaps: append([]string{"source_call_coverage_incomplete"}, c.coverage.Skipped...)})
	return nil
}
