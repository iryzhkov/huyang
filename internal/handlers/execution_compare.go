package handlers

import (
	"context"
	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) compareTraces(ctx context.Context, requestID string, w *workspacecore.Workspace, args map[string]any) map[string]any {
	passingID, _ := args["passing_trace_id"].(string)
	failingID, _ := args["failing_trace_id"].(string)
	passing, err := w.ExecutionTrace(passingID)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	failing, err := w.ExecutionTrace(failingID)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	comparison, err := workspacecore.CompareExecutionTraces(ctx, passing, failing)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	result := mcpapi.Envelope(requestID, w, "partial", "trace_comparison_partial", "Comparison of retained samples; divergence does not establish causality", map[string]any{"comparison": comparison})
	result["api_version"] = mcpapi.APIVersionExperimental
	return result
}
