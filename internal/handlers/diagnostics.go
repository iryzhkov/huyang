package handlers

import (
	"fmt"
	"sort"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// diagnostics answers the diagnostics tool from the workspace's durable
// inbox; the outcome follows the confidence of the evidence.
func diagnostics(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	since, _ := arguments["since"].(string)
	report, err := workspace.Diagnostics(since)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "diagnostic_cursor_invalid", err)
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
