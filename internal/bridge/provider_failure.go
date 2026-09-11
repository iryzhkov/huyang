package bridge

import (
	"errors"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// modernProviderFailure maps a kernel operation failure onto a tool result
// using the provider's stable error code rather than its message text. The
// fallback code names the operation that failed when the kernel gave none.
func modernProviderFailure(requestID string, workspace *workspacecore.Workspace, fallbackCode string, err error) map[string]any {
	switch provider.ErrorCode(err) {
	case "workspace_busy":
		return modernEnvelope(requestID, workspace, "conflict", "workspace_busy", err.Error(), map[string]any{})
	case "lsp_not_configured":
		result := modernEnvelope(requestID, workspace, "unavailable", "language_server_unavailable", err.Error(), map[string]any{
			"coverage": map[string]any{"complete": false, "unavailable": []string{"language_server"}},
		})
		result["next"] = []any{map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"}}
		return result
	case "provider_cancelled":
		return modernEnvelope(requestID, workspace, "failed", "request_cancelled", err.Error(), map[string]any{})
	}
	var failure *provider.Failure
	if errors.As(err, &failure) && failure.Code != "" {
		return modernFailure(requestID, workspace, string(failure.Code), err)
	}
	return modernFailure(requestID, workspace, fallbackCode, err)
}
