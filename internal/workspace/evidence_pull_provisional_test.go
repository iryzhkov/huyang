package workspace

import (
	"path/filepath"
	"testing"
)

func TestUnversionedPullCannotResolveOmissionsOrReuseOlderCoverage(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	path := filepath.Join(w.Identity().Root, "main.go")
	if _, err := w.Snapshot(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch := DiagnosticBatch{
		Kind: EvidencePush, ProviderID: "producer#1", Producer: "producer",
		Document: path, DocumentRevision: "before", Selected: true, Complete: true,
		DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(1),
		Findings: []DiagnosticFinding{finding("kept"), finding("omitted")},
	}
	record := func() DiagnosticReport {
		t.Helper()
		report, err := w.RecordDiagnosticEvidence(batch)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	first := record()
	writeFile(t, path, "package main\n\n// edited\n")
	if _, err := w.Refresh(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}

	// A real full pull has an opaque result ID, but no producer version.
	// Its positive subset must not establish absence for the omitted finding.
	batch.Kind, batch.ResultID = EvidencePull, "advanced-before-analysis"
	batch.Complete, batch.DocumentVersion = false, nil
	batch.ExpectedVersion = diagnosticVersion(2)
	batch.PreEditDocumentRevision, batch.DocumentRevision = "before", "after"
	batch.TransactionID = "edit_pull"
	batch.Findings = []DiagnosticFinding{finding("kept"), finding("introduced")}
	positive := record()
	if positive.Confidence != ConfidenceProvisional || len(positive.New) != 1 ||
		positive.New[0].Finding.Message != "introduced" || len(positive.Resolved) != 0 ||
		positive.StaleCount != 1 || positive.Coverage["edited_documents"].State != "incomplete" {
		t.Fatalf("unproven positive pull promoted coverage or resolved an omission: %+v", positive)
	}
	repeated := record()
	if len(repeated.New) != 0 || len(repeated.Resolved) != 0 {
		t.Fatalf("repeated positive pull reannounced a finding: %+v", repeated)
	}
	batch.Findings, batch.ResultID = nil, "another-empty-id"
	empty := record()
	if empty.Confidence != ConfidenceProvisional || len(empty.Resolved) != 0 ||
		empty.CurrentCount != 2 || empty.StaleCount != 1 {
		t.Fatalf("empty pull resolved findings: %+v", empty)
	}
	page, err := w.DiagnosticNoticesSince(first.Cursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	introduced := 0
	for _, notice := range page.Notices {
		if notice.Kind == "resolved" {
			t.Fatalf("unproven pull emitted a resolution: %+v", page)
		}
		if notice.Kind == "new" && notice.ID == positive.New[0].ID {
			introduced++
		}
	}
	if introduced != 1 {
		t.Fatalf("introduced finding notices = %d, want one: %+v", introduced, page)
	}

	// The old expectation resolved all three IDs here. That inferred pull
	// withdrawal from a complete push, the captured Rust B failure. Only the
	// push-only "omitted" finding is covered; "kept" has both origins and
	// "introduced" has only pull evidence. Both must retain membership.
	batch.Kind, batch.Complete = EvidencePush, true
	batch.DocumentVersion = diagnosticVersion(2)
	complete := record()
	if complete.Confidence != ConfidenceProvisional || len(complete.Resolved) != 1 ||
		complete.Resolved[0].ID != first.New[1].ID || complete.Resolved[0].Finding.Message != "omitted" ||
		complete.CurrentCount != 2 || complete.StaleCount != 0 || len(complete.New) != 0 ||
		complete.Coverage["edited_documents"].State != "incomplete" {
		t.Fatalf("versioned empty push exceeded its stream or lost retained pull facts: %+v", complete)
	}
	resolvedPage, err := w.DiagnosticNoticesSince(page.Cursor, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolvedPage.Notices) != 1 || resolvedPage.Notices[0].Kind != "resolved" ||
		resolvedPage.Notices[0].ID != first.New[1].ID {
		t.Fatalf("push emitted cross-stream transitions: %+v", resolvedPage)
	}
	recovered, err := w.Diagnostics(complete.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Confidence != ConfidenceProvisional || recovered.CurrentCount != 2 ||
		len(recovered.Current) != 2 || len(recovered.New) != 0 || len(recovered.Resolved) != 0 ||
		recovered.Coverage["edited_documents"].State != "incomplete" {
		t.Fatalf("recovery lost independent pull membership or coverage: %+v", recovered)
	}
	expected := map[string][]DiagnosticEvidenceKind{}
	expected[first.New[0].ID] = []DiagnosticEvidenceKind{EvidencePush, EvidencePull}
	expected[positive.New[0].ID] = []DiagnosticEvidenceKind{EvidencePull}
	for _, item := range recovered.Current {
		kinds, ok := expected[item.ID]
		if !ok || item.Status != DiagnosticStatusCurrent || len(item.EvidenceKinds) != len(kinds) {
			t.Fatalf("retained identity/origin history changed: %+v", item)
		}
		for i, kind := range kinds {
			if item.EvidenceKinds[i] != kind {
				t.Fatalf("retained origin history changed: %+v", item)
			}
		}
		delete(expected, item.ID)
	}
	if len(expected) != 0 {
		t.Fatalf("retained pull identities missing: %v", expected)
	}
	// A later ordinary empty pull still supplies no withdrawal proof.
	batch.Kind, batch.DocumentVersion = EvidencePull, nil
	later := record()
	if later.Confidence != ConfidenceProvisional || later.CurrentCount != 2 ||
		len(later.New) != 0 || len(later.Resolved) != 0 {
		t.Fatalf("later empty pull withdrew retained origins: %+v", later)
	}
}
