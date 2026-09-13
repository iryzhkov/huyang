package workspace

import (
	"context"
	"testing"
)

// previewSnapshot is a small world: an exported declaration, a caller, a test
// that exercises it, a generated file and a configuration file.
func previewSnapshot(t *testing.T) AnalysisSnapshot {
	t.Helper()
	builder := newAnalysisBuilder(AnalysisCaps{}.withDefaults())
	authoritative := FactProducer{Name: "embedded_nvim", Confidence: string(ConfidenceAuthoritative)}
	provisional := FactProducer{Name: "native_imports", Confidence: string(ConfidenceProvisional)}
	for _, node := range []AnalysisNode{
		{Kind: NodeFile, Path: "ledger.go"},
		{Kind: NodeFile, Path: "report.go"},
		{Kind: NodeTest, Path: "ledger_test.go"},
		{Kind: NodeGenerated, Path: "zz_generated.go"},
		{Kind: NodeConfiguration, Path: "config.toml"},
		{Kind: NodeSymbol, Path: "ledger.go", NamePath: "Total"},
	} {
		builder.Node(node)
	}
	symbol := NodeID(NodeSymbol, "ledger.go", "Total")
	builder.Fact(NodeID(NodeFile, "ledger.go", ""), symbol, EdgeExports, authoritative)
	builder.Fact(NodeID(NodeFile, "report.go", ""), symbol, EdgeCalls, authoritative)
	builder.Fact(NodeID(NodeFile, "ledger_test.go", ""), symbol, EdgeCalls, authoritative)
	builder.Fact(NodeID(NodeFile, "report.go", ""), NodeID(NodeFile, "ledger.go", ""), EdgeImports, provisional)
	return builder.finish(AnalysisKey{WorkspaceID: "ws_x", Epoch: 1, Revision: "wsrev_7", Profile: "impact_preview/v1"})
}

// The question before an edit: who calls this, is it public, is anything
// testing it, and what is involved that nobody mentioned.
func TestImpactPreviewAnswersTheQuestionBeforeAnEdit(t *testing.T) {
	snapshot := previewSnapshot(t)
	preview := PreviewImpact(snapshot, "preview_a1", []ImpactTarget{
		{OpID: "retype", Kind: "replace_symbol", Path: "ledger.go", NamePath: "Total"},
	}, nil)

	if preview.AnalysedRevision != "wsrev_7" || preview.PreviewRevision != "preview_a1" {
		t.Fatalf("the preview confuses the code it read with the change it weighed: %#v", preview)
	}
	if !preview.Targets[0].Exported {
		t.Fatal("a declaration the file exports was not reported as public")
	}
	// One entry per place and target: report.go both calls the declaration
	// and imports its file, and those are different claims from different
	// contributors.
	callers := map[string]string{}
	for _, caller := range preview.Callers {
		callers[caller.Path+"#"+caller.Target] = caller.Confidence
	}
	if callers["report.go#Total"] != string(ConfidenceAuthoritative) {
		t.Fatalf("the declaration's caller is missing or unattributed: %#v", preview.Callers)
	}
	if callers["report.go#ledger.go"] != string(ConfidenceProvisional) {
		t.Fatalf("the file-level import lost its lower confidence: %#v", preview.Callers)
	}
	if len(preview.AssociatedTests) != 1 || preview.AssociatedTests[0] != "ledger_test.go" {
		t.Fatalf("associated tests = %#v", preview.AssociatedTests)
	}
	// The declaration's own file is not a caller of itself.
	if _, present := callers["ledger.go"]; present {
		t.Fatalf("the changed file was reported as its own caller: %#v", preview.Callers)
	}
}

// A file operation has no declaration, and the answer is about the file. A
// change with nothing testing it says so.
func TestImpactPreviewReportsUntestedFileOperations(t *testing.T) {
	snapshot := previewSnapshot(t)
	preview := PreviewImpact(snapshot, "preview_b2", []ImpactTarget{
		{OpID: "drop", Kind: "delete_file", Path: "zz_generated.go"},
		{OpID: "tune", Kind: "replace_literal", Path: "config.toml"},
	}, []VariantPolicy{{Name: "linux", Files: []string{"**/*.go"}, Required: true}})

	if len(preview.Generated) != 1 || preview.Generated[0] != "zz_generated.go" {
		t.Fatalf("generated involvement = %#v", preview.Generated)
	}
	if len(preview.Configuration) != 1 || preview.Configuration[0] != "config.toml" {
		t.Fatalf("configuration involvement = %#v", preview.Configuration)
	}
	if len(preview.UntestedPaths) != 2 {
		t.Fatalf("nothing tests either file, so both are untested: %#v", preview.UntestedPaths)
	}
	if !preview.RecommendFull {
		t.Fatal("a change with no associated test did not recommend broader verification")
	}
}

// What nobody could see is part of the answer: a contributor that failed and
// a cap that was reached both appear, and the preview does not call itself
// complete.
func TestImpactPreviewCarriesWhatNobodyCouldSee(t *testing.T) {
	snapshot, err := BuildAnalysisSnapshot(context.Background(),
		AnalysisRequest{Key: AnalysisKey{Revision: "wsrev_9"}, Caps: AnalysisCaps{MaxFacts: 1}},
		stubContributor{name: "native_imports", version: "1", claims: []AnalysisFact{
			{From: "a.go", To: "b.go", Kind: EdgeImports},
			{From: "a.go", To: "c.go", Kind: EdgeImports},
		}},
		stubContributor{name: "embedded_nvim", version: "1", fail: context.DeadlineExceeded},
	)
	if err != nil {
		t.Fatal(err)
	}
	preview := PreviewImpact(snapshot, "preview_c3", []ImpactTarget{{OpID: "x", Path: "a.go"}}, nil)
	if len(preview.Unresolved) == 0 {
		t.Fatalf("the preview hides what it could not see: %#v", preview)
	}
	if preview.Coverage.Complete || !preview.RecommendFull {
		t.Fatalf("an incomplete preview did not say so: %#v", preview.Coverage)
	}
}
