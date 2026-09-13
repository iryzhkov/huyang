package handlers

import (
	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) traceValueOrigin(requestID string, w *workspacecore.Workspace, args map[string]any) map[string]any {
	id, _ := args["trace_id"].(string)
	name, _ := args["value_name"].(string)
	trace, err := w.ExecutionTrace(id)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	origin, err := workspacecore.TraceValueOrigin(trace, name, argInt(args, "thread", 0))
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	result := mcpapi.Envelope(requestID, w, "partial", "mutation_origin_unavailable", "Sampled values cannot establish exact mutation origin", map[string]any{"value_origin": origin})
	result["api_version"] = mcpapi.APIVersionExperimental
	return result
}
