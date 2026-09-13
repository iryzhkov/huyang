package handlers

// Reading and asking about a prepared revision.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// readPrepared answers a path read against staged bytes. The reply names the
// prepared revision it read, so an agent cannot mistake a proposal for the
// repository.
func (h *Handlers) readPrepared(requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, request readRequest) map[string]any {
	if request.Symbol != nil || request.Handle != "" || request.View == "history" || request.View == "changes" {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "prepared_target_unsupported",
			"a prepared revision is read by path; symbol, handle, history and changes views read the canonical workspace",
			map[string]any{"revision": view.PreparedRevision})
	}
	path := workspacePath(workspace, request.Path)
	absolute, err := preparedPath(view, path)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "invalid_target", err)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		return mcpapi.Envelope(requestID, workspace, "conflict", "prepared_path_missing",
			fmt.Sprintf("%s does not exist in %s", path, view.PreparedRevision),
			map[string]any{"path": path, "revision": view.PreparedRevision})
	}
	body, firstLine, lastLine, rangeErr := boundedLines(content, request.StartLine, request.EndLine)
	if rangeErr != nil {
		return mcpapi.Failure(requestID, workspace, "invalid_range", rangeErr)
	}
	if request.Numbered {
		body = numberLines(body, firstLine)
	}
	text := string(body)
	data := map[string]any{
		"path": path, "content": text, "lines": lineCount(content),
		"start_line": firstLine, "end_line": lastLine,
		// The revision this content is, and the plan it belongs to: a
		// prepared read is only meaningful beside them.
		"revision_id": view.PreparedRevision, "plan_id": view.PlanID, "plan_revision": view.PlanRevision,
		"handle": preparedHandle(view, path, content),
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "",
		fmt.Sprintf("Read %s at %s", path, view.PreparedRevision), data)
}

// preparedHandle names bytes inside one prepared revision. It is deliberately
// not a workspace handle: it cannot be resolved against canonical source, and
// it stops meaning anything the moment the preparation it names is replaced.
func preparedHandle(view providerpool.PreparedView, path string, content []byte) string {
	digest := sha256.Sum256(content)
	return fmt.Sprintf("prep_%s_%s_%s", strings.TrimPrefix(view.PreparedRevision, "prep_")[:12], hex.EncodeToString(digest[:6]), path)
}

// preparedProviderTarget is the position arguments a provider call needs,
// pointed at the staged copy of the file rather than the canonical one.
func preparedProviderTarget(view providerpool.PreparedView, workspace *workspacecore.Workspace, target map[string]any) (map[string]any, error) {
	locator, _ := target["symbol_locator"].(map[string]any)
	path, _ := target["path"].(string)
	if locator != nil {
		path, _ = locator["path"].(string)
	}
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("a prepared navigation needs target.path or target.symbol_locator.path")
	}
	relative := workspacePath(workspace, path)
	absolute, err := preparedPath(view, relative)
	if err != nil {
		return nil, err
	}
	arguments := map[string]any{"root": view.Tree, "file": absolute}
	if line := argInt(target, "line", 0); line > 0 {
		arguments["line"] = line
	}
	if column := argInt(target, "col", 0); column > 0 {
		arguments["col"] = column
	}
	if locator != nil {
		name, _ := locator["name_path"].(string)
		leaf := canonicalNamePath(name)
		if index := strings.LastIndexAny(leaf, "/"); index >= 0 {
			leaf = leaf[index+1:]
		}
		arguments["symbol"] = leaf
		if line, ok := declarationLine(absolute, leaf); ok {
			arguments["line"] = line
		}
	}
	if symbol, ok := target["symbol"].(string); ok && symbol != "" {
		arguments["symbol"] = symbol
	}
	return arguments, nil
}

// declarationLine finds the line a name is declared on inside a staged file.
// The canonical symbol index cannot answer this: it describes other bytes.
// The native sectioner reads the staged content directly where it knows the
// language; otherwise the first mention outside a comment is the best guess
// available, and a comment is the one place a language server will refuse a
// position with "no identifier found".
func declarationLine(absolute, leaf string) (int, bool) {
	content, err := os.ReadFile(absolute)
	if err != nil || leaf == "" {
		return 0, false
	}
	if sections, sectionErr := (workspacecore.NativeSectioner{}).Sections(absolute, content); sectionErr == nil {
		for _, section := range sections {
			name := section.Name
			if index := strings.LastIndexAny(name, "/"); index >= 0 {
				name = name[index+1:]
			}
			if name != leaf {
				continue
			}
			// The declaration's range starts at its documentation comment,
			// so the identifier is on the first line inside it that names it.
			if line, ok := firstIdentifierLine(content[section.ByteStart:section.ByteEnd], leaf, bytes.Count(content[:section.ByteStart], []byte{'\n'})); ok {
				return line, true
			}
		}
	}
	return firstIdentifierLine(content, leaf, 0)
}

// firstIdentifierLine is the first line of content mentioning leaf outside a
// comment, counted from offset lines into the file.
func firstIdentifierLine(content []byte, leaf string, before int) (int, bool) {
	for index, line := range bytes.Split(content, []byte{'\n'}) {
		trimmed := strings.TrimLeft(string(line), " \t")
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(trimmed, leaf) {
			return before + index + 1, true
		}
	}
	return 0, false
}

// callPrepared runs one provider operation against the prepared sandbox and
// maps every path in the answer back to what the agent knows.
func (h *Handlers) callPrepared(ctx context.Context, requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, operation string, arguments map[string]any) (any, map[string]any) {
	if view.Provider == nil {
		return nil, mcpapi.Envelope(requestID, workspace, "unavailable", "prepared_provider_unavailable",
			"the prepared revision has no language server; prepare it again to start one",
			map[string]any{"revision": view.PreparedRevision})
	}
	value, err := providerpool.CallCanonical(ctx, requestID, workspace, view.Provider, operation, arguments)
	if err != nil {
		return nil, modernProviderFailure(requestID, workspace, "language_server_unavailable", err)
	}
	return redactSandboxPaths(view, value), nil
}

// redactSandboxPaths rewrites every path a provider answered with into its
// canonical-facing form, and drops anything outside the prepared tree. A
// sandbox path is the service's own business and must never reach a caller.
func redactSandboxPaths(view providerpool.PreparedView, value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = redactSandboxPaths(view, item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, redactSandboxPaths(view, item))
		}
		return out
	case string:
		if relative, ok := canonicalFacing(view, typed); ok {
			return relative
		}
		if strings.Contains(typed, view.Tree) {
			return strings.ReplaceAll(typed, view.Tree+"/", "")
		}
		return typed
	default:
		return value
	}
}
