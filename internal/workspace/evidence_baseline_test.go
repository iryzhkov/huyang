package workspace

import "testing"

func TestFirstTouchWithoutBaselineIsLabelledAndCleanBaselineProvesNew(t *testing.T) {
	for _, baseline := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "clean_baseline"}[baseline], func(t *testing.T) {
			w, _ := evidenceWorkspace(t)
			v := int64(1)
			batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go",
				Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v}
			if baseline {
				batch.DocumentRevision = "before"
				batch.PreEditDocumentRevision = "before"
				batch.Baseline = true
				if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
					t.Fatal(err)
				}
				batch.Baseline = false
			}
			batch.Findings = []DiagnosticFinding{finding("error")}
			report, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.New) != 1 || report.New[0].BaselineMissing == baseline {
				t.Fatalf("baseline=%v report=%+v", baseline, report)
			}
		})
	}
}

func TestPreEditBaselineDoesNotAnnouncePreexistingFinding(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	v := int64(1)
	batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go",
		Complete: true, Selected: true, Baseline: true, DocumentVersion: &v, ExpectedVersion: &v, Findings: []DiagnosticFinding{finding("old")}}
	before, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.New) != 0 || len(w.DiagnosticNotices(20)) != 0 {
		t.Fatalf("baseline announced as new: %+v", before)
	}
	batch.Baseline = false
	after, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.New) != 0 || after.PreexistingCount != 1 {
		t.Fatalf("preexisting finding announced: %+v", after)
	}
}
