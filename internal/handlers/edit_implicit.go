package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editImplicitDocument serves an edit_apply that names files but no
// workspace: a config file, a script, a note or a single source file outside
// any repository. It opens an exact documents workspace over every path the
// call names and runs the edit through it, so the same guards and
// diagnostics apply. One operation or a list of them are treated alike;
// replace_range is the exception, because its handle comes from a workspace
// that already exists.
func (h *Handlers) editImplicitDocument(ctx context.Context, requestID string, arguments map[string]any) map[string]any {
	operations := implicitOperations(arguments)
	if len(operations) == 0 {
		return mcpapi.Envelope(requestID, nil, "failed", "workspace_required",
			"workspace_id is required unless every operation names its file by absolute path (path, or from and to)", map[string]any{})
	}
	files := make([]any, 0, len(operations))
	seen := map[string]bool{}
	for _, operation := range operations {
		named, failure := prepareImplicitOperation(requestID, operation)
		if failure != nil {
			return failure
		}
		for _, path := range named {
			if !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
		}
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

// implicitOperations returns the operations of the call, whether it carries
// one or a list. The maps are the caller's own, so rewriting a path to its
// absolute form below reaches the edit that follows.
func implicitOperations(arguments map[string]any) []map[string]any {
	if listed := mcpapi.AnySlice(arguments["operations"]); len(listed) > 0 {
		operations := make([]map[string]any, 0, len(listed))
		for _, raw := range listed {
			if operation, ok := raw.(map[string]any); ok {
				operations = append(operations, operation)
			}
		}
		return operations
	}
	if operation, ok := arguments["operation"].(map[string]any); ok {
		return []map[string]any{operation}
	}
	return nil
}

// prepareImplicitOperation resolves the paths one operation names, creates
// the parent directory of a path it is about to write, and returns the
// documents the workspace must cover. The failure envelope is non-nil when
// nothing should be opened.
func prepareImplicitOperation(requestID string, operation map[string]any) ([]string, map[string]any) {
	kind, _ := operation["kind"].(string)
	fields := []string{"path"}
	switch kind {
	case "move_file":
		fields = []string{"from", "to"}
	case "copy_file":
		// The source may live anywhere; TransferSource reads it as an
		// external file when it is not a document of the workspace.
		fields = []string{"to"}
	case "replace_range":
		return nil, mcpapi.Envelope(requestID, nil, "failed", "workspace_required",
			"replace_range needs the workspace its handle came from", map[string]any{})
	}
	named := make([]string, 0, len(fields))
	for _, field := range fields {
		path, _ := operation[field].(string)
		if strings.TrimSpace(path) == "" {
			return nil, mcpapi.Envelope(requestID, nil, "failed", "workspace_required",
				"workspace_id is required unless every operation names its file by absolute path (path, or from and to)", map[string]any{})
		}
		if !filepath.IsAbs(path) {
			// The service's working directory is not the agent's, so a relative
			// path without a workspace names nothing useful.
			return nil, mcpapi.Envelope(requestID, nil, "failed", "invalid_target", "without workspace_id or root the path must be absolute; pass root for a repository file", map[string]any{"path": path})
		}
		absolute := filepath.Clean(path)
		if writesNewFile(kind, field, operation) {
			if _, statErr := os.Stat(absolute); statErr == nil {
				return nil, mcpapi.Envelope(requestID, nil, "conflict", workspacecore.CodeCreateTargetExists, absolute+" already exists; nothing changed", map[string]any{"path": absolute})
			}
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				return nil, mcpapi.Envelope(requestID, nil, "failed", "create_failed", err.Error(), map[string]any{})
			}
		}
		operation[field] = absolute
		named = append(named, absolute)
	}
	return named, nil
}

// writesNewFile reports whether one field of an operation names a path that
// must not exist yet, which is also the path whose parent directory is
// created before the workspace opens over it.
func writesNewFile(kind, field string, operation map[string]any) bool {
	if field == "from" {
		return false
	}
	switch kind {
	case "move_file", "copy_file":
		return true
	case "create_file":
		return operation["replace"] != true
	default:
		return false
	}
}
