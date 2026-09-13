package handlers

import (
	"path/filepath"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func executionEndpoint(graph workspacecore.ExecutionGraph, raw any) (workspacecore.ExecutionNode, error) {
	id, _ := raw.(string)
	path, name := "", ""
	if target, ok := raw.(map[string]any); ok {
		path, _ = target["path"].(string)
		name, _ = target["name_path"].(string)
		path = filepath.ToSlash(filepath.Clean(path))
	}
	matches := []workspacecore.ExecutionNode{}
	for _, node := range graph.Nodes {
		if node.Kind != "symbol" {
			continue
		}
		if id != "" && (node.ID == id || node.Name == id) || name != "" && node.Path == path && node.Name == name {
			matches = append(matches, node)
		}
	}
	if len(matches) != 1 {
		return workspacecore.ExecutionNode{}, workspacecore.Codedf("graph_endpoint_ambiguous", "endpoint must identify exactly one captured function; matched %d", len(matches))
	}
	return matches[0], nil
}
func executionContentSelector(name string, selector preparedSelector) bool {
	return (name == "path_explain" || name == "execution_graph") && selector.PlanID == "" && strings.HasPrefix(selector.Revision, "content_")
}
