package handlers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// readRequest is the decoded read call. Exactly one of Handle, Path,
// Symbol (path and name_path) or the file_range path addresses the target.
type readRequest struct {
	View      string
	Handle    string
	Path      string
	HasPath   bool
	Symbol    map[string]any
	HasRange  bool
	StartLine int
	EndLine   int
	Limit     int
}

func decodeReadRequest(arguments map[string]any) (readRequest, bool) {
	target, ok := arguments["target"].(map[string]any)
	if !ok {
		return readRequest{}, false
	}
	request := readRequest{
		StartLine: argInt(arguments, "start_line", 0), EndLine: argInt(arguments, "end_line", 0),
		Limit: argInt(arguments, "limit", 20),
	}
	request.View, _ = arguments["view"].(string)
	request.Handle, _ = target["handle"].(string)
	request.Path, request.HasPath = target["path"].(string)
	request.Symbol, _ = target["symbol_locator"].(map[string]any)
	if fileRange, present := target["file_range"].(map[string]any); present {
		request.HasRange = true
		request.Path, _ = fileRange["path"].(string)
	}
	return request, true
}

func (h *Handlers) read(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	request, ok := decodeReadRequest(arguments)
	if !ok {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "target must be an object", map[string]any{})
	}
	if request.View == "changes" {
		return readCommitChanges(requestID, workspace, request)
	}
	switch {
	case request.Handle != "":
		return readHandle(requestID, workspace, request)
	case request.HasPath:
		return readPath(requestID, workspace, request)
	case request.Symbol != nil:
		return h.readSymbol(ctx, requestID, workspace, request)
	case request.HasRange:
		return readPath(requestID, workspace, request)
	default:
		return mcpapi.Envelope(requestID, workspace, "unavailable", "target_kind_unavailable", "read requires target.path, target.handle, target.symbol_locator, or target.file_range", map[string]any{})
	}
}

// readCommitChanges answers the changes view from an opaque commit handle.
func readCommitChanges(requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	if request.Handle == "" {
		return mcpapi.Envelope(requestID, workspace, "failed", "commit_handle_required", "changes view requires an opaque commit handle", map[string]any{})
	}
	changes, err := workspace.CommitChanges(workspacecore.CommitHandle(request.Handle), request.Limit)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "commit_changes_failed", err)
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("%d changed paths", len(changes.Changes)), changes)
}

// readHandle resolves a revision-bound handle. Without a view or line
// window it returns the handle's exact bytes; the history view maps the
// handle to its lines, and any other view falls through to the path read.
func readHandle(requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	resolution, err := workspace.ResolveHandle(workspacecore.HandleID(request.Handle))
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "handle_resolve_failed", err)
	}
	if resolution.Status == workspacecore.ResolutionConflicted {
		return modernHandleConflict(requestID, workspace, resolution)
	}
	resolved, err := resolution.RangeHandle()
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "handle_resolve_failed", err)
	}
	request.Path = resolved.Path
	read, err := workspace.Read(request.Path)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "read_failed", err)
	}
	if resolved.ByteStart < 0 || resolved.ByteEnd > len(read.Content) || resolved.ByteEnd < resolved.ByteStart {
		return mcpapi.Envelope(requestID, workspace, "conflict", "target_deleted", "Resolved handle range is no longer readable", resolution)
	}
	if request.View == "history" {
		request.StartLine = bytes.Count(read.Content[:resolved.ByteStart], []byte("\n")) + 1
		request.EndLine = bytes.Count(read.Content[:resolved.ByteEnd], []byte("\n")) + 1
	} else if request.StartLine == 0 && request.EndLine == 0 {
		return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", resolved.Path), map[string]any{
			"path": resolved.Path, "content": string(read.Content[resolved.ByteStart:resolved.ByteEnd]), "snapshot": read.Snapshot,
			"coverage": read.Coverage, "resolution": resolution,
		})
	}
	return readPath(requestID, workspace, request)
}

