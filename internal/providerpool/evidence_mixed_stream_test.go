package providerpool

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestMixedStreamSelectionAndLateLedger(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, pushFirst := range []bool{false, true} {
			for _, shared := range []bool{false, true} {
				t.Run(fmt.Sprintf("late_%t/push_first_%t/shared_%t", late, pushFirst, shared), func(t *testing.T) {
					root := t.TempDir()
					w, err := workspacecore.New(workspacecore.KindProject, root, 1)
					if err != nil {
						t.Fatal(err)
					}
					v := int64(4)
					push := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "rust_analyzer#1",
						Producer: "rust_analyzer", Document: filepath.Join(root, "a.go"), Selected: true, Complete: true,
						DocumentVersion: &v, ExpectedVersion: &v,
						Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Code: "E0308", Source: "rustc", Message: "mismatched types"}}}
					pull := push
					pull.Kind, pull.Complete, pull.DocumentVersion = workspacecore.EvidencePull, false, nil
					pull.ResultID = "rust-analyzer"
					if !shared {
						pull.Findings = []workspacecore.DiagnosticFinding{{Severity: 1, Code: "E0308",
							Source: "rust-analyzer", Message: "expected i32, found &'static str"}}
					}
					batches := []workspacecore.DiagnosticBatch{pull, push}
					if pushFirst {
						batches = []workspacecore.DiagnosticBatch{push, pull}
					}
					p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
						ID: "test", Root: root, Epoch: 1,
					}}, batches: batches}
					if late {
						if count, err := CollectLateEvidence(context.Background(), w, p); err != nil || count != 2 {
							t.Fatalf("late lanes count=%d err=%v", count, err)
						}
					} else {
						report, err := RecordDiagnostics(context.Background(), w, p,
							[]workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}}, "rev_B", "edit_B")
						want := 2
						if shared {
							want = 1
						}
						if err != nil || len(report.New) != want || len(report.Current) != want ||
							report.Confidence != workspacecore.ConfidenceProvisional {
							t.Fatalf("immediate report lost lane or duplicated ID: %+v %v", report, err)
						}
					}
					before, err := w.Diagnostics("")
					want := 2
					if shared {
						want = 1
					}
					if err != nil || before.CurrentCount != want || before.Confidence != workspacecore.ConfidenceProvisional {
						t.Fatalf("mixed query: %+v %v", before, err)
					}
					// A complete empty push withdraws the distinct push-only ID.
					// The shared ID has pull history and must remain.
					push.Findings = nil
					p.batches = []workspacecore.DiagnosticBatch{push}
					if _, err := CollectLateEvidence(context.Background(), w, p); err != nil {
						t.Fatal(err)
					}
					after, _ := w.Diagnostics(before.Cursor)
					wantResolved := 1
					if shared {
						wantResolved = 0
					}
					if after.CurrentCount != 1 || len(after.Resolved) != wantResolved ||
						after.Confidence != workspacecore.ConfidenceProvisional {
						t.Fatalf("empty push falsely withdrew independent pull: %+v", after)
					}
					p.batches = nil
					if count, err := CollectLateEvidence(context.Background(), w, p); err != nil || count != 0 {
						t.Fatalf("empty later collection count=%d err=%v", count, err)
					}
					drained, _ := w.Diagnostics(after.Cursor)
					if drained.CurrentCount != 1 || drained.Confidence != workspacecore.ConfidenceProvisional ||
						len(drained.Resolved) != 0 || len(drained.New) != 0 {
						t.Fatalf("draining fabricated clean coverage: %+v", drained)
					}
				})
			}
		}
	}
}

func TestProvisionalEmptyCoalescingPreservesUndeliveredPositive(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late_%t", late), func(t *testing.T) {
			root := t.TempDir()
			w, err := workspacecore.New(workspacecore.KindProject, root, 1)
			if err != nil {
				t.Fatal(err)
			}
			positive := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePull, ProviderID: "test#1", Producer: "test",
				Document: filepath.Join(root, "a.go"), Selected: true, ResultID: "positive",
				Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "still present"}}}
			empty := positive
			empty.Findings, empty.ResultID = nil, "empty"
			p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
				ID: "test", Root: root, Epoch: 1,
			}}, batches: []workspacecore.DiagnosticBatch{positive, empty}}
			if late {
				if _, err := CollectLateEvidence(context.Background(), w, p); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := RecordDiagnostics(context.Background(), w, p,
					[]workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}}, "rev", "edit"); err != nil {
					t.Fatal(err)
				}
			}
			report, _ := w.Diagnostics("")
			if report.CurrentCount != 1 || len(report.Resolved) != 0 || report.Confidence != workspacecore.ConfidenceProvisional {
				t.Fatalf("provisional empty discarded undelivered fact: %+v", report)
			}
		})
	}
}

