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
	notices := w.DiagnosticNotices(20)
	if len(before.New) != 0 || len(notices) != 1 || notices[0].Kind != "persisting" {
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

func TestPreEditBaselineNeverResolvesOmittedFindings(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "partial"}[partial], func(t *testing.T) {
			w, _ := evidenceWorkspace(t)
			v := int64(1)
			batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go",
				Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v,
				Findings: []DiagnosticFinding{finding("first"), finding("omitted")}}
			initial, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			batch.Baseline = true
			batch.Findings = nil
			if partial {
				batch.Findings = []DiagnosticFinding{finding("first"), finding("seeded")}
			}
			seeded, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 2
			if partial {
				wantCount++
			}
			if len(seeded.New) != 0 || len(seeded.Resolved) != 0 || seeded.CurrentCount != wantCount {
				t.Fatalf("baseline changed absence into resolution: %+v", seeded)
			}
			batch.Baseline = false
			batch.Findings = []DiagnosticFinding{finding("first"), finding("omitted")}
			if partial {
				batch.Findings = append(batch.Findings, finding("seeded"))
			}
			after, err := w.RecordDiagnosticEvidence(batch)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.New) != 0 || len(after.Resolved) != 0 || after.PreexistingCount != wantCount {
				t.Fatalf("baseline caused a false return: %+v", after)
			}
			report, err := w.Diagnostics(initial.Cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, notice := range report.Notices {
				if notice.Kind == "new" || notice.Kind == "resolved" {
					t.Fatalf("baseline emitted a false transition: %+v", notice)
				}
			}
		})
	}
}
