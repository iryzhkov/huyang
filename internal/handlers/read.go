package handlers

import (
	"bytes"
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// readRequest is the decoded read call. Exactly one of Handle, Path,
// Symbol (path and name_path) or the file_range path addresses a single
// target; Targets carries several path or symbol reads for one call.
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
	Numbered  bool
	// MaxLines caps the lines delivered for a source read; zero is no cap.
	// A capped read says so and reports the total, so the agent can window
	// or outline the rest instead of receiving a blind dump.
	MaxLines int
	Targets  []map[string]any
}

func decodeReadRequest(arguments map[string]any) (readRequest, bool) {
	request := readRequest{
		StartLine: argInt(arguments, "start_line", 0), EndLine: argInt(arguments, "end_line", 0),
		Limit: argInt(arguments, "limit", 20), MaxLines: argInt(arguments, "max_lines", 0),
	}
	request.Numbered, _ = arguments["numbered"].(bool)
	request.View, _ = arguments["view"].(string)
	for _, raw := range mcpapi.AnySlice(arguments["targets"]) {
		if target, ok := raw.(map[string]any); ok {
			request.Targets = append(request.Targets, target)
		}
	}
	target, ok := arguments["target"].(map[string]any)
	if !ok {
		return request, len(request.Targets) > 0
	}
	decodeReadTarget(&request, target)
	return request, true
}

// decodeReadTarget fills the single-target fields from one target object.
func decodeReadTarget(request *readRequest, target map[string]any) {
	request.Handle, _ = target["handle"].(string)
	request.Path, request.HasPath = target["path"].(string)
	request.Symbol, _ = target["symbol_locator"].(map[string]any)
	if fileRange, present := target["file_range"].(map[string]any); present {
		request.HasRange = true
		request.Path, _ = fileRange["path"].(string)
	}
}

func (h *Handlers) read(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	request, ok := decodeReadRequest(arguments)
	if !ok {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "read requires target or targets", map[string]any{})
	}
	if len(request.Targets) > 0 && request.Handle == "" && !request.HasPath && request.Symbol == nil && !request.HasRange {
		return h.readMany(ctx, requestID, workspace, request)
	}
	return h.readOne(ctx, requestID, workspace, request)
}

// readOne dispatches a single-target read on the shape of its target.
func (h *Handlers) readOne(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
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

// readMany answers several path or symbol reads in one call. Each target
// is read independently; a target that fails is reported in place with its
// code and does not fail the others. The reply lists every target's size
// under entries, which sorts ahead of the bodies, so a client that shows
// only the head of a large reply still shows what each target weighs.
func (h *Handlers) readMany(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	files := make([]map[string]any, 0, len(request.Targets))
	entries := make([]map[string]any, 0, len(request.Targets))
	failed, truncated := 0, 0
	for index, target := range request.Targets {
		single := readRequest{
			View: "source", StartLine: argInt(target, "start_line", 0), EndLine: argInt(target, "end_line", 0),
			Limit: request.Limit, Numbered: request.Numbered, MaxLines: argInt(target, "max_lines", request.MaxLines),
		}
		decodeReadTarget(&single, target)
		result := h.readOne(ctx, fmt.Sprintf("%s_%d", requestID, index), workspace, single)
		if result["outcome"] != "ok" {
			failed++
			files = append(files, map[string]any{"path": readTargetLabel(single), "code": result["code"], "error": result["summary"]})
			entries = append(entries, map[string]any{"path": readTargetLabel(single), "code": result["code"]})
			continue
		}
		data, _ := result["data"].(map[string]any)
		files = append(files, data)
		entry := readEntry(data)
		if entry["truncated"] == true {
			truncated++
		}
		entries = append(entries, entry)
	}
	outcome, summary := "ok", fmt.Sprintf("Read %d targets", len(files))
	if failed > 0 {
		outcome, summary = "partial", fmt.Sprintf("Read %d of %d targets; %d failed", len(files)-failed, len(files), failed)
	}
	if truncated > 0 {
		summary += fmt.Sprintf("; %d truncated at max_lines", truncated)
	}
	return mcpapi.Envelope(requestID, workspace, outcome, "", summary, map[string]any{"entries": entries, "files": files})
}

// readEntry is the size line of one delivered target: its path, the total
// lines of the document or declaration, the bytes delivered, and whether
// the delivery stopped at max_lines.
func readEntry(data map[string]any) map[string]any {
	content, _ := data["content"].(string)
	entry := map[string]any{"path": data["path"], "bytes": len(content)}
	if lines, ok := data["lines"].(int); ok {
		entry["lines"] = lines
	} else {
		entry["lines"] = lineCount([]byte(content))
	}
	if name, ok := data["name_path"]; ok {
		entry["name_path"] = name
	}
	if data["truncated"] == true {
		entry["truncated"] = true
	}
	return entry
}

func readTargetLabel(request readRequest) string {
	if request.Symbol != nil {
		return fmt.Sprintf("%v#%v", request.Symbol["path"], request.Symbol["name_path"])
	}
	return request.Path
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
		data := map[string]any{
			"path": resolved.Path, "content": string(read.Content[resolved.ByteStart:resolved.ByteEnd]),
			"revision_id": read.Snapshot.Revision, "handle": request.Handle,
		}
		if resolution.Status == workspacecore.ResolutionRelocated {
			data["relocated"] = true
		}
		return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Read %s", resolved.Path), data)
	}
	return readPath(requestID, workspace, request)
}

