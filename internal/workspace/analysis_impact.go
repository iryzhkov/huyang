package workspace

// Impact derived from a snapshot rather than computed as one.
//
// The affected set, the untested files and the variants a change reaches are
// all questions about relations between files. They used to be answered by
// the import reader directly, which made its guesses indistinguishable from
// a language server's knowledge. They are now answered from the snapshot, so
// the same answer carries the snapshot it came from, every contributor that
// fed it, and what those contributors could not see.

import (
	"context"
	"sort"
)

// AnalyzeImpact builds a snapshot for one revision and derives the impact
// graph from it. Contributors beyond the native import reader are passed in
// by the caller, because the workspace core does not speak to providers.
func AnalyzeImpact(ctx context.Context, request AnalysisRequest, policy ImpactPolicy, variants []VariantPolicy, extra ...AnalysisContributor) (AnalysisSnapshot, ImpactGraph, error) {
	contributors := append([]AnalysisContributor{NativeImportContributor{Policy: policy, Variants: variants}}, extra...)
	snapshot, err := BuildAnalysisSnapshot(ctx, request, contributors...)
	if err != nil {
		return snapshot, ImpactGraph{}, err
	}
	return snapshot, ImpactFromSnapshot(snapshot, request.Changed, variants), nil
}

// ImpactFromSnapshot reads the file-level relations out of a snapshot and
// answers the questions the verification pipeline asks of them.
func ImpactFromSnapshot(snapshot AnalysisSnapshot, changed []string, variants []VariantPolicy) ImpactGraph {
	graph := ImpactGraph{
		Revision: snapshot.Key.Revision, Changed: uniqueSorted(changed),
		Snapshot: snapshot.ID, Producers: snapshot.Producers(), Coverage: snapshot.Coverage,
	}
	tests := map[string]bool{}
	for _, node := range snapshot.Nodes {
		if node.Kind == NodeSymbol {
			continue
		}
		graph.Nodes = append(graph.Nodes, ImpactNode{
			Path: node.Path, Language: node.Language,
			Test:      node.Kind == NodeTest,
			Generated: node.Kind == NodeGenerated,
			Config:    node.Kind == NodeConfiguration,
		})
		tests[node.Path] = node.Kind == NodeTest
	}
	for _, fact := range snapshot.Facts {
		from, fromOK := filePath(snapshot, fact.From)
		to, toOK := filePath(snapshot, fact.To)
		if !fromOK || !toOK || from == to {
			continue
		}
		producer := strongestProducer(fact.Producers)
		graph.Edges = append(graph.Edges, ImpactEdge{
			From: from, To: to, Kind: string(fact.Kind),
			Adapter: producer.Name, Confidence: producer.Confidence,
		})
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].Path < graph.Nodes[j].Path })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From == graph.Edges[j].From {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		return graph.Edges[i].From < graph.Edges[j].From
	})
	graph.Affected = impactClosure(graph.Changed, graph.Edges)
	graph.Untested = untestedPaths(graph.Affected, graph.Edges, tests)
	graph.Adapters = adaptersFor(graph.Nodes)
	applyVariants(&graph, variants)
	if snapshot.Coverage.Capped {
		graph.Risks = append(graph.Risks, ImpactRisk{Kind: "graph_cap", Detail: "file or edge cap reached"})
	}
	for _, skipped := range snapshot.Coverage.Skipped {
		graph.Risks = append(graph.Risks, ImpactRisk{Kind: "incomplete_contributor", Detail: skipped})
	}
	graph.Coverage.Complete = snapshot.Coverage.Complete
	graph.RecommendFull = !graph.Coverage.Complete || len(graph.Untested) > 0 || len(graph.Omitted) > 0
	return graph
}

// filePath is the file a node stands for: a file node is itself, a symbol
// node is the file it is declared in, and anything else is not a file.
func filePath(snapshot AnalysisSnapshot, id string) (string, bool) {
	for _, node := range snapshot.Nodes {
		if node.ID == id {
			return node.Path, node.Path != ""
		}
	}
	return "", false
}

// strongestProducer is the most confident claim behind a fact, which is what
// a derived answer should be labelled with.
func strongestProducer(producers []FactProducer) FactProducer {
	best := FactProducer{}
	for _, producer := range producers {
		if best.Name == "" || confidenceRank(DiagnosticConfidence(producer.Confidence)) > confidenceRank(DiagnosticConfidence(best.Confidence)) {
			best = producer
		}
	}
	return best
}

// untestedPaths are the affected files that nothing in the affected set
// covers, which is the question "did anything test this change".
func untestedPaths(affected []string, edges []ImpactEdge, tests map[string]bool) []string {
	covered := map[string]bool{}
	for _, edge := range edges {
		if tests[edge.From] {
			covered[edge.To] = true
		}
	}
	var untested []string
	for _, path := range affected {
		if !tests[path] && !covered[path] {
			untested = append(untested, path)
		}
	}
	return uniqueSorted(untested)
}

// applyVariants records which declared variants the affected set reaches.
func applyVariants(graph *ImpactGraph, variants []VariantPolicy) {
	for _, variant := range variants {
		if variantApplies(variant, graph.Affected) {
			graph.Included = append(graph.Included, variant.Name)
			continue
		}
		graph.Omitted = append(graph.Omitted, variant.Name)
		if variant.Required {
			graph.Risks = append(graph.Risks, ImpactRisk{Kind: "variant_omitted", Detail: variant.Name})
		}
	}
	graph.Included = uniqueSorted(graph.Included)
	graph.Omitted = uniqueSorted(graph.Omitted)
}
