package handlers

import (
	"errors"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// modernProviderFailure maps a kernel operation failure onto a tool result
// using the provider's stable error code rather than its message text. The
// fallback code names the operation that failed when the kernel gave none.
func modernProviderFailure(requestID string, workspace *workspacecore.Workspace, fallbackCode string, err error) map[string]any {
	switch provider.ErrorCode(err) {
	case "workspace_busy":
		return mcpapi.Envelope(requestID, workspace, "conflict", "workspace_busy", err.Error(), map[string]any{})
	case "lsp_not_configured":
		result := mcpapi.Envelope(requestID, workspace, "unavailable", "language_server_unavailable", err.Error(), map[string]any{
			"coverage": map[string]any{"complete": false, "unavailable": []string{"language_server"}},
		})
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"}}
		return result
	case "provider_cancelled":
		return mcpapi.Envelope(requestID, workspace, "failed", "request_cancelled", err.Error(), map[string]any{})
	case "lsp_starting", "lsp_attach_deadline_exceeded":
		return languageServerNotReady(requestID, workspace, fallbackCode, err)
	}
	var failure *provider.Failure
	if errors.As(err, &failure) && failure.Code != "" {
		return mcpapi.Failure(requestID, workspace, string(failure.Code), err)
	}
	return mcpapi.Failure(requestID, workspace, fallbackCode, err)
}

// languageServerNotReady answers a call that reached a language server
// before it attached: one still starting, or one that did not attach within
// the wait. Nothing is wrong with the call and nothing is broken yet, so it
// is unavailable rather than failed and retryable. The code stays the one the
// operation names, so callers that fall back on it still do; the provider's
// reason is in data. Retrying is advice in the summary rather than a next
// entry, because every next entry names a tool to call.
func languageServerNotReady(requestID string, workspace *workspacecore.Workspace, code string, err error) map[string]any {
	// A server still starting is worth waiting for; one that never began
	// attaching is better looked at first, because the status says whether
	// it is unconfigured, missing or failed and what would fix it.
	advice := "the language server did not attach in time; language_server_status says why, then retry the same call"
	if provider.ErrorCode(err) == "lsp_starting" {
		advice = "the language server is still starting; retry the same call shortly"
	}
	result := mcpapi.Envelope(requestID, workspace, "unavailable", code, err.Error()+"; "+advice, map[string]any{
		"reason":   provider.ErrorCode(err),
		"coverage": map[string]any{"complete": false, "unavailable": []string{"language_server"}},
	})
	result["retryable"] = true
	result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment"}}
	return result
}
