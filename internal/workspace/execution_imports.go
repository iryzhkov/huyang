package workspace

import "context"

// Import boundaries are dependencies, not calls. Keeping the existing reader
// in the snapshot makes unsupported-language fallback visible without turning
// an import statement into a claim that execution reaches the imported file.
func AcquireImportExecutionCalls(ctx context.Context, request ExecutionRequest, sources []ExecutionSource, policy PipelinePolicy) ExecutionCallFacts {
	facts := ExecutionCallFacts{Producer: ProducerVersion{Name: "native_import_boundary", Version: "1"},
		Coverage: ExecutionCoverage{Complete: false, Gaps: []string{"imports_are_not_calls"}, Limits: []string{}}}
	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		paths = append(paths, source.Path)
	}
	snapshot, err := BuildAnalysisSnapshot(ctx, AnalysisRequest{Key: request.Key, Root: request.Root, Changed: paths},
		NativeImportContributor{Policy: policy.Impact, Variants: policy.Variants})
	if err != nil {
		facts.Coverage.Gaps = append(facts.Coverage.Gaps, "import_graph_unavailable")
		return facts
	}
	facts.Coverage.Capped = snapshot.Coverage.Capped
	known := map[string]string{}
	for _, source := range sources {
		known["file:"+source.Path] = source.Path
	}
	evidence := func() []ExecutionEvidence {
		return []ExecutionEvidence{{
			Revision: request.Key.Revision, Producer: facts.Producer, Confidence: "heuristic", Classification: "static",
			SourceHandles: []string{}, EvidenceIDs: []string{}, Coverage: facts.Coverage,
		}}
	}
	for _, node := range snapshot.Nodes {
		path, ok := known[node.ID]
		if !ok {
			continue
		}
		facts.Nodes = append(facts.Nodes, ExecutionNode{ID: "source:" + path, Kind: "unresolved", Path: path, Evidence: evidence()})
	}
	for _, edge := range snapshot.Facts {
		from, fromOK := known[edge.From]
		to, toOK := known[edge.To]
		if !fromOK || !toOK {
			continue
		}
		facts.Edges = append(facts.Edges, ExecutionEdge{From: "source:" + from, To: "source:" + to, Kind: "data_dependency", Evidence: evidence()})
	}
	return facts
}