// readSymbol reads the declaration a symbol locator names: natively when
// the sectioner understands the language, otherwise through the provider.
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
		// The native sectioner covers Go and Python; every other language
		// resolves through the same provider-backed path symbol_find uses,
		// which registers durable handles the locator can then select.
		if record, ok := h.resolveSymbolLocatorViaProvider(ctx, requestID, workspace, path, name); ok {
			exact = []workspacecore.HandleRecord{record}
			coverage = workspacecore.Coverage{Complete: true, Semantic: "embedded_nvim"}
		}
	}
	if len(exact) != 1 {
		return symbolReadMiss(requestID, workspace, coverage, exact, rawName, path)
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
	startLine := bytes.Count(read.Content[:current.ByteStart], []byte("\n")) + 1
	endLine := bytes.Count(read.Content[:current.ByteEnd], []byte("\n")) + 1
	return mcpapi.Envelope(requestID, workspace, "ok", "", fmt.Sprintf("Read symbol %s", name), map[string]any{
		"path": path, "name_path": name, "kind": current.Kind, "content": string(read.Content[current.ByteStart:current.ByteEnd]),
		"revision_id": read.Snapshot.Revision, "handle": resolved.Handle, "start_line": startLine, "end_line": endLine,
		"coverage": coverage,
	})
}

// symbolReadMiss answers a locator that resolved to no or several
// declarations, with the cheapest recovery for each case.
func symbolReadMiss(requestID string, workspace *workspacecore.Workspace, coverage workspacecore.Coverage, exact []workspacecore.HandleRecord, rawName, path string) map[string]any {
	outcome, code, summary := "conflict", "symbol_not_found", "Symbol locator did not resolve uniquely"
	if !coverage.Complete {
		outcome, code, summary = "unavailable", "semantic_provider_unavailable", "Symbol read requires parser coverage that is unavailable"
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{"coverage": coverage, "matches": exact})
	result["next"] = []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": rawName, "path": path}, map[string]any{"tool": "read", "action": "read_known_path", "path": path}}
	return result
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
	firstLine := actualStart
	if firstLine == 0 {
		firstLine = 1
	}
	content, capped := capLines(content, request.MaxLines)
	if request.Numbered {
		content = numberLines(content, firstLine)
	}
	data := map[string]any{"path": read.Path, "content": string(content), "revision_id": read.Snapshot.Revision, "lines": lineCount(read.Content)}
	if request.StartLine != 0 || request.EndLine != 0 {
		data["start_line"], data["end_line"] = actualStart, actualEnd
	}
	if !read.Coverage.Complete {
		data["coverage"] = read.Coverage
	}
	summary := fmt.Sprintf("Read %s", read.Path)
	if capped {
		lastLine := firstLine + request.MaxLines - 1
		data["truncated"], data["delivered_end_line"] = true, lastLine
		summary = fmt.Sprintf("Read %s: lines %d to %d of %d; truncated at max_lines", read.Path, firstLine, lastLine, lineCount(read.Content))
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
	if capped {
		result["next"] = []any{
			map[string]any{"tool": "read", "action": "outline_then_window_what_matters", "path": read.Path, "view": "outline"},
			map[string]any{"tool": "read", "action": "continue_from_line", "path": read.Path, "start_line": firstLine + request.MaxLines},
		}
	}
	return result
}

// numberLines prefixes every line with its 1-based number and a tab, so an
// agent can cite or window a line without counting.
func numberLines(content []byte, first int) []byte {
	lines := bytes.Split(content, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	var out bytes.Buffer
	for index, line := range lines {
		fmt.Fprintf(&out, "%d\t%s\n", first+index, line)
	}
	return out.Bytes()
}

// resolveSymbolLocatorViaProvider asks the semantic provider for the
// declaration when the native sectioner cannot section the document.
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
