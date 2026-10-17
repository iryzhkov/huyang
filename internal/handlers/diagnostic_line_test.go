package handlers

import (
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"testing"
)

func testDiagnosticVersion(v int64) *int64 { return &v }

func TestDiagnosticLinePreservesOneBasedProviderCoordinates(t *testing.T) {
	w, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	item := workspacecore.DiagnosticItem{ID: "test", Document: "main.go", Finding: workspacecore.DiagnosticFinding{Range: workspacecore.DiagnosticRange{StartLine: 3}, Severity: 1, Message: "bad"}}
	report := workspacecore.DiagnosticReport{New: []workspacecore.DiagnosticItem{item}}
	got := compactDiagnosticDelta(w, report)["new"].([]map[string]any)[0]["line"]
	if got != 3 || diagnosticLine(item.Finding) != 3 {
		t.Fatalf("line=%v, want 3", got)
	}
}
