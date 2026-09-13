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
	q, err := h.captureExecution(ctx, workspace, arguments, nil)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, workspacecore.ErrorCode(err), err)
	}
	return executionGraphEnvelope(requestID, workspace, q)
}
func executionGraphEnvelope(requestID string, w *workspacecore.Workspace, q *executionQuery) map[string]any {
	data := map[string]any{"snapshot": q.snapshot, "status": workspacecore.ExecutionUnknown}
	if q.prepared != nil {
		data["revision"] = q.prepared.PreparedRevision
		data["plan_id"] = q.prepared.PlanID
	}
	result := mcpapi.Envelope(requestID, w, "partial", "execution_coverage_incomplete",
		"Execution snapshot includes static candidates and unresolved frontiers; dynamic coverage remains incomplete", data)
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
