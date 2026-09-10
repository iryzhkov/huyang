package workspace

import (
	"path/filepath"
	"testing"
	"time"
)

func diagnosticVersion(value int64) *int64 { return &value }

func evidenceWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")
	state := t.TempDir()
	workspace, err := Open(OpenOptions{Kind: KindProject, Root: root, ProviderEpoch: 1, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	return workspace, state
}

func finding(message string) DiagnosticFinding {
	return DiagnosticFinding{Range: DiagnosticRange{StartLine: 1, StartCharacter: 2, EndLine: 1, EndCharacter: 5}, Severity: 1, Code: "E1", Source: "fake", Message: message}
}

func TestDiagnosticEvidenceNormalizesDeduplicatesAndRetainsProviderConflicts(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	version := int64(7)
	base := DiagnosticBatch{Kind: EvidencePush, ProviderID: "gopls#1", Producer: "gopls", ProducerVersion: "v1",
		Document: "main.go", DocumentRevision: "rev_a", DocumentVersion: &version, ExpectedVersion: &version,
		Complete: true, Selected: true, Findings: []DiagnosticFinding{finding(" undefined   name "), finding("undefined name")}}
	first, err := workspace.RecordDiagnosticEvidence(base)
	if err != nil {
		t.Fatal(err)
	}
	if first.Confidence != ConfidenceAuthoritative || len(first.New) != 1 {
		t.Fatalf("first report = %#v", first)
	}

	pull := base
	pull.Kind, pull.ResultID = EvidencePull, "pull-1"
	second, err := workspace.RecordDiagnosticEvidence(pull)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.New) != 0 || second.PreexistingCount != 1 {
		t.Fatalf("push/pull duplicate leaked: %#v", second)
	}

	conflict := base
	conflict.ProviderID, conflict.Producer = "staticcheck#1", "staticcheck"
	conflict.Findings = []DiagnosticFinding{finding("different finding")}
	third, err := workspace.RecordDiagnosticEvidence(conflict)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.New) != 1 || third.New[0].ProviderID != "staticcheck#1" {
		t.Fatalf("complementary finding collapsed: %#v", third)
	}
}

func TestDiagnosticEvidenceRejectsUnselectedProviders(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	_, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{
		Kind: EvidencePush, ProviderID: "undeclared#1", Producer: "undeclared",
		Document: "main.go", Complete: true, Selected: false,
	})
	if err == nil {
		t.Fatal("unselected provider evidence was accepted")
	}
}

func TestDiagnosticBarrierPermutationsNeverCallIncompleteEvidenceClean(t *testing.T) {
	cases := []struct {
		name  string
		batch DiagnosticBatch
		want  DiagnosticConfidence
	}{
		{"wrong push version", DiagnosticBatch{Kind: EvidencePush, DocumentVersion: diagnosticVersion(1), ExpectedVersion: diagnosticVersion(2), Complete: true}, ConfidenceProvisional},
		{"pull without result", DiagnosticBatch{Kind: EvidencePull, Complete: true}, ConfidenceProvisional},
		{"workspace incomplete", DiagnosticBatch{Kind: EvidenceWorkspace, ResultID: "w", Complete: false}, ConfidenceProvisional},
		{"progress veto", DiagnosticBatch{Kind: EvidencePull, ResultID: "p", Complete: true, ProgressPending: true}, ConfidenceProvisional},
		{"timeout", DiagnosticBatch{Kind: EvidencePull, ResultID: "p", Complete: true, TimedOut: true}, ConfidenceProvisional},
		{"project check distinct", DiagnosticBatch{Kind: EvidenceProjectCheck, Complete: true}, ConfidenceCorroborated},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			test.batch.ProviderID, test.batch.Producer, test.batch.Selected = "fake#1", "fake", true
			test.batch.Document = "main.go"
			workspace, _ := evidenceWorkspace(t)
			report, err := workspace.RecordDiagnosticEvidence(test.batch)
			if err != nil {
				t.Fatal(err)
			}
			if report.Confidence != test.want {
				t.Fatalf("confidence=%s want %s", report.Confidence, test.want)
			}
			if report.Confidence == ConfidenceAuthoritative {
				t.Fatal("incomplete evidence was called authoritative")
			}
		})
	}
}

func TestDiagnosticInboxPersistsAcknowledgementAndEvidence(t *testing.T) {
	workspace, state := evidenceWorkspace(t)
	id := workspace.Identity().ID
	version := int64(3)
	report, err := workspace.RecordDiagnosticEvidence(DiagnosticBatch{Kind: EvidencePush, ProviderID: "lua_ls#1", Producer: "lua_ls",
		Document: "main.lua", DocumentVersion: &version, ExpectedVersion: &version, Complete: true, Selected: true, Findings: []DiagnosticFinding{finding("bad")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.DiagnosticNotices(10)) != 1 {
		t.Fatal("late notice missing")
	}
	if _, err := workspace.Evidence(report.EvidenceIDs[0]); err != nil {
		t.Fatal(err)
	}

	root := workspace.Identity().Root
	reopened, err := Open(OpenOptions{Kind: KindProject, Root: root, ProviderEpoch: 1, StateDir: state, Identity: id, StateSeq: workspace.Identity().StateSeq})
	if err != nil {
		t.Fatal(err)
	}
	updates, err := reopened.Diagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates.Notices) != 1 {
		t.Fatalf("persisted notices=%#v", updates.Notices)
	}
	if _, err := reopened.Diagnostics(updates.Cursor); err != nil {
		t.Fatal(err)
	}
	if got := reopened.DiagnosticNotices(10); len(got) != 0 {
		t.Fatalf("acknowledged notices remain: %#v", got)
	}
}

func TestCulpritAttributionUsesEvidenceNotTiming(t *testing.T) {
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", ExactSandbox: true}}); got.Rank != "exact" {
		t.Fatalf("exact=%#v", got)
	}
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", PostimageVersionMatch: true}, {TransactionID: "tx_b", PostimageVersionMatch: true}}); got.Rank != "ambiguous" {
		t.Fatalf("ambiguous=%#v", got)
	}
	if got := rankCulprit([]CulpritCandidate{{TransactionID: "tx_a", ChangedSymbol: true}}); got.Rank != "likely" {
		t.Fatalf("likely=%#v", got)
	}
	if got := rankCulprit(nil); got.Rank != "unattributed" {
		t.Fatalf("none=%#v", got)
	}
}

func TestDiagnosticResolutionCreatesDurableNotice(t *testing.T) {
	workspace, _ := evidenceWorkspace(t)
	now := time.Now().UTC()
	batch := DiagnosticBatch{Kind: EvidencePull, ProviderID: "pyright#1", Producer: "pyright", Document: "main.py", ResultID: "r1", Complete: true, Selected: true, ObservedAt: now, Findings: []DiagnosticFinding{finding("bad")}}
	if _, err := workspace.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	batch.ResultID = "r2"
	batch.Findings = nil
	batch.ObservedAt = now.Add(time.Second)
	report, err := workspace.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Resolved) != 1 {
		t.Fatalf("resolved=%#v", report.Resolved)
	}
}
