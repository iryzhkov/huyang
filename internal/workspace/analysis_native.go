package workspace

// The native contributor: the import reader that used to be the whole impact
// model, now one voice among several. It reads what a file says it depends on
// with a regular expression, which is cheap, needs no language server, and is
// right often enough to be worth having and wrong often enough that its claims
// must say who made them.

import "context"

// NativeImportContributor turns the static import graph into snapshot facts.
type NativeImportContributor struct {
	Policy   ImpactPolicy
	Variants []VariantPolicy
}

func (NativeImportContributor) Name() string { return "native_imports" }

// Version is the shape of the reader itself: a change to the patterns is a
// change to what it will claim, so it invalidates every snapshot it fed.
func (NativeImportContributor) Version() string { return "1" }

// nativeConfidence is what a regular expression over imports earns. It is
// never authoritative: it cannot see a dynamic import, a build tag or a
// generated file, and the snapshot has to keep that difference visible.
const nativeConfidence = "provisional"

func (c NativeImportContributor) Contribute(ctx context.Context, request AnalysisRequest, builder *AnalysisBuilder) error {
	graph, err := BuildImpactGraph(request.Root, request.Key.Revision, request.Changed, c.Policy, c.Variants)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, node := range graph.Nodes {
		builder.Node(AnalysisNode{
			ID: NodeID(nativeNodeKind(node), node.Path, ""), Kind: nativeNodeKind(node),
			Path: node.Path, Language: node.Language,
		})
	}
	for _, edge := range graph.Edges {
		builder.Fact(
			NodeID(NodeFile, edge.From, ""), NodeID(NodeFile, edge.To, ""), nativeEdgeKind(edge.Kind),
			FactProducer{
				Name: c.Name(), Confidence: nativeConfidence,
				Coverage: graph.Coverage, Detail: edge.Adapter,
			},
		)
	}
	for _, risk := range graph.Risks {
		builder.Skipped("native_imports: " + risk.Kind)
	}
	builder.Covered(graph.Coverage)
	return nil
}

// nativeNodeKind maps the import reader's flags onto node kinds. A file is
// only one of these, and the order is the one a reader would use: a generated
// test is a test.
func nativeNodeKind(node ImpactNode) AnalysisNodeKind {
	switch {
	case node.Test:
		return NodeTest
	case node.Generated:
		return NodeGenerated
	case node.Config:
		return NodeConfiguration
	default:
		return NodeFile
	}
}

// nativeEdgeKind maps the reader's edge labels onto the typed vocabulary.
func nativeEdgeKind(kind string) AnalysisEdgeKind {
	switch kind {
	case "covers":
		return EdgeCovers
	case "configures":
		return EdgeConfigures
	case "generates":
		return EdgeGenerates
	default:
		return EdgeImports
	}
}
