package workspace

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestDiagnosticPublicationVersionProof(t *testing.T) {
	for _, kind := range []DiagnosticEvidenceKind{EvidencePush, EvidencePull, EvidenceWorkspace} {
		for _, version := range []int64{-1, 7, 8, 9} {
			t.Run(fmt.Sprintf("%s/version_%d", kind, version), func(t *testing.T) {
				expected := int64(8)
				batch := DiagnosticBatch{Kind: kind, Selected: true, Complete: true, ChangeBarrier: true, ResultID: "r", ExpectedVersion: &expected}
				if version >= 0 {
					batch.DocumentVersion = &version
				}
				got, _ := confidenceFor(batch)
				if wantCurrent := version >= expected; (got == ConfidenceAuthoritative) != wantCurrent {
					t.Fatalf("version %d expected %d: confidence %s", version, expected, got)
				}
			})
		}
	}
}

func TestLegacyProducerCoverageRetainsUnknownStreamProof(t *testing.T) {
	w, state := evidenceWorkspace(t)
	v := int64(2)
	batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go", Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v}
	report, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	w.diagnostics.state.ProviderDimensions = map[string]DiagnosticDimension{"edited_documents\x00test#1": report.Coverage["edited_documents"]}
	if err := w.diagnostics.save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newDiagnosticStore(state, w.Identity().ID)
	if err != nil {
		t.Fatal(err)
	}
	// The old key cannot identify all historical streams. Bounded retained
	// payloads are not migration proof, even when the latest one is a push.
	if got := reopened.state.ProviderDimensions["edited_documents\x00test#1"].Confidence; got != ConfidenceProvisional {
		t.Fatalf("legacy confidence=%s", got)
	}
}

func TestOldCleanPublicationCannotResolveAndLateCurrentCleanDoes(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	path := filepath.Join(w.Identity().Root, "main.go")
	w.Snapshot(path, ProviderLayer{})
	old, expected := int64(7), int64(8)
	batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "jdtls#1", Producer: "jdtls",
		Document: path, DocumentVersion: &old, ExpectedVersion: &old, Complete: true, Selected: true,
		Findings: []DiagnosticFinding{finding("existing")}}
	first, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "package main\n// unrelated\n")
	if _, err := w.Refresh(path, ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch.ExpectedVersion, batch.Findings, batch.ChangeBarrier = &expected, nil, true
	stale, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if stale.Confidence != ConfidenceProvisional || len(stale.Resolved) != 0 || stale.StaleCount != 1 {
		t.Fatalf("old clean publication resolved finding: %+v", stale)
	}
	batch.DocumentVersion = &expected
	clean, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Resolved) != 1 || clean.Resolved[0].ID != first.New[0].ID || clean.StaleCount != 0 {
		t.Fatalf("late current clean did not resolve stale: %+v", clean)
	}
}
