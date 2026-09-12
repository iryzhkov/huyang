package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editImplicitDocument serves an edit_apply that names a file but no
// workspace: a config file, a script, a note or a single source file outside
// any repository. It opens an exact one-document workspace for that path and
// runs the edit through it, so the same guards and diagnostics apply. Only
// replace_literal and create_file can name a file; replace_range needs a
// handle from a workspace that already exists.
func (h *Handlers) editImplicitDocument(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	operation, _ := arguments["operation"].(map[string]any)
	path, _ := operation["path"].(string)
	if strings.TrimSpace(path) == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_required",
			"workspace_id is required unless operation.path names the file (replace_literal or create_file)", map[string]any{})
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return mcpapi.Envelope(requestID, nil, "failed", "invalid_target", err.Error(), map[string]any{})
	}
	if operation["kind"] == "create_file" {
		if _, statErr := os.Stat(absolute); statErr == nil {
			return mcpapi.Envelope(requestID, nil, "conflict", "create_target_exists", absolute+" already exists; nothing changed", map[string]any{"path": absolute})
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			return mcpapi.Envelope(requestID, nil, "failed", "create_failed", err.Error(), map[string]any{})
		}
	}
	opened := h.open(ctx, requestID, map[string]any{"kind": "documents", "files": []any{absolute}})
	if opened["outcome"] != "ok" {
		return opened
	}
	identity, _ := opened["workspace"].(workspacecore.Identity)
	if strings.TrimSpace(string(identity.ID)) == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", "implicit document workspace did not return an ID", map[string]any{})
	}
	arguments["workspace_id"] = string(identity.ID)
	operation["path"] = absolute
	result := h.Execute(ctx, requestID, "edit_apply", arguments)
	result["warnings"] = append(result["warnings"].([]string), "Implicitly opened an exact one-document workspace for "+absolute+"; reuse the returned workspace_id for further edits of this file.")
	if data, ok := result["data"].(map[string]any); ok {
		data["implicit_workspace"] = true
	}
	return result
}
