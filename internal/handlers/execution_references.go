package handlers

import (
	"encoding/json"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const maxExecutionNodeProducers = 64

func mergeExecutionProviderNode(nodes map[string]workspacecore.ExecutionNode, node workspacecore.ExecutionNode) {
	existing, ok := nodes[node.ID]
	if !ok {
		nodes[node.ID] = node
		return
	}
	for _, candidate := range node.Evidence {
		encoded, _ := json.Marshal(candidate)
		found := false
		for _, evidence := range existing.Evidence {
			other, _ := json.Marshal(evidence)
			if string(encoded) == string(other) {
				found = true
				break
			}
		}
		if !found && len(existing.Evidence) < maxExecutionNodeProducers {
			existing.Evidence = append(existing.Evidence, candidate)
		}
	}
	nodes[node.ID] = existing
}

// References are retained as data dependencies. They do not enter call-path
// traversal until a call adapter supplies evidence for that stronger relation.
func decodeExecutionReferences(batch executionProviderBatch, known map[string]workspacecore.ExecutionSource, revision string, nodes map[string]workspacecore.ExecutionNode, facts *workspacecore.ExecutionCallFacts) {
	for _, ref := range batch.References {
		var owner executionProviderSymbol
		for _, node := range batch.Nodes {
			if node.File == ref.File && node.Line <= ref.Line && node.EndLine >= ref.Line && node.Line >= owner.Line {
				owner = node
			}
		}
		if owner.File == "" {
			continue
		}
		evidence := []workspacecore.ExecutionEvidence{{Revision: revision, Producer: workspacecore.ProducerVersion{Name: ref.Producer, Version: ref.Version},
			Classification: "static", Confidence: "heuristic", SourceHandles: []string{}, EvidenceIDs: []string{},
			Coverage: workspacecore.ExecutionCoverage{Complete: false, Gaps: []string{"reference_is_not_a_call"}, Limits: []string{}}}}
		from := executionProviderNode(owner, known, evidence)
		to := executionProviderNode(ref.Target, known, evidence)
		mergeExecutionProviderNode(nodes, from)
		mergeExecutionProviderNode(nodes, to)
		facts.Edges = append(facts.Edges, workspacecore.ExecutionEdge{From: from.ID, To: to.ID, Kind: "data_dependency",
			SitePath: from.Path, SiteLine: ref.Line, SiteColumn: 1, Evidence: evidence})
	}
}
