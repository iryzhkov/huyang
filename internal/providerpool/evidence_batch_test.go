package providerpool

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type batchProvider struct {
	stubProvider
	batches []workspacecore.DiagnosticBatch
}

func (p *batchProvider) Call(context.Context, provider.Request) (provider.Result, error) {
	return provider.Result{Value: providerDiagnosticPayload{Batches: p.batches}}, nil
}

func TestMissingEditedDocumentBatchKeepsReplyProvisional(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(2)
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Backend: "test", Root: root, Epoch: 1}},
		batches: []workspacecore.DiagnosticBatch{{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: filepath.Join(root, "a.go"), Complete: true, DocumentVersion: &v, ExpectedVersion: &v}}}
	report, err := RecordDiagnostics(context.Background(), w, p, []workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}, {Path: "b.go", AfterExists: true}}, "wsrev_1", "tx")
	if err != nil {
		t.Fatal(err)
	}
	if report.Confidence != workspacecore.ConfidenceProvisional {
		t.Fatalf("missing document proof hidden: %+v", report)
	}
}

func TestRecordDiagnosticsAggregatesAllDocumentsAndDeltas(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	old, current := int64(1), int64(2)
	for _, staleFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "findings", true: "provisional"}[staleFirst], func(t *testing.T) {
			p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Backend: "test", Root: root, Epoch: 1}}}
			p.batches = []workspacecore.DiagnosticBatch{
				{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: filepath.Join(root, "a.go"), Complete: true, DocumentVersion: &current, ExpectedVersion: &current, Findings: []workspacecore.DiagnosticFinding{{Message: "first", Severity: 1}}},
				{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test", Document: filepath.Join(root, "b.go"), Complete: true, DocumentVersion: &current, ExpectedVersion: &current},
			}
			if staleFirst {
				p.batches[0].DocumentVersion = &old
			}
			report, err := RecordDiagnostics(context.Background(), w, p, []workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}, {Path: "b.go", AfterExists: true}}, "wsrev_1", "tx")
			if err != nil {
				t.Fatal(err)
			}
			if staleFirst {
				if report.Confidence != workspacecore.ConfidenceProvisional {
					t.Fatalf("first proof gap hidden: %+v", report)
				}
			} else if len(report.New) != 1 || len(report.EvidenceIDs) != 2 {
				t.Fatalf("earlier batch lost: %+v", report)
			}
		})
	}
}
