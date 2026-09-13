package handlers

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
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
	MaxLines         int
	MaxBytes         int
	Compact          bool
	ByteOffset       int
	ExpectedRevision string
	Targets          []map[string]any
}

func decodeReadRequest(arguments map[string]any) (readRequest, bool) {
	request := readRequest{
		StartLine: argInt(arguments, "start_line", 0), EndLine: argInt(arguments, "end_line", 0),
		Limit: argInt(arguments, "limit", 20), MaxLines: argInt(arguments, "max_lines", 0),
	}
	request.MaxBytes = argInt(arguments, "max_bytes", 0)
	request.Compact = arguments["response_mode"] == "compact"
	request.ByteOffset = argInt(arguments, "byte_offset", 0)
	request.ExpectedRevision, _ = arguments["expected_revision_id"].(string)
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
func (h *Handlers) readOneUnbounded(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	if request.View == "changes" {
		return readCommitChanges(requestID, workspace, request)
	}
	switch {
	case request.Handle != "":
		return h.readHandle(ctx, requestID, workspace, request)
	case request.HasPath:
		return h.readPath(ctx, requestID, workspace, request)
	case request.Symbol != nil:
		return h.readSymbol(ctx, requestID, workspace, request)
	case request.HasRange:
		return h.readPath(ctx, requestID, workspace, request)
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
			MaxBytes: argInt(target, "max_bytes", request.MaxBytes), ByteOffset: argInt(target, "byte_offset", request.ByteOffset),
			ExpectedRevision: request.ExpectedRevision, Compact: request.Compact,
		}
		// Every option of a single-target read applies per target, so one
		// call can outline one file and window another.
		if view, ok := target["view"].(string); ok && view != "" {
			single.View = view
		}
		if numbered, ok := target["numbered"].(bool); ok {
			single.Numbered = numbered
		}
		if revision, ok := target["expected_revision_id"].(string); ok {
			single.ExpectedRevision = revision
		}
		decodeReadTarget(&single, target)
		result := h.readOne(ctx, fmt.Sprintf("%s_%d", requestID, index), workspace, single)
		if result["outcome"] != "ok" {
			failed++
			files = append(files, map[string]any{"path": readTargetLabel(single), "code": result["code"], "error": result["summary"], "next": result["next"]})
			entries = append(entries, map[string]any{"path": readTargetLabel(single), "code": result["code"]})
			continue
		}
		data := readTargetData(result["data"], readTargetLabel(single))
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
		// A source target stops at max_lines and an outline at the outline
		// bound, so the count is of deliveries that were cut, whichever bound
		// cut them; each entry says which one it hit.
		summary += fmt.Sprintf("; %d truncated", truncated)
	}
	return mcpapi.Envelope(requestID, workspace, outcome, "", summary, map[string]any{"entries": entries, "files": files})
}

// readTargetData normalises one target's payload into the map a multi-target
// reply carries. The source and outline views already answer a map; anything
// else is carried as it is under the target's label, so one odd view never
// costs the other targets their shape.
func readTargetData(payload any, label string) map[string]any {
	switch data := payload.(type) {
	case map[string]any:
		return data
	default:
		return map[string]any{"path": label, "result": payload}
	}
}

// readEntry is the size line of one delivered target: its path, the total
// lines of the document or declaration, the bytes delivered, and whether
// the delivery stopped at max_lines. An outline target has no bytes of its
// own, so its entry counts the sections instead.
func readEntry(data map[string]any) map[string]any {
	if _, ok := data["sections"].([]map[string]any); ok {
		// The count is what the file declares, not what this reply listed, so
		// a truncated outline is not mistaken for a small file.
		entry := map[string]any{"path": data["path"], "section_count": data["declaration_count"]}
		if data["sections_truncated"] == true {
			entry["truncated"] = true
		}
		return entry
	}
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
func (h *Handlers) readHandle(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	resolution, err := workspace.ResolveHandle(workspacecore.HandleID(request.Handle))
	if err != nil {
		return handleResolveFailure(requestID, workspace, request.Handle, err)
	}
	if resolution.Status == workspacecore.ResolutionConflicted {
		return modernHandleConflict(requestID, workspace, resolution)
	}
	resolved, err := resolution.RangeHandle()
	if err != nil {
		return handleResolveFailure(requestID, workspace, request.Handle, err)
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
	return h.readPath(ctx, requestID, workspace, request)
}

// handleResolveFailure answers a handle that did not resolve. A handle is
// unknown because it was never issued, because the bounded store dropped it,
// or because the service was replaced under the caller, and the workspace's
// code says which; all three have the same repair, and a reply that offers it
// costs the caller one call instead of a guess. Handles are in-memory on
// purpose, so this is the ordinary way a long session meets one.
func handleResolveFailure(requestID string, workspace *workspacecore.Workspace, handle string, err error) map[string]any {
	code, outcome := workspacecore.ErrorCode(err), "conflict"
	if code == "" {
		code, outcome = "handle_resolve_failed", "failed"
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, err.Error(), map[string]any{"handle": handle})
	result["next"] = []any{
		map[string]any{"tool": "search", "action": "repeat_the_search_and_take_a_fresh_handle", "include_handles": true},
		map[string]any{"tool": "read", "action": "read_by_path_or_symbol_locator_instead"},
	}
	return result
}

// readSymbol reads the declaration a symbol locator names: natively when
// the sectioner understands the language, otherwise through the provider.
func (h *Handlers) readSymbol(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
	rawPath, _ := request.Symbol["path"].(string)
	rawName, _ := request.Symbol["name_path"].(string)
	path, name := workspacePath(workspace, rawPath), canonicalNamePath(rawName)
	// The locator names one file, so only that file is parsed: the
	// workspace-wide scan read every document in the repository and answered
	// coverage about files this read never asked about.
	matches, coverage, findErr := workspace.FindSymbolsInFile(path, name)
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
		return symbolReadMiss(requestID, workspace, coverage, exact, matches, rawName, path)
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
	})
}

