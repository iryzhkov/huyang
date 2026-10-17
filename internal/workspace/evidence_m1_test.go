package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticStateUpgradePersistsVersionAndRejectsDowngrade(t *testing.T) {
	w, state := evidenceWorkspace(t)
	path := filepath.Join(w.Identity().Root, "main.go")
	v := int64(1)
	batch := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: path, Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v, Findings: []DiagnosticFinding{finding("existing")}}
	report, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	// Model a version-1 ledger with the old producer-wide coverage key.
	w.diagnostics.state.Version = 1
	w.diagnostics.state.ProviderDimensions = map[string]DiagnosticDimension{"edited_documents\x00test#1": report.Coverage["edited_documents"]}
	w.diagnostics.state.Notices = append(w.diagnostics.state.Notices, DiagnosticNotice{Cursor: "diagcur_99", Kind: "persisting", ID: report.New[0].ID})
	if err := w.diagnostics.save(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := newDiagnosticStore(state, w.Identity().ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := upgraded.state.ProviderDimensions["edited_documents\x00test#1"].Confidence; got != ConfidenceProvisional {
		t.Fatalf("unsplittable legacy coverage claimed proof: %s", got)
	}
	bytes, err := os.ReadFile(upgraded.path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted diagnosticState
	if err := json.Unmarshal(bytes, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Version != diagnosticStateVersion || !strings.Contains(string(bytes), "persisting") {
		t.Fatal("upgrade not persisted")
	}
	// The old loader's exact guard is Version != 1; the persisted header must
	// fail it before it can interpret either new notice kinds or dimensions.
	if persisted.Version == 1 {
		t.Fatal("version-1 binary would accept migrated state")
	}
	upgraded.state.Version = diagnosticStateVersion + 1
	if err := upgraded.save(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(upgraded.path)
	if _, err := newDiagnosticStore(state, w.Identity().ID); err == nil || !strings.Contains(err.Error(), "unsupported diagnostic state version") {
		t.Fatalf("future state accepted: %v", err)
	}
	after, _ := os.ReadFile(upgraded.path)
	if string(before) != string(after) {
		t.Fatal("unsupported state was overwritten")
	}
}

func TestOldAuthoritativeDimensionDoesNotProvePreEditBaseline(t *testing.T) {
	for _, preRevision := range []string{"", "external_revision", "known_revision"} {
		t.Run("pre_"+preRevision, func(t *testing.T) {
			w, _ := evidenceWorkspace(t)
			v := int64(1)
			b := DiagnosticBatch{Kind: EvidencePush, ProviderID: "test#1", Producer: "test", Document: "main.go", DocumentRevision: "known_revision", Complete: true, Selected: true, DocumentVersion: &v, ExpectedVersion: &v}
			if _, err := w.RecordDiagnosticEvidence(b); err != nil {
				t.Fatal(err)
			}
			b.DocumentRevision = "after"
			b.PreEditDocumentRevision = preRevision
			b.Findings = []DiagnosticFinding{finding("error")}
			report, err := w.RecordDiagnosticEvidence(b)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.New) != 1 || report.New[0].BaselineMissing != (preRevision != "known_revision") {
				t.Fatalf("invalid baseline: %+v", report)
			}
		})
	}
}

func TestLiteralStageRetainsExactBaselineRevision(t *testing.T) {
	w, _ := evidenceWorkspace(t)
	before, err := w.Snapshot("main.go", ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	match, err := w.LocateLiteral("main.go", "package main")
	if err != nil {
		t.Fatal(err)
	}
	change, err := w.ReplaceLiteral(w.Identity().ID, match.Hits, []byte("package other"), false)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := w.DiagnosticPreEditRevision(change.Files[0])
	if err != nil {
		t.Fatal(err)
	}
	if revision != string(before.Revision) {
		t.Fatalf("native stage lost pre-edit identity: %s want %s", revision, before.Revision)
	}
}

func TestRustIncompleteEvidenceExplicitlyPending(t *testing.T) {
	v := int64(1)
	b := DiagnosticBatch{Kind: EvidencePush, ProviderID: "rust_analyzer#1", Producer: "rust_analyzer", Selected: true, DocumentVersion: &v, ExpectedVersion: &v}
	confidence, reasons := confidenceFor(b)
	if confidence != ConfidenceProvisional || len(reasons) < 1 || reasons[0] != "rust_diagnostics_pending" {
		t.Fatalf("no pending verdict: %s %v", confidence, reasons)
	}
	b.Complete = true
	if c, r := confidenceFor(b); c != ConfidenceAuthoritative || len(r) != 0 {
		t.Fatalf("complete Rust evidence remained pending: %s %v", c, r)
	}
}
