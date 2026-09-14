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

// maxGraphNodes bounds the function list of a graph reply. The whole list
// of this repository is 205 nodes and 132 KB, about 32,000 tokens, which is
// the largest reply this server can produce and more than a light model can
// hold: the reply is for orienting, and expand_functions is for detail.
const maxGraphNodes = 60

// compactExecutionGraph keeps what names a function and where it is. The
// per-node evidence repeats the same source revision on every node, and that
// revision is already beside the graph.
func compactExecutionGraph(graph *workspacecore.ExecutionGraph) map[string]any {
	if graph == nil {
		return nil
	}
	nodes := make([]map[string]any, 0, min(len(graph.Nodes), maxGraphNodes))
	for _, node := range graph.Nodes {
		if len(nodes) == maxGraphNodes {
			break
		}
		compact := map[string]any{"id": node.ID, "kind": node.Kind}
		for key, value := range map[string]string{"path": node.Path, "name": node.Name, "owner": node.Owner} {
			if value != "" {
				compact[key] = value
			}
		}
		if node.Line != 0 {
			compact["line"] = node.Line
		}
		nodes = append(nodes, compact)
	}
	return map[string]any{
		"id": graph.ID, "revision": graph.Revision, "coverage": graph.Coverage,
		"nodes": nodes, "node_count": len(graph.Nodes), "nodes_truncated": len(graph.Nodes) > len(nodes),
		"edges": graph.Edges, "edge_count": len(graph.Edges), "risks": graph.Risks,
	}
}

func executionGraphEnvelope(requestID string, w *workspacecore.Workspace, q *executionQuery) map[string]any {
	snapshot := map[string]any{
		"id": q.snapshot.ID, "key": q.snapshot.Key, "coverage": q.snapshot.Coverage,
		"built_at": q.snapshot.BuiltAt, "execution": compactExecutionGraph(q.snapshot.Execution),
		"fact_count": len(q.snapshot.Facts), "conflicts": q.snapshot.Conflicts,
	}
	data := map[string]any{"snapshot": snapshot, "status": workspacecore.ExecutionUnknown}
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