// symbolReadMiss answers a locator that resolved to no or several
// declarations, with the cheapest recovery for each case.
// candidateNamePaths names the declarations the file does have that carry
// the requested name, in file order and bounded, so a miss on a bare method
// name answers the qualified locator that resolves instead of a dead end.
func candidateNamePaths(nearby []workspacecore.HandleRecord, rawName string) []string {
	const maxCandidates = 5
	leaf := rawName
	if index := strings.LastIndexAny(leaf, "/."); index >= 0 {
		leaf = leaf[index+1:]
	}
	seen := make(map[string]bool, len(nearby))
	candidates := make([]string, 0, maxCandidates)
	for _, record := range nearby {
		name := record.Locator.NamePath
		if name == "" || name == rawName || seen[name] || !strings.Contains(name, leaf) {
			continue
		}
		seen[name] = true
		if candidates = append(candidates, name); len(candidates) == maxCandidates {
			break
		}
	}
	return candidates
}

func symbolReadMiss(requestID string, workspace *workspacecore.Workspace, coverage workspacecore.Coverage, exact, nearby []workspacecore.HandleRecord, rawName, path string) map[string]any {
	// A name that is not there and a name that is there twice are different
	// mistakes with different repairs - use another name, or qualify the one
	// you used - and "did not resolve uniquely" described both.
	outcome, code := "conflict", "symbol_not_found"
	summary := fmt.Sprintf("No declaration named %s in %s", rawName, path)
	// A method is declared under its receiver, so the bare name of one never
	// resolves. The scan already found those declarations; naming them here
	// is the difference between a dead end and a locator the caller can
	// retry in one call.
	candidates := candidateNamePaths(nearby, rawName)
	if len(exact) == 0 && len(candidates) > 0 {
		summary = fmt.Sprintf("No declaration named %s in %s; the file declares %s", rawName, path, strings.Join(candidates, ", "))
	}
	if len(exact) > 1 {
		summary = fmt.Sprintf("%d declarations named %s in %s; the locator selects none of them", len(exact), rawName, path)
	}
	if !coverage.Complete {
		outcome, code = "unavailable", "semantic_provider_unavailable"
		summary = fmt.Sprintf("No parser covers %s, so a declaration in it cannot be located by name", path)
	}
	data := map[string]any{"coverage": coverage, "matches": exact, "match_count": len(exact)}
	if len(candidates) > 0 {
		data["candidates"] = candidates
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, code, summary, data)
	next := []any{map[string]any{"tool": "search", "action": "literal_fallback", "query": rawName, "path": path}, map[string]any{"tool": "read", "action": "read_known_path", "path": path}}
	if len(exact) == 0 && coverage.Complete {
		// The outline is the list of names this file does declare, which is
		// the answer to "then what is it called".
		next = append([]any{map[string]any{"tool": "read", "action": "outline_the_file_to_see_what_it_declares", "path": path, "view": "outline"}}, next...)
	}
	result["next"] = next
	return result
}

