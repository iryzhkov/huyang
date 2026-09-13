package handlers

import (
	"context"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) combinedPathExplain(ctx context.Context, requestID string, w *workspacecore.Workspace, args map[string]any, view *providerpool.PreparedView) map[string]any {
	id, _ := args["trace_id"].(string)
	if id == "" || view != nil {
		return mcpapi.Failure(requestID, w, "trace_overlay_invalid", workspacecore.Codedf("trace_overlay_invalid", "combined mode requires an explicit completed trace and canonical source"))
	}
	trace, err := w.ExecutionTrace(id)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	if trace.Finished == nil || trace.Digest == "" {
		return mcpapi.Failure(requestID, w, "trace_incomplete", workspacecore.Codedf("trace_incomplete", "stop or finish the traced session before overlaying it"))
	}
	identity := w.Identity()
	if trace.WorkspaceID != identity.ID || trace.Epoch != identity.Epoch {
		return mcpapi.Failure(requestID, w, "trace_source_changed", workspacecore.Codedf("trace_source_changed", "trace workspace epoch differs from current source"))
	}
	staticArgs := cloneDebugMap(args)
	staticArgs["mode"] = "static"
	staticArgs["revision"] = trace.Revision
	result := h.pathExplain(ctx, requestID, w, staticArgs, nil)
	payload, _ := result["data"].(map[string]any)
	snapshot, ok := payload["snapshot"].(workspacecore.AnalysisSnapshot)
	if !ok || snapshot.Execution == nil {
		return result
	}
	overlay, err := workspacecore.OverlayExecution(ctx, *snapshot.Execution, trace)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	payload["mode"] = "combined"
	payload["static_status"] = payload["status"]
	payload["status"] = "unknown"
	payload["overlay"] = overlay
	paths, _ := payload["paths"].([]workspacecore.GraphPath)
	payload["frontier_candidates"] = workspacecore.ExecutionOverlayFrontiers(paths, overlay)
	payload["thread_transitions"] = workspacecore.ExecutionOverlayThreads(overlay)
	target, _ := payload["to"].(string)
	explanation, err := workspacecore.ExplainExecution(ctx, *snapshot.Execution, trace, overlay, paths, target)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	boundaries := workspacecore.ExecutionExplanationBoundaries(*snapshot.Execution, trace)
	if len(boundaries) >= workspacecore.MaxExecutionExplanations {
		explanation.Coverage.Capped = true
		explanation.Coverage.Limits = append(explanation.Coverage.Limits, "explanation_boundaries")
	}
	payload["explanation"] = explanation
	payload["boundary_candidates"] = boundaries
	payload["branch_outcomes"] = explanation.Conditions
	payload["coverage"] = explanation.Coverage
	payload["observation_scope"] = "Recorded stack relations and sampled locations in one execution; static paths remain alternatives. Thread changes do not prove synchronization."
	result["outcome"] = "partial"
	result["summary"] = "Static alternatives with bounded observations from one execution"
	return result
}
