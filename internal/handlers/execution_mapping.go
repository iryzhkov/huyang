package handlers

import (
	"path/filepath"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A server may hold an unsaved buffer even while the disk revision remains
// unchanged. Accepting its locations under the disk hash would invent source
// evidence, so every internal symbol must name a matching provider buffer.
func executionBatchMatchesSources(workspace *workspacecore.Workspace, sources []workspacecore.ExecutionSource, batch executionProviderBatch) bool {
	expected := map[string]string{}
	for _, source := range sources {
		expected[filepath.Join(workspace.Identity().Root, source.Path)] = source.Hash
	}
	matches := func(symbol executionProviderSymbol) bool {
		path := filepath.Clean(symbol.File)
		hash, internal := expected[path]
		return !internal || batch.Sources[path] == hash
	}
	for _, node := range batch.Nodes {
		if !matches(node) {
			return false
		}
	}
	for _, edge := range batch.Edges {
		if !matches(edge.Source) || !matches(edge.Target) {
			return false
		}
	}
	for _, reference := range batch.References {
		if !matches(reference.Target) || !matches(executionProviderSymbol{File: reference.File}) {
			return false
		}
	}
	return true
}
