package handlers

import (
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"testing"
)

func TestCompactExecutionGraphRetainsSelectedFlowEvidence(t *testing.T) {
	condition := &workspacecore.ExecutionCondition{}
	evidence := []workspacecore.ExecutionEvidence{{Revision: "wsrev_1", SourceHandles: []string{"source_handle"}}}
	graph := &workspacecore.ExecutionGraph{Nodes: []workspacecore.ExecutionNode{{ID: "declaration", Kind: "symbol", Evidence: evidence}, {ID: "flow", Owner: "declaration", Kind: "branch", Column: 3, Condition: condition, Evidence: evidence}}}
	result := compactExecutionGraph(graph)
	nodes := result["nodes"].([]map[string]any)
	if nodes[0]["evidence"] != nil {
		t.Fatal("unexpanded overview repeats evidence")
	}
	if nodes[1]["condition"] != condition || nodes[1]["column"] != 3 || nodes[1]["evidence"] == nil {
		t.Fatalf("selected flow dropped semantic detail: %#v", nodes[1])
	}
}
