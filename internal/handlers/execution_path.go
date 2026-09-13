package handlers

import (
	"context"
	"path/filepath"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *Handlers) pathExplain(ctx context.Context, requestID string, w *workspacecore.Workspace, args map[string]any, view *providerpool.PreparedView) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	if mode, _ := args["mode"].(string); mode != "" && mode != "static" {
		return mcpapi.Failure(requestID, w, "execution_mode_unavailable", workspacecore.Codedf("execution_mode_unavailable", "combined paths require the trace overlay stage"))
	}
	maxPaths, maxDepth := argInt(args, "max_paths", workspacecore.MaxExecutionPaths), argInt(args, "max_depth", workspacecore.MaxExecutionDepth)
	if maxPaths < 1 || maxPaths > workspacecore.MaxExecutionPaths || maxDepth < 1 || maxDepth > workspacecore.MaxExecutionDepth {
		return mcpapi.Failure(requestID, w, "graph_budget_invalid", workspacecore.Codedf("graph_budget_invalid", "path/depth limits must be within 1..16 and 1..64"))
	}
	q, err := h.captureExecution(ctx, w, args, view)
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	from, err := executionEndpoint(*q.snapshot.Execution, args["from"])
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	to, err := executionEndpoint(*q.snapshot.Execution, args["to"])
	if err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	paths := workspacecore.ExecutionGraphPaths(ctx, *q.snapshot.Execution, from.ID, to.ID, maxPaths, maxDepth)
	if len(paths.Paths) == 0 {
		q.closedProof(ctx, from, to)
		paths = workspacecore.ExecutionGraphPaths(ctx, *q.snapshot.Execution, from.ID, to.ID, maxPaths, maxDepth)
	} else {
		paths, err = q.expandPaths(ctx, h, args, paths, from.ID, to.ID, maxPaths, maxDepth)
		if err != nil {
			return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
		}
	}
	workspacecore.RankExecutionPaths(*q.snapshot.Execution, &paths)
	if err = q.validate(ctx, h); err != nil {
		return mcpapi.Failure(requestID, w, workspacecore.ErrorCode(err), err)
	}
	outcome, code := "partial", "execution_coverage_incomplete"
	if paths.Coverage.Complete && !paths.Coverage.Capped {
		outcome, code = "ok", ""
	}
	payload := map[string]any{"status": paths.Status, "paths": paths.Paths, "coverage": paths.Coverage, "snapshot": q.snapshot,
		"from": from.ID, "to": to.ID, "mode": "static", "ranking": "confidence, unresolved transitions, length, stable identity; ranking applies within the bounded candidates",
		"path_identity": "path IDs name immutable candidate content; they are not registry lookup handles"}
	if q.closedDirectory != nil {
		payload["proof_scope"] = map[string]any{"package_path": *q.closedDirectory, "model": "closed nullary direct-call Go grammar"}
	}
	if view != nil {
		payload["revision"] = view.PreparedRevision
		payload["plan_id"] = view.PlanID
	}
	result := mcpapi.Envelope(requestID, w, outcome, code, "Static paths describe candidates; absence requires complete uncapped coverage", payload)
	result["api_version"] = mcpapi.APIVersionExperimental
	return result
}
func (q *executionQuery) closedProof(ctx context.Context, from, to workspacecore.ExecutionNode) {
	if filepath.Dir(from.Path) != filepath.Dir(to.Path) {
		return
	}
	snapshot, err := workspacecore.ClosedGoExecution(ctx, q.request, q.sources, filepath.Dir(from.Path))
	if err != nil {
		return
	}
	facts := workspacecore.ExecutionCallFacts{Producer: workspacecore.ProducerVersion{Name: "closed_go_calls", Version: "1"},
		Nodes: snapshot.Execution.Nodes, Edges: snapshot.Execution.Edges, Coverage: snapshot.Execution.Coverage}
	q.bind(ctx, &facts)
	request := q.request
	request.Key = snapshot.Key
	if bound, err := workspacecore.BuildExecutionSnapshot(ctx, request, facts); err == nil {
		q.snapshot = bound
		directory := filepath.Dir(from.Path)
		q.closedDirectory = &directory
	}
}
func (q *executionQuery) expandPaths(ctx context.Context, h *Handlers, args map[string]any, paths workspacecore.ExecutionPaths, from, to string, maxPaths, maxDepth int) (workspacecore.ExecutionPaths, error) {
	selected := workspacecore.ExecutionPathFunctions(*q.snapshot.Execution, paths)
	enabled := true
	if value, ok := args["use_provider"].(bool); ok {
		enabled = value
	}
	if err := q.expand(ctx, h, selected, enabled); err != nil {
		return paths, err
	}
	expanded, err := workspacecore.BuildExecutionSnapshot(ctx, q.request, q.contributors...)
	if err != nil {
		return paths, err
	}
	q.snapshot = expanded
	projection := workspacecore.ExecutionControlProjection(*expanded.Execution)
	result := workspacecore.ExecutionGraphPaths(ctx, projection, from, to, maxPaths, maxDepth)
	if len(result.Paths) == 0 {
		result = workspacecore.ExecutionGraphPaths(ctx, *expanded.Execution, from, to, maxPaths, maxDepth)
		result.Coverage.Complete = false
		result.Coverage.Gaps = append(result.Coverage.Gaps, "control_flow_slice_incomplete")
	}
	return result, nil
}
