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

	// Producer-stamped current proof can still resolve current and stale items.
	batch.Kind, batch.Complete = EvidencePush, true
	batch.DocumentVersion = diagnosticVersion(2)
	complete := record()
	if complete.Confidence != ConfidenceAuthoritative || len(complete.Resolved) != 3 {
		t.Fatalf("versioned empty push did not resolve findings: %+v", complete)
	}
}
