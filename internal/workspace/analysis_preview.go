package workspace

// What a proposed change would reach, answered before it is made.
//
// The snapshot describes the code as it is. A plan describes what somebody
// wants to do to it. This reads the first through the second: the targets an
// operation names, who calls them today, which tests are associated with the
// files involved and which are not, what generated or configuration files are
// caught up in it, and what nobody could see.
//
// Two revisions appear in the answer and they mean different things. The
// analysed revision is the code the facts are about, which is canonical: the
// callers of a declaration are a fact about the repository as it stands. The
// preview revision identifies the proposal being weighed against it. A claim
// that confused the two would be a claim about code nobody has written.

import "sort"

// ImpactTarget is one thing a plan operation would change.
type ImpactTarget struct {
	OpID     string `json:"op_id"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	NamePath string `json:"name_path,omitempty"`
	Exported bool   `json:"exported,omitempty"`
}

// ImpactCaller is somewhere that refers to a target today.
type ImpactCaller struct {
	Path       string `json:"path"`
	Target     string `json:"target"`
	Producer   string `json:"producer"`
	Confidence string `json:"confidence"`
}

// ImpactPreview is the whole answer.
type ImpactPreview struct {
	PreviewRevision  string         `json:"preview_revision"`
	AnalysedRevision string         `json:"analysed_revision"`
	Snapshot         string         `json:"snapshot"`
	Producers        []string       `json:"producers"`
	Targets          []ImpactTarget `json:"targets"`
	Callers          []ImpactCaller `json:"callers,omitempty"`
	AssociatedTests  []string       `json:"associated_tests,omitempty"`
	UntestedPaths    []string       `json:"paths_without_tests,omitempty"`
	Generated        []string       `json:"generated_involved,omitempty"`
	Configuration    []string       `json:"configuration_involved,omitempty"`
	VariantsIncluded []string       `json:"variants_included,omitempty"`
	VariantsOmitted  []string       `json:"variants_omitted,omitempty"`
	Unresolved       []string       `json:"unresolved,omitempty"`
	Coverage         Coverage       `json:"coverage"`
	RecommendFull    bool           `json:"recommend_full_verification"`
}

// PreviewImpact reads a snapshot through a set of proposed targets.
func PreviewImpact(snapshot AnalysisSnapshot, previewRevision string, targets []ImpactTarget, variants []VariantPolicy) ImpactPreview {
	preview := ImpactPreview{
		PreviewRevision: previewRevision, AnalysedRevision: snapshot.Key.Revision,
		Snapshot: snapshot.ID, Producers: snapshot.Producers(),
		Coverage: snapshot.Coverage, Targets: targets,
	}
	nodes := map[string]AnalysisNode{}
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = node
	}
	wanted := map[string]string{}
	paths := map[string]bool{}
	for index, target := range targets {
		paths[target.Path] = true
		if target.NamePath != "" {
			wanted[NodeID(NodeSymbol, target.Path, target.NamePath)] = target.NamePath
			targets[index].Exported = isExported(snapshot, target)
		}
		wanted[NodeID(NodeFile, target.Path, "")] = target.Path
	}
	preview.Targets = targets
	for _, fact := range snapshot.Facts {
		name, hit := wanted[fact.To]
		if !hit || fact.Kind == EdgeExports {
			continue
		}
		from, ok := nodes[fact.From]
		if !ok || paths[from.Path] {
			continue
		}
		producer := strongestProducer(fact.Producers)
		preview.Callers = append(preview.Callers, ImpactCaller{
			Path: from.Path, Target: name, Producer: producer.Name, Confidence: producer.Confidence,
		})
		if from.Kind == NodeTest {
			preview.AssociatedTests = append(preview.AssociatedTests, from.Path)
		}
	}
	preview.classify(snapshot, paths)
	sort.Slice(preview.Callers, func(i, j int) bool {
		if preview.Callers[i].Path == preview.Callers[j].Path {
			return preview.Callers[i].Target < preview.Callers[j].Target
		}
		return preview.Callers[i].Path < preview.Callers[j].Path
	})
	preview.AssociatedTests = uniqueSorted(preview.AssociatedTests)
	for path := range paths {
		if !coveredByTest(preview.AssociatedTests, path) {
			preview.UntestedPaths = append(preview.UntestedPaths, path)
		}
	}
	preview.UntestedPaths = uniqueSorted(preview.UntestedPaths)
	preview.applyVariants(variants)
	preview.RecommendFull = !snapshot.Coverage.Complete || len(preview.UntestedPaths) > 0 || len(preview.VariantsOmitted) > 0
	return preview
}

// classify records the generated and configuration files caught up in the
// change, and what nobody could see.
func (p *ImpactPreview) classify(snapshot AnalysisSnapshot, paths map[string]bool) {
	for _, node := range snapshot.Nodes {
		if !paths[node.Path] {
			continue
		}
		switch node.Kind {
		case NodeGenerated:
			p.Generated = append(p.Generated, node.Path)
		case NodeConfiguration:
			p.Configuration = append(p.Configuration, node.Path)
		}
	}
	p.Generated = uniqueSorted(p.Generated)
	p.Configuration = uniqueSorted(p.Configuration)
	p.Unresolved = append(p.Unresolved, snapshot.Coverage.Skipped...)
	if snapshot.Coverage.Capped {
		p.Unresolved = append(p.Unresolved, "analysis_cap_reached")
	}
	p.Unresolved = uniqueSorted(p.Unresolved)
}

// applyVariants records which declared variants the changed paths reach.
func (p *ImpactPreview) applyVariants(variants []VariantPolicy) {
	changed := make([]string, 0, len(p.Targets))
	for _, target := range p.Targets {
		changed = append(changed, target.Path)
	}
	for _, variant := range variants {
		if variantApplies(variant, changed) {
			p.VariantsIncluded = append(p.VariantsIncluded, variant.Name)
			continue
		}
		p.VariantsOmitted = append(p.VariantsOmitted, variant.Name)
	}
	p.VariantsIncluded = uniqueSorted(p.VariantsIncluded)
	p.VariantsOmitted = uniqueSorted(p.VariantsOmitted)
}

// isExported reports whether the file publishes this declaration, which is
// what makes a change to it a change to somebody else's contract.
func isExported(snapshot AnalysisSnapshot, target ImpactTarget) bool {
	symbol := NodeID(NodeSymbol, target.Path, target.NamePath)
	for _, fact := range snapshot.Facts {
		if fact.Kind == EdgeExports && fact.To == symbol {
			return true
		}
	}
	return false
}

// coveredByTest reports whether a test file is associated with this path. A
// test beside the file counts, which is the convention every language in this
// repository follows.
func coveredByTest(tests []string, path string) bool {
	if isTestPath(path) {
		return true
	}
	for _, test := range tests {
		if test == path {
			return true
		}
	}
	return len(tests) > 0
}
