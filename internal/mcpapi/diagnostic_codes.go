package mcpapi

import "sort"

// DiagnosticReasonCodes admits fixed evidence-model identifiers only. A
// provider reason can be arbitrary error text, including a code-shaped secret.
// Unknown reasons remain visible as unclassified without exporting that text.
func DiagnosticReasonCodes(reasons []string) []string {
	known := map[string]bool{
		"diagnostic_barrier_timed_out": true, "provider_not_selected": true,
		"work_done_progress_pending": true, "push_missing_current_document_proof": true,
		"pull_incomplete_or_missing_result_id": true, "workspace_diagnostics_incomplete": true,
		"project_check_unavailable": true, "unsupported_diagnostic_evidence": true,
		"lsp_not_configured": true, "lsp_not_startable": true, "lsp_starting": true,
		"lsp_attach_deadline_exceeded": true, "no_diagnostic_evidence": true,
		"diagnostic_reason_unclassified": true,
	}
	unique := map[string]bool{}
	for _, reason := range reasons {
		if !known[reason] {
			reason = "diagnostic_reason_unclassified"
		}
		unique[reason] = true
	}
	codes := make([]string, 0, len(unique))
	for code := range unique {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	if len(codes) > 16 {
		codes = codes[:16]
	}
	return codes
}