// readPath answers the history, outline or source view of one path.
func (h *Handlers) readPath(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request readRequest) map[string]any {
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
		return h.readOutline(ctx, requestID, workspace, request.Path)
	}
	read, err := workspace.Read(request.Path)
	if err != nil {
		return missingPathFailure(requestID, workspace, request.Path, err)
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

// missingPathFailure answers a read of a path the workspace does not hold
// with the paths it does hold under that name. A file an agent expected in
// one directory and found in another is the commonest read failure in the
// friction spool, and "<path> is missing" ends the line of enquiry where the
// path that does exist would continue it.
func missingPathFailure(requestID string, workspace *workspacecore.Workspace, path string, err error) map[string]any {
	result := mcpapi.Failure(requestID, workspace, "read_failed", err)
	if !strings.Contains(err.Error(), "is missing") {
		return result
	}
	data, _ := result["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
		result["data"] = data
	}
	if candidates := pathsNamed(workspace, path); len(candidates) > 0 {
		data["candidates"] = candidates
		result["summary"] = fmt.Sprintf("No file at %s; the workspace holds %s", path, strings.Join(candidates, ", "))
		return result
	}
	result["summary"] = fmt.Sprintf("No file at %s, and no file of that name anywhere in the workspace", path)
	result["next"] = []any{map[string]any{"tool": "search", "action": "locate_the_file_by_name", "query": pathBase(path)}}
	return result
}

// pathsNamed are the workspace paths whose base name is the base name of
// path, bounded, in path order.
func pathsNamed(workspace *workspacecore.Workspace, path string) []string {
	const maxPathCandidates = 5
	base := pathBase(path)
	if base == "" {
		return nil
	}
	orientation, err := workspace.Orient()
	if err != nil {
		return nil
	}
	candidates := make([]string, 0, maxPathCandidates)
	for _, entry := range orientation.Entries {
		if pathBase(entry.Path) != base || entry.Path == path {
			continue
		}
		if candidates = append(candidates, entry.Path); len(candidates) == maxPathCandidates {
			break
		}
	}
	return candidates
}

func pathBase(path string) string {
	if index := strings.LastIndexByte(path, '/'); index >= 0 {
		return path[index+1:]
	}
	return path
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

// readOutline answers the outline of one path. The native sectioner covers
// Go and Python; for every other language the semantic provider is asked for
// the file's declarations, the same way a symbol locator resolves through it,
// so an outline is useful wherever a parser or a language server is. A file
// neither can section still answers its whole-document fallback handle.
func (h *Handlers) readOutline(ctx context.Context, requestID string, workspace *workspacecore.Workspace, path string) map[string]any {
	outline, err := workspace.Outline(path)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "read_failed", err)
	}
	// Only when the native sectioner did not understand the document: a file
	// it parsed and found nothing in declares nothing, and asking a language
	// server to confirm that costs a call and a buffer load.
	if len(outline.Sections) == 0 && outline.Coverage.Semantic != "parser_sections" {
		if sections, handles, ok := h.outlineViaProvider(ctx, requestID, workspace, outline.Path); ok {
			outline.Sections, outline.Handles = sections, handles
			outline.Fallback, outline.FallbackHandle = nil, nil
			outline.Coverage = workspacecore.Coverage{Complete: true, Semantic: "embedded_nvim"}
		}
	}
	summary := "Outline read: whole document, no declarations found"
	if count := len(outline.Sections); count > 0 {
		summary = fmt.Sprintf("Outline read: %d declarations", count)
	}
	var content []byte
	if read, readErr := workspace.Read(outline.Path); readErr == nil {
		content = read.Content
	}
	compact := mcpapi.CompactOutline(outline, content)
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, compact)
	if compact["sections_truncated"] == true {
		// The outline is the call the guide recommends for a large file, so a
		// generated one is exactly where it gets cut. Say which declarations
		// arrived and name the two ways to reach the rest.
		result["summary"] = fmt.Sprintf("Outline read: %d declarations, the first %d listed; truncated at the outline bound",
			compact["declaration_count"], compact["listed_count"])
		result["next"] = []any{
			map[string]any{"tool": "search", "action": "find_the_declaration_by_name", "paths": []string{outline.Path}},
			map[string]any{"tool": "read", "action": "window_what_matters", "path": outline.Path},
		}
	}
	return result
}

// outlineViaProvider asks the semantic provider for every declaration in one
// file and registers each as a durable symbol handle, so the sections it
// returns can be read and edited by locator like the native ones.
func (h *Handlers) outlineViaProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, path string) ([]workspacecore.Section, []workspacecore.HandleRecord, bool) {
	if workspace.Identity().Kind != workspacecore.KindProject {
		return nil, nil, false
	}
	backend, err := h.pool.Canonical(ctx, workspace)
	if err != nil {
		return nil, nil, false
	}
	value, err := providerpool.CallCanonical(ctx, requestID+"_outline", workspace, backend, "file_symbols", map[string]any{
		"root": workspace.Identity().Root, "file": path,
	})
	if err != nil {
		return nil, nil, false
	}
	payload, _ := value.(map[string]any)
	records, _ := registerProviderMatches(workspace, mcpapi.AnySlice(payload["matches"]))
	if len(records) == 0 {
		return nil, nil, false
	}
	// File order, whatever order the provider listed them in, and the
	// sections stay aligned with the handles that address them.
	sort.Slice(records, func(i, j int) bool { return records[i].Locator.ByteStart < records[j].Locator.ByteStart })
	sections := make([]workspacecore.Section, 0, len(records))
	for _, record := range records {
		sections = append(sections, workspacecore.Section{
			Name: record.Locator.NamePath, Kind: record.Locator.Kind,
			ByteStart: record.Locator.ByteStart, ByteEnd: record.Locator.ByteEnd,
		})
	}
	return sections, records, true
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
