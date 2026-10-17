package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestEditDiagnosticDeltaAndSummaryFollowLedgerTransitions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.py")
	if err := os.WriteFile(path, []byte("# before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := workspacecore.New(workspacecore.KindProject, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Snapshot(path, workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	finding := workspacecore.DiagnosticFinding{Severity: 2, Message: "existing warning"}
	batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePull, DocumentVersion: testDiagnosticVersion(1), ExpectedVersion: testDiagnosticVersion(1), ProviderID: "pyright#1", Producer: "pyright",
		Document: path, DocumentRevision: "before", PreEditDocumentRevision: "before", ResultID: "before", Complete: true, Selected: true,
		Findings: []workspacecore.DiagnosticFinding{finding}}
	record := func() workspacecore.DiagnosticReport {
		t.Helper()
		report, err := w.RecordDiagnosticEvidence(batch)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	first := record()
	if err := os.WriteFile(path, []byte("# after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Refresh(path, workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	check := func(report workspacecore.DiagnosticReport, count int, summary string) {
		t.Helper()
		delta := compactDiagnosticDelta(w, report)
		if got := len(delta["new"].([]map[string]any)); got != count {
			t.Fatalf("delta new count = %d, want %d: %+v", got, count, delta)
		}
		if got := diagnosticSummary(report); got != summary {
			t.Fatalf("summary = %q, want %q", got, summary)
		}
	}
	batch.ResultID = "after"
	batch.DocumentRevision = "after"
	batch.PreEditDocumentRevision = "after"
	check(record(), 0, "no new diagnostics")
	batch.Findings = append(batch.Findings, workspacecore.DiagnosticFinding{Severity: 1, Message: "introduced error"})
	check(record(), 1, "1 new diagnostic(s), 0 resolved")
	batch.Findings = nil
	check(record(), 0, "2 diagnostic(s) resolved, none new")
	batch.Findings = []workspacecore.DiagnosticFinding{finding}
	regression := record()
	check(regression, 1, "1 new diagnostic(s), 0 resolved")
	if regression.New[0].ID != first.New[0].ID {
		t.Fatal("regression did not restore the original finding ID")
	}
}

func TestEditDiagnosticSeverityFilterKeepsInformationAndHintsInDiagnostics(t *testing.T) {
	w, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	batch := workspacecore.DiagnosticBatch{Kind: workspacecore.EvidencePull, DocumentVersion: testDiagnosticVersion(1), ExpectedVersion: testDiagnosticVersion(1), ProviderID: "bashls#1", Producer: "bashls",
		Document: "main.sh", ResultID: "r1", Complete: true, Selected: true,
		Findings: []workspacecore.DiagnosticFinding{
			{Severity: 1, Message: "error"}, {Severity: 2, Message: "warning"},
			{Severity: 3, Code: "SC2329", Message: "function never invoked"},
			{Severity: 4, Message: "hint"},
		}}
	report, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	delta := compactDiagnosticDelta(w, report)
	items := delta["new"].([]map[string]any)
	if len(items) != 2 || items[0]["message"] != "error" || items[1]["message"] != "warning" ||
		diagnosticSummary(report) != "2 new or pre-existing diagnostic(s) (no baseline), 0 new, 0 resolved" {
		t.Fatalf("severity leaked into edit reply: %+v; %s", delta, diagnosticSummary(report))
	}
	all, err := w.Diagnostics("")
	if err != nil {
		t.Fatal(err)
	}
	tool := mcpapi.CompactDiagnosticReport(all, "ok", false)["diagnostics"].(map[string]any)
	if len(tool["new"].([]map[string]any)) != 4 {
		t.Fatalf("diagnostics tool lost lower severity findings: %+v", tool)
	}

	// A reply consisting only of information and hints must say none new.
	report.New = report.New[2:]
	if len(compactDiagnosticDelta(w, report)["new"].([]map[string]any)) != 0 ||
		diagnosticSummary(report) != "no new diagnostics" {
		t.Fatal("information/hint-only reply announced new diagnostics")
	}
}

func TestDiagnosticUpdatesKeepPersistingDistinctFromNew(t *testing.T) {
	notices := []workspacecore.DiagnosticNotice{
		{Cursor: "diagcur_1", Kind: "new", ID: "existing", Severity: 2},
		{Cursor: "diagcur_2", Kind: "stale", ID: "existing", Severity: 2},
		{Cursor: "diagcur_3", Kind: "persisting", ID: "existing", Severity: 2},
		{Cursor: "diagcur_4", Kind: "new", ID: "introduced", Severity: 1},
		{Cursor: "diagcur_5", Kind: "new", ID: "info", Severity: 3},
		{Cursor: "diagcur_6", Kind: "new", ID: "hint", Severity: 4},
	}
	updates, truncated := collapseNotices("", notices)
	if truncated || len(updates) != 4 || updates[3].Kind != "persisting" || countNewNotices(updates) != 1 {
		t.Fatalf("updates misclassified persisting or lower severity findings: %+v", updates)
	}
}
