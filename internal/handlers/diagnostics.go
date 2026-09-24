package handlers

import (
	"fmt"
	"slices"
	"sort"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// noDiagnosticEvidence is the reason the ledger gives when nothing has ever
// been recorded in it.
const noDiagnosticEvidence = "no_diagnostic_evidence"

// diagnostics answers the diagnostics tool from the workspace's durable
// inbox; the outcome follows the confidence of the evidence.
//
// An inbox nothing was ever recorded in is not an outage: no edit or
// verification has asked a language server about this workspace yet, and a
// running provider's late findings were already collected before this was
// called. The answer is then ok with no findings and coverage that says it
// is incomplete and why, rather than unavailable, which reads as a failure
// to retry.
func diagnostics(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	since, _ := arguments["since"].(string)
	report, err := workspace.Diagnostics(since)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "diagnostic_cursor_invalid", err)
	}
	if slices.Contains(report.ProvisionalReasons, noDiagnosticEvidence) {
		return emptyDiagnostics(requestID, workspace, report, arguments)
	}
	outcome := "ok"
	summary := "Diagnostic evidence retrieved from the durable workspace inbox"
	if report.Confidence == workspacecore.ConfidenceProvisional {
		outcome = "provisional"
		summary = "Only provisional diagnostic evidence is available"
	} else if report.Confidence == workspacecore.ConfidenceUnavailable {
		outcome = "unavailable"
		summary = "Diagnostic evidence is unavailable for this workspace"
	}
	full, _ := arguments["full"].(bool)
	result := mcpapi.Envelope(requestID, workspace, outcome, "", summary, mcpapi.CompactDiagnosticReport(report, outcome, full))
	ids := append([]string(nil), report.EvidenceIDs...)
	sort.Strings(ids)
	result["evidence"] = map[string]any{"ids": mcpapi.NonNilStrings(mcpapi.UniqueStrings(ids)), "truncated": false}
	if outcome == "unavailable" {
		result["next"] = []any{
			map[string]any{"tool": "workspace_inspect", "action": "inspect_provider_and_pipeline_status", "view": "status"},
			map[string]any{"tool": "verify_run", "action": "run_configured_diagnostics_for_exact_revision"},
		}
	}
	return result
}

// emptyDiagnostics answers for a workspace whose ledger has never recorded
// anything: no findings, and coverage that is explicitly incomplete.
func emptyDiagnostics(requestID string, workspace *workspacecore.Workspace, report workspacecore.DiagnosticReport, arguments map[string]any) map[string]any {
	full, _ := arguments["full"].(bool)
	data := mcpapi.CompactDiagnosticReport(report, "ok", full)
	data["coverage"] = map[string]any{"complete": false, "reason": noDiagnosticEvidence}
	result := mcpapi.Envelope(requestID, workspace, "ok", "",
		"0 diagnostics: no language server has reported on this workspace yet, so coverage is incomplete", data)
	result["warnings"] = []string{"No diagnostic evidence has been recorded for this workspace; an absence of findings here is not a clean bill of health."}
	result["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
	result["next"] = []any{
		map[string]any{"tool": "verify_run", "action": "run_configured_diagnostics_for_exact_revision", "stages": []string{"diagnostics"}, "revision_or_transaction": "current"},
		map[string]any{"tool": "workspace_inspect", "action": "inspect_provider_and_pipeline_status", "view": "status"},
	}
	return result
}

// evidenceGet returns one evidence record by ID.
func evidenceGet(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	evidence, err := workspace.Evidence(fmt.Sprint(arguments["evidence_id"]))
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "evidence_not_found", err)
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "", "Detailed diagnostic evidence retrieved", map[string]any{"evidence": evidence})
	result["evidence"] = map[string]any{"ids": []string{evidence.ID}, "truncated": false}
	return result
}
