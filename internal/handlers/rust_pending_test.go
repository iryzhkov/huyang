package handlers

import (
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
	"strings"
	"testing"
)

func TestRustStartingAndIncompleteSummariesExplicitlyPending(t *testing.T) {
	for _, reason := range []string{"lsp_starting", "lsp_attach_deadline_exceeded", "work_done_progress_pending", "push_missing_current_document_proof"} {
		report := workspacecore.DiagnosticReport{Confidence: workspacecore.ConfidenceProvisional, ProvisionalReasons: []string{reason}}
		got := provisionalSummary(report, []workspacecore.PlanStageFile{{Path: "src/main.rs", AfterExists: true}})
		if !strings.Contains(got, "Rust diagnostics pending") || !strings.Contains(got, "diagnostic_updates") || !strings.Contains(got, reason) {
			t.Fatalf("pending reason %s hidden: %s", reason, got)
		}
	}
}