// readSymbol reads the declaration a symbol locator names, resolving it
// through the provider when the native text core has no parser coverage.
func (h *Handlers) readSymbol(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	rawPath, _ := request.Symbol["path"].(string)
	rawName, _ := request.Symbol["name_path"].(string)
	path, name := workspacePath(workspace, rawPath), canonicalNamePath(rawName)
	matches, coverage, findErr := workspace.FindSymbols(name)
	if findErr != nil {
		return mcpapi.Failure(requestID, workspace, "symbol_read_failed", findErr)
	}
	var exact []workspacecore.HandleRecord
	for _, match := range matches {
		if match.Locator.Path == path && match.Locator.NamePath == name {
			exact = append(exact, match)
		}
	}
	if len(exact) != 1 {
		// The bridge configures no built-in sectioner, so the native
		// FindSymbols never has parser coverage. Resolve through the
		// same provider-backed path symbol_find uses, which registers
		// durable handles the locator can then select.
		if record, ok := h.resolveSymbolLocatorViaProvider(ctx, requestID, workspace, path, name); ok {
			exact = []workspacecore.HandleRecord{record}
			coverage = workspacecore.Coverage{Complete: true, Semantic: "embedded_nvim"}
		}
	}
	if len(exact) != 1 {
		outcome, code, summary := "conflict", "symbol_not_found", "Symbol locator did not resolve uniquely"
		if !coverage.Complete {
			outcome, code, summary = "unavailable", "semantic_provider_unavailable", "Symbol read requires parser coverage that is unavailable"
		}
		result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{"coverage": coverage, "matches": exact})
		result["next"] = []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": rawName, "path": path}, map[string]any{"tool": "read", "action": "read_known_path", "path": path}}
		return result
	}
	resolved, resolveErr := workspace.ResolveHandle(exact[0].Handle)
	if resolveErr != nil {
		return mcpapi.Failure(requestID, workspace, "symbol_read_failed", resolveErr)
	}
	if resolved.Current == nil {
		return modernHandleConflict(requestID, workspace, resolved)
	}
	read, readErr := workspace.Read(path)
	if readErr != nil {
		return mcpapi.Failure(requestID, workspace, "read_failed", readErr)
	}
	current := resolved.Current
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Read symbol %s", name), map[string]any{
		"path": path, "content": string(read.Content[current.ByteStart:current.ByteEnd]), "snapshot": read.Snapshot,
		"coverage": coverage, "resolution": resolved,
	})
}

// readPath answers the history, outline or source view of one path.
func readPath(requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	switch request.View {
	case "history":
		history, err := workspace.FileHistory(workspacecore.HistoryRequest{
			Path: request.Path, StartLine: request.StartLine, EndLine: request.EndLine, Limit: request.Limit,
		})
		if err != nil {
			return mcpapi.Failure(requestID, workspace, "git_history_read_failed", err)
		}
		return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("%d provenance spans", len(history.Spans)), history)
	case "outline":
		outline, err := workspace.Outline(request.Path)
		if err != nil {
			return mcpapi.Failure(requestID, workspace, "read_failed", err)
		}
		return mcpapi.Envelope(requestID, workspace, "ok", "", "Outline read", outline)
	}
	read, err := workspace.Read(request.Path)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "read_failed", err)
	}
	content, actualStart, actualEnd, rangeErr := boundedLines(read.Content, request.StartLine, request.EndLine)
	if rangeErr != nil {
		return mcpapi.Failure(requestID, workspace, "invalid_line_range", rangeErr)
	}
	data := map[string]any{"path": read.Path, "content": string(content), "snapshot": read.Snapshot, "coverage": read.Coverage}
	if request.StartLine != 0 || request.EndLine != 0 {
		data["start_line"], data["end_line"] = actualStart, actualEnd
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", read.Path), data)
}

// resolveSymbolLocatorViaProvider asks the semantic provider for the
// declaration when the native text core cannot section the document.
func (h *Handlers) resolveSymbolLocatorViaProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, path, name string) (workspacecore.HandleRecord, bool) {
	if record, err := workspace.ResolveSymbolLocator(path, name); err == nil {
		return record, true
	}
	if workspace.Identity().Kind != workspacecore.KindProject {
		return workspacecore.HandleRecord{}, false
	}
	h.symbolFind(ctx, requestID+"_resolve", workspace, map[string]any{"query": name})
	record, err := workspace.ResolveSymbolLocator(path, name)
	return record, err == nil
}

func modernHandleConflict(requestID string, workspace *workspacecore.Workspace, resolution workspacecore.HandleResolution) map[string]any {
	summary := "The revision-bound target changed and must be refreshed"
	switch resolution.Code {
	case workspacecore.ConflictTargetDeleted:
		summary = "The revision-bound target no longer exists"
	case workspacecore.ConflictSymbolAmbiguous:
		summary = "The revision-bound target now resolves to multiple candidates"
	case workspacecore.ConflictSymbolSignatureChanged:
		summary = "The target symbol signature changed"
	case workspacecore.ConflictDocumentChanged:
		summary = "The target document changed since this handle was issued"
	}
	result := mcpapi.Envelope(requestID, workspace, "conflict", string(resolution.Code), summary, resolution)
	path := resolution.Original.Path
	next := []any{}
	if resolution.Code != workspacecore.ConflictTargetDeleted {
		next = append(next, map[string]any{"tool": "read", "action": "refresh_path", "path": path})
	} else if destination, ok := workspace.RecentRenameDestination(path); ok {
		result["summary"] = fmt.Sprintf("The revision-bound target moved from %s to %s", path, destination)
		next = append(next, map[string]any{
			"tool": "read", "action": "recover_at_detected_rename",
			"path": destination, "source_path": path,
		})
	} else {
		next = append(next, map[string]any{
			"tool": "search", "action": "inspect_git_rename_history",
			"git_history": map[string]any{"query": path, "fields": []string{"path", "diff"}},
		})
	}
	if resolution.Original.NamePath != "" {
		next = append(next, map[string]any{"tool": "symbol_find", "action": "relocate_target", "query": resolution.Original.NamePath})
	}
	result["next"] = next
	return result
}
