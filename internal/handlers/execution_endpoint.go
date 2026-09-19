package handlers

import (
	"fmt"
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
	// The graph keys a function by its leaf name, while every other tool of
	// this API names a declaration Parent/Name. An endpoint written the way
	// read and edit_apply take it matched nothing at all, so both forms are
	// accepted and the leaf is what is compared.
	leaf := leafName(canonicalNamePath(name))
	idLeaf := leafName(canonicalNamePath(id))
	matches := []workspacecore.ExecutionNode{}
	inFile := []string{}
	for _, node := range graph.Nodes {
		if node.Kind != "symbol" {
			continue
		}
		if path != "" && node.Path == path {
			inFile = append(inFile, node.Name)
		}
		if id != "" && (node.ID == id || node.Name == id || node.Name == idLeaf) || leaf != "" && node.Path == path && node.Name == leaf {
			matches = append(matches, node)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	// A miss and a double are different mistakes: one needs another name,
	// the other needs the exact id, and both are in the graph that was just
	// built.
	if len(matches) == 0 {
		if len(inFile) > 0 {
			return workspacecore.ExecutionNode{}, workspacecore.Codedf("graph_endpoint_unknown",
				"no captured function named %q in %s; the graph captured %s", nameOrID(name, id), path, strings.Join(boundedNames(inFile), ", "))
		}
		return workspacecore.ExecutionNode{}, workspacecore.Codedf("graph_endpoint_unknown",
			"no captured function matches %q; execution_graph lists what was captured", nameOrID(name, id))
	}
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.ID)
	}
	return workspacecore.ExecutionNode{}, workspacecore.Codedf("graph_endpoint_ambiguous",
		"%d captured functions match; name one by id: %s", len(matches), strings.Join(boundedNames(ids), ", "))
}

func nameOrID(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// boundedNames keeps a suggestion list short enough to read.
func boundedNames(values []string) []string {
	const maxNames = 8
	if len(values) <= maxNames {
		return values
	}
	return append(values[:maxNames:maxNames], fmt.Sprintf("and %d more", len(values)-maxNames))
}

func executionContentSelector(name string, selector preparedSelector) bool {
	return (name == "path_explain" || name == "execution_graph") && selector.PlanID == "" && strings.HasPrefix(selector.Revision, "content_")
}
