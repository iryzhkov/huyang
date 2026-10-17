package workspace

import (
	"testing"
)

func TestPythonEnvironmentReasonCannotClaimCleanOrResolve(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	base := DiagnosticBatch{Kind: EvidencePush, ProviderID: "pyright#1", Producer: "pyright",
		Document: "main.py", DocumentRevision: "before", DocumentVersion: diagnosticVersion(3), ExpectedVersion: diagnosticVersion(3),
		Complete: true, Selected: true, Findings: []DiagnosticFinding{finding("real type error")}}
	if _, err := workspace.RecordDiagnosticEvidence(base); err != nil {
		t.Fatal(err)
	}
	grouped := base
	grouped.DocumentRevision = "after"
	grouped.Reason = "python_environment_unresolved"
	grouped.Complete = false
	grouped.Findings = []DiagnosticFinding{{Range: DiagnosticRange{1, 1, 1, 1}, Severity: 2,
		Code: "python_environment_unresolved", Source: "huyang", Message: "Python environment not resolved"}}
	first, err := workspace.RecordDiagnosticEvidence(grouped)
	if err != nil {
		t.Fatal(err)
	}
	if first.Confidence != ConfidenceProvisional || len(first.Resolved) != 0 || len(first.New) != 1 {
		t.Fatalf("grouped report = %#v", first)
	}
	repeated, err := workspace.RecordDiagnosticEvidence(grouped)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeated.New) != 0 || len(repeated.Resolved) != 0 {
		t.Fatalf("reannounced note = %#v", repeated)
	}
	for _, kind := range []DiagnosticEvidenceKind{EvidencePush, EvidencePull} {
		for _, progress := range []bool{false, true} {
			grouped.Kind, grouped.ProgressPending, grouped.Complete = kind, progress, true
			confidence, reasons := confidenceFor(grouped)
			found := false
			for _, reason := range reasons {
				found = found || reason == "python_environment_unresolved"
			}
			if confidence != ConfidenceProvisional || !found {
				t.Fatalf("false clean proof: %s %v", confidence, reasons)
			}
		}
	}
}
