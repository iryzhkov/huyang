package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func executionFlowSelection(arguments map[string]any) ([]string, error) {
	raw, ok := arguments["expand_functions"]
	if !ok {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok || len(values) > workspacecore.MaxExecutionExpandedFunctions {
		return nil, workspacecore.Codedf("graph_selection_invalid", "expand_functions accepts at most 16 function IDs")
	}
	seen := map[string]bool{}
	selected := []string{}
	for _, value := range values {
		id, ok := value.(string)
		if !ok || len(id) == 0 || len(id) > 1024 {
			return nil, workspacecore.Codedf("graph_selection_invalid", "function IDs must be nonempty and at most 1024 bytes")
		}
		if !seen[id] {
			selected = append(selected, id)
			seen[id] = true
		}
	}
	sort.Strings(selected)
	return selected, nil
}
func (h *Handlers) expandExecutionFlow(ctx context.Context, w *workspacecore.Workspace, sources []workspacecore.ExecutionSource, request *workspacecore.ExecutionRequest, contributors []workspacecore.ExecutionContributor, selected []string, withProvider bool) ([]workspacecore.ExecutionContributor, error) {
	if len(selected) == 0 {
		return contributors, nil
	}
	graph, err := workspacecore.BuildExecutionSnapshot(ctx, *request, contributors...)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(selected)
	request.Key.Profile += fmt.Sprintf("/flow-%x", sha256.Sum256(encoded))
	goIDs, otherIDs := []string{}, []string{}
	nodes := map[string]workspacecore.ExecutionNode{}
	for _, node := range graph.Execution.Nodes {
		nodes[node.ID] = node
	}
	for _, id := range selected {
		node, ok := nodes[id]
		if !ok || node.Kind != "symbol" {
			return nil, workspacecore.Codedf("graph_selection_invalid", "selected ID must identify a captured function")
		}
		if filepath.Ext(node.Path) == ".go" {
			goIDs = append(goIDs, id)
		} else {
			otherIDs = append(otherIDs, id)
		}
	}
	if len(goIDs) > 0 {
		facts := workspacecore.AcquireGoExecutionFlow(ctx, request.Key.Revision, sources, *graph.Execution, goIDs)
		bindExecutionFacts(ctx, w, sources, &facts)
		contributors = append(contributors, facts)
	}
	if len(otherIDs) > 0 {
		facts := h.acquireSyntaxFlow(ctx, w, sources, request.Key.Revision, nodes, otherIDs, withProvider)
		bindExecutionFacts(ctx, w, sources, &facts)
		contributors = append(contributors, facts)
	}
	return contributors, nil
}
