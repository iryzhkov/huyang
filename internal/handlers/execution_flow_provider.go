package handlers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type syntaxFlowBatch struct {
	Sources map[string]string             `json:"sources"`
	Nodes   []workspacecore.ExecutionNode `json:"nodes"`
	Edges   []workspacecore.ExecutionEdge `json:"edges"`
	Gaps    []string                      `json:"gaps"`
	Capped  bool                          `json:"capped"`
}

func (h *Handlers) acquireSyntaxFlow(ctx context.Context, w *workspacecore.Workspace, sources []workspacecore.ExecutionSource, revision string, nodes map[string]workspacecore.ExecutionNode, selected []string, enabled bool) workspacecore.ExecutionCallFacts {
	facts := workspacecore.ExecutionCallFacts{Producer: workspacecore.ProducerVersion{Name: "treesitter_flow", Version: "1"},
		Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"syntax_slices_do_not_prove_control_flow"}, Limits: []string{}}}
	if !enabled {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "flow_provider_disabled")
		return facts
	}
	selections := []map[string]any{}
	expected := map[string]string{}
	for _, source := range sources {
		expected[source.Path] = source.Hash
	}
	for _, id := range selected {
		node := nodes[id]
		selections = append(selections, map[string]any{"file": filepath.Join(w.Identity().Root, node.Path), "path": node.Path, "id": id, "line": node.Line, "col": node.Column})
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(executionProviderMillis)*time.Millisecond)
	defer cancel()
	value, err := h.callProvider(ctx, "execution_flow", w, "huyang_execution_flow", map[string]any{"selections": selections})
	if err != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "flow_provider_unavailable")
		return facts
	}
	content, err := json.Marshal(value)
	var batch syntaxFlowBatch
	if err != nil || len(content) > workspacecore.MaxExecutionBytes || json.Unmarshal(content, &batch) != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "flow_batch_invalid")
		return facts
	}
	for _, node := range batch.Nodes {
		hash, ok := expected[node.Path]
		if !ok || batch.Sources[filepath.Join(w.Identity().Root, node.Path)] != hash {
			facts.Coverage.Gaps = append(facts.Coverage.Gaps, "flow_source_mapping_ambiguous")
			return facts
		}
	}
	facts.Coverage.Capped = batch.Capped
	facts.Coverage.Gaps = append(facts.Coverage.Gaps, batch.Gaps...)
	evidence := func() []workspacecore.ExecutionEvidence {
		return []workspacecore.ExecutionEvidence{{Revision: revision, Producer: facts.Producer, Confidence: "parser", Classification: "static",
			SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: facts.Coverage}}
	}
	for i := range batch.Nodes {
		batch.Nodes[i].Evidence = evidence()
	}
	byID := map[string]workspacecore.ExecutionNode{}
	for _, node := range batch.Nodes {
		byID[node.ID] = node
	}
	for i := range batch.Edges {
		edge := &batch.Edges[i]
		node := byID[edge.From]
		edge.SitePath, edge.SiteLine, edge.SiteColumn = node.Path, node.Line, node.Column
		edge.Evidence = evidence()
	}
	facts.Nodes, facts.Edges = batch.Nodes, batch.Edges
	return facts
}