func TestMixedStreamEmptyPushRetainsPositivePull(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, pushFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("late_%t/push_first_%t", late, pushFirst), func(t *testing.T) {
				root := t.TempDir()
				w, err := workspacecore.New(workspacecore.KindProject, root, 1)
				if err != nil {
					t.Fatal(err)
				}
				v := int64(4)
				push := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
					Document: filepath.Join(root, "a.go"), Selected: true, Complete: true,
					DocumentVersion: &v, ExpectedVersion: &v}
				pull := push
				pull.Kind, pull.DocumentVersion, pull.Complete = workspacecore.EvidencePull, nil, false
				pull.Findings = []workspacecore.DiagnosticFinding{{Severity: 1, Source: "rust-analyzer",
					Code: "E0308", Message: "expected i32, found &'static str"}}
				batches := []workspacecore.DiagnosticBatch{pull, push}
				if pushFirst {
					batches = []workspacecore.DiagnosticBatch{push, pull}
				}
				p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
					ID: "test", Root: root, Epoch: 1,
				}}, batches: batches}
				if late {
					if _, err := CollectLateEvidence(context.Background(), w, p); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := RecordDiagnostics(context.Background(), w, p,
						[]workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}}, "rev", "edit"); err != nil {
						t.Fatal(err)
					}
				}
				report, err := w.Diagnostics("")
				if err != nil || report.CurrentCount != 1 || len(report.New) != 1 || len(report.Resolved) != 0 ||
					report.Confidence != workspacecore.ConfidenceProvisional {
					t.Fatalf("complete empty push swallowed positive pull: %+v %v", report, err)
				}
			})
		}
	}
}

func TestMixedStreamStagedReportDeduplicatesSharedIDs(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(4)
	push := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
		Document: filepath.Join(root, "a.go"), Complete: true, DocumentVersion: &v, ExpectedVersion: &v,
		Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "shared"}}}
	pull := push
	pull.Kind, pull.DocumentVersion, pull.Complete = workspacecore.EvidencePull, nil, false
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
		ID: "test", Root: root, Epoch: 1,
	}}, batches: []workspacecore.DiagnosticBatch{pull, push}}
	report, err := RecordStagedDiagnostics(context.Background(), w, p,
		[]workspacecore.PlanStageFile{{Path: "a.go", AfterExists: true}}, "staged", "prepare")
	if err != nil || len(report.New) != 1 || len(report.Current) != 1 || report.CurrentCount != 1 ||
		report.Confidence != workspacecore.ConfidenceProvisional {
		t.Fatalf("staged mixed report: %+v %v", report, err)
	}
	canonical, _ := w.Diagnostics("")
	if canonical.CurrentCount != 0 || canonical.StaleCount != 0 || len(canonical.Resolved) != 0 {
		t.Fatalf("staged streams changed canonical membership: %+v", canonical)
	}
}

func TestSupersededPositiveRetainsFactsWithoutOmissionProof(t *testing.T) {
	root := t.TempDir()
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(4)
	push := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1", Producer: "test",
		Document: filepath.Join(root, "a.go"), Selected: true, Complete: true,
		DocumentVersion: &v, ExpectedVersion: &v,
		Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "old finding"}}}
	initial, err := w.RecordDiagnosticEvidence(push)
	if err != nil {
		t.Fatal(err)
	}
	positive := push
	positive.Findings = []workspacecore.DiagnosticFinding{{Severity: 1, Message: "undelivered positive"}}
	empty := positive
	empty.Complete, empty.DocumentVersion, empty.Findings = false, nil, nil
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{
		ID: "test", Root: root, Epoch: 1,
	}}, batches: []workspacecore.DiagnosticBatch{positive, empty}}
	if _, err := CollectLateEvidence(context.Background(), w, p); err != nil {
		t.Fatal(err)
	}
	report, _ := w.Diagnostics(initial.Cursor)
	if report.CurrentCount != 2 || len(report.New) != 1 || len(report.Resolved) != 0 ||
		report.Confidence != workspacecore.ConfidenceProvisional {
		t.Fatalf("superseded positive retained omission proof or lost facts: %+v", report)
	}
}
