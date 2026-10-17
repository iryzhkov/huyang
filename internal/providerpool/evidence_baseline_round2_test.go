package providerpool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestBaselineReportsCrossFileRecurrenceBeforeNextMutation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	v := int64(1)
	pathA := filepath.Join(root, "a.go")
	x := workspacecore.DiagnosticFinding{Message: "X returned after editing B", Severity: 1}
	e := workspacecore.DiagnosticFinding{Message: "E first observed after editing B", Severity: 1}
	batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush, ProviderID: "test#1",
		Producer: "test", Document: pathA, Complete: true, Selected: true,
		DocumentVersion: &v, ExpectedVersion: &v, Findings: []workspacecore.DiagnosticFinding{x},
		TransactionID: "old_culprit", Candidates: []workspacecore.CulpritCandidate{{TransactionID: "old_culprit", ExactSandbox: true}}}
	if _, err := w.RecordDiagnosticEvidence(batch); err != nil {
		t.Fatal(err)
	}
	batch.Findings = nil
	fixed, err := w.RecordDiagnosticEvidence(batch)
	if err != nil || len(fixed.Resolved) != 1 {
		t.Fatalf("initial resolution: %+v, %v", fixed, err)
	}
	// B changes; A's bytes do not. Its provider findings change across files,
	// but are first collected by the baseline before the next edit to C.
	pathB := filepath.Join(root, "b.go")
	if err := os.WriteFile(pathB, []byte("package fixture\n// B now causes X and E in A\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Refresh(pathB, workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	batch.Findings = []workspacecore.DiagnosticFinding{x, e}
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}},
		batches: []workspacecore.DiagnosticBatch{batch}}
	if err := RecordDiagnosticBaseline(ctx, w, p); err != nil {
		t.Fatal(err)
	}
	page, err := w.DiagnosticNoticesSince(fixed.Cursor, 10)
	if err != nil || len(page.Notices) != 2 || page.Notices[0].Kind != "new" || page.Notices[1].Kind != "persisting" {
		t.Fatalf("cross-file changes hidden by baseline: %+v, %v", page, err)
	}
	for _, notice := range page.Notices {
		body := w.DiagnosticFindings([]string{notice.ID})[notice.ID]
		if body.Message == "" || len(notice.Attribution.Candidates) != 0 {
			t.Fatalf("notice missing body or retained old blame: %+v %+v", notice, body)
		}
	}
	current, err := w.Diagnostics(fixed.Cursor)
	if err != nil || len(current.Current) != 2 {
		t.Fatalf("current baseline findings: %+v, %v", current, err)
	}
	for _, item := range current.Current {
		if item.TransactionID != "" || item.BaselineMissing || len(item.Attribution.Candidates) != 0 {
			t.Fatalf("baseline finding falsely attributed: %+v", item)
		}
	}
	// An edit to C must not count either baseline finding as introduced.
	pathC := filepath.Join(root, "c.go")
	before, err := w.Snapshot(pathC, workspacecore.ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	after := []byte("package fixture\n// unrelated edit to C\n")
	if err := os.WriteFile(pathC, after, 0600); err != nil {
		t.Fatal(err)
	}
	cBatch := batch
	cBatch.Document, cBatch.Findings, cBatch.Candidates = pathC, nil, nil
	p.batches = []workspacecore.DiagnosticBatch{batch, cBatch}
	report, err := RecordDiagnostics(ctx, w, p, []workspacecore.PlanStageFile{{
		Path: "c.go", Before: []byte("package fixture\n"), BeforeExists: true, BeforeDisk: before.Disk,
		After: after, AfterExists: true,
	}}, "revision_after_C", "edit_C")
	if err != nil || len(report.New) != 0 || report.PreexistingCount != 2 {
		t.Fatalf("baseline findings blamed on C: %+v, %v", report, err)
	}
	repeated, err := w.DiagnosticNoticesSince(page.Cursor, 10)
	if err != nil || len(repeated.Notices) != 0 {
		t.Fatalf("subsequent observation repeated baseline notices: %+v, %v", repeated, err)
	}
}

func TestBaselineLoadedBuffersFilterBeforeBulkIngestion(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	w, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	v := int64(1)
	p := &batchProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "test", Root: root, Epoch: 1}}}
	for i := 0; i < 20; i++ {
		path := filepath.Join(root, fmt.Sprintf("loaded_%d.go", i))
		if err := os.WriteFile(path, []byte("package fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
		p.batches = append(p.batches, workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePush,
			ProviderID: "test#1", Producer: "test", Document: path, Complete: true,
			DocumentVersion: &v, ExpectedVersion: &v,
			Findings: []workspacecore.DiagnosticFinding{{Severity: 1, Message: "preexisting"}}})
	}
	invalid := p.batches[0]
	invalid.ProviderID, invalid.Producer, invalid.ProgressPending = "", "", true
	missing := p.batches[0]
	missing.Document = filepath.Join(root, "missing", "cannot_snapshot.go")
	// A missing parent produces no native proof, so it must be filtered.
	p.batches = append(p.batches, invalid, missing)
	if err := RecordDiagnosticBaseline(context.Background(), w, p); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(stateDir, "diagnostics", string(w.Identity().ID)+".json")
	readLedger := func() map[string]json.RawMessage {
		t.Helper()
		data, err := os.ReadFile(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		var ledger map[string]json.RawMessage
		if err := json.Unmarshal(data, &ledger); err != nil {
			t.Fatal(err)
		}
		return ledger
	}
	initial := readLedger()
	// Exercise the RPC integration repeatedly without timing or benchmark claims.
	for i := 0; i < 30; i++ {
		if err := RecordDiagnosticBaseline(context.Background(), w, p); err != nil {
			t.Fatal(err)
		}
	}
	final := readLedger()
	var evidence map[string]json.RawMessage
	if err := json.Unmarshal(final["evidence"], &evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 20 || len(final["evidence"]) != len(initial["evidence"]) {
		t.Fatalf("loaded buffer repetitions grew evidence: count=%d bytes=%d->%d", len(evidence), len(initial["evidence"]), len(final["evidence"]))
	}
	report, err := w.Diagnostics("")
	if err != nil || len(report.New) != 0 || report.CurrentCount != 20 {
		t.Fatalf("filtered bulk state: %+v, %v", report, err)
	}
}
