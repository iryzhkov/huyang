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
// any repository. It opens an exact documents workspace over the named
// paths and runs the edit through it, so the same guards and diagnostics
// apply. replace_literal, create_file and delete_file name a path; move_file
// and copy_file name from and to; replace_range needs a handle from a
// workspace that already exists.
func (h *Handlers) editImplicitDocument(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	operation, _ := arguments["operation"].(map[string]any)
	kind, _ := operation["kind"].(string)
	fields := []string{"path"}
	switch kind {
	case "move_file":
		fields = []string{"from", "to"}
	case "copy_file":
		// The source may live anywhere; TransferSource reads it as an
		// external file when it is not a document of the workspace.
		fields = []string{"to"}
	}
	files := make([]any, 0, len(fields))
	for _, field := range fields {
		path, _ := operation[field].(string)
		if strings.TrimSpace(path) == "" {
			return mcpapi.Envelope(requestID, nil, "failed", "workspace_required",
				"workspace_id is required unless the operation names its file by absolute path (path, or from and to)", map[string]any{})
		}
		if !filepath.IsAbs(path) {
			// The service's working directory is not the agent's, so a relative
			// path without a workspace names nothing useful.
			return mcpapi.Envelope(requestID, nil, "failed", "invalid_target", "without workspace_id or root the path must be absolute; pass root for a repository file", map[string]any{"path": path})
		}
		absolute := filepath.Clean(path)
		if field != "from" && (kind == "create_file" && operation["replace"] != true || kind == "move_file" || kind == "copy_file") {
			if _, statErr := os.Stat(absolute); statErr == nil {
				return mcpapi.Envelope(requestID, nil, "conflict", workspacecore.CodeCreateTargetExists, absolute+" already exists; nothing changed", map[string]any{"path": absolute})
			}
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				return mcpapi.Envelope(requestID, nil, "failed", "create_failed", err.Error(), map[string]any{})
			}
		}
		operation[field] = absolute
		files = append(files, absolute)
	}
	opened := h.open(ctx, requestID, map[string]any{"kind": "documents", "files": files})
	if opened["outcome"] != "ok" {
		return opened
	}
	identity, _ := opened["workspace"].(workspacecore.Identity)
	if strings.TrimSpace(string(identity.ID)) == "" {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_open_failed", "implicit document workspace did not return an ID", map[string]any{})
	}
	arguments["workspace_id"] = string(identity.ID)
	result := h.Execute(ctx, requestID, "edit_apply", arguments)
	if data, ok := result["data"].(map[string]any); ok {
		data["implicit_workspace"] = true
	}
	return result
}
