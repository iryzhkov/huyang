package workspace

// Helpers for answers derived from an analysis snapshot rather than computed
// by the import reader directly, so each answer can carry the producer it
// came from and the variants the change reaches.

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
