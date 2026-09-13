package handlers

// The seam where a refactor a language server owns becomes ordinary guarded
// edits. rename_symbol, apply_code_action, inline_symbol and safe_delete_symbol
// are request vocabulary: the server is asked what it would change, its answer
// is bound to the current document revisions as replace_range operations, and
// the plan stores those. Preview, prepare, verification, apply and recovery
// then work on exact bytes, which is what makes a server refactor previewable
// and reversible here rather than an unexplained rewrite.
//
// Every expanded operation carries DerivedFrom, so a plan of twelve ranges
// still says which one request produced it.

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

// Request-only operation kinds, expanded before a plan is stored.
const (
	kindRenameSymbol     = "rename_symbol"
	kindApplyCodeAction  = "apply_code_action"
	kindInlineSymbol     = "inline_symbol"
	kindSafeDeleteSymbol = "safe_delete_symbol"
)

// inlineActionKinds are the LSP code-action kinds that mean "inline this".
// A server may spell it more specifically (refactor.inline.variable), which
// the kernel matches by prefix.
var inlineActionKinds = []any{"refactor.inline"}

func providerOperationKind(kind workspacecore.OperationKind) bool {
	switch string(kind) {
	case kindRenameSymbol, kindApplyCodeAction, kindInlineSymbol, kindSafeDeleteSymbol:
		return true
	}
	return false
}

// expandProviderOperations replaces every server-owned operation with the
// exact ranges the server proposes. Operations of any other kind are kept as
// they are and in their place.
func (h *Handlers) expandProviderOperations(ctx context.Context, requestID string, workspace *workspacecore.Workspace, operations []workspacecore.PlanOperation) ([]workspacecore.PlanOperation, error) {
	if !anyProviderOperation(operations) {
		return operations, nil
	}
	expanded := make([]workspacecore.PlanOperation, 0, len(operations))
	for _, operation := range operations {
		if !providerOperationKind(operation.Kind) {
			expanded = append(expanded, operation)
			continue
		}
		produced, err := h.expandOneProviderOperation(ctx, requestID, workspace, operation)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, produced...)
	}
	return expanded, nil
}

func anyProviderOperation(operations []workspacecore.PlanOperation) bool {
	for _, operation := range operations {
		if providerOperationKind(operation.Kind) {
			return true
		}
	}
	return false
}

func (h *Handlers) expandOneProviderOperation(ctx context.Context, requestID string, workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) ([]workspacecore.PlanOperation, error) {
	if string(operation.Kind) == kindSafeDeleteSymbol {
		return h.expandSafeDelete(ctx, requestID, workspace, operation)
	}
	arguments, err := h.workspaceEditArguments(workspace, operation)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	value, err := h.callProvider(ctx, requestID+"_"+operation.OpID, workspace, "huyang_workspace_edit", arguments)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	payload, _ := value.(map[string]any)
	changes := mcpapi.AnySlice(payload["changes"])
	if len(changes) == 0 {
		return nil, fmt.Errorf("%s: %s", operation.OpID, noWorkspaceEditReason(operation, payload))
	}
	if operations := mcpapi.AnySlice(payload["file_operations"]); len(operations) > 0 {
		// Creating, renaming or deleting files is a different guard: those
		// paths go through move_file, create_file and delete_file, which
		// state what they touch and what Git has to be told.
		return nil, fmt.Errorf("%s: the server's edit also creates, renames or deletes files (%v), which this operation cannot stage", operation.OpID, operations)
	}
	return rangeOperations(workspace, operation, changes)
}

// noWorkspaceEditReason explains an empty answer in the caller's terms: for a
// code action, which actions the server did offer instead.
func noWorkspaceEditReason(operation workspacecore.PlanOperation, payload map[string]any) string {
	offered := mcpapi.AnySlice(payload["actions_offered"])
	if len(offered) == 0 {
		return "the language server proposed no edit for this operation"
	}
	titles := make([]string, 0, len(offered))
	for _, raw := range offered {
		action, _ := raw.(map[string]any)
		titles = append(titles, fmt.Sprintf("%v (%v)", action["title"], action["kind"]))
	}
	return fmt.Sprintf("no code action here matches this operation; the server offers: %s", strings.Join(titles, "; "))
}

// workspaceEditArguments asks the kernel the question one operation implies.
func (h *Handlers) workspaceEditArguments(workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) (map[string]any, error) {
	position, err := planTargetPosition(workspace, operation)
	if err != nil {
		return nil, err
	}
	arguments := map[string]any{"root": workspace.Identity().Root}
	for key, value := range position {
		arguments[key] = value
	}
	switch string(operation.Kind) {
	case kindRenameSymbol:
		if strings.TrimSpace(operation.Content) == "" {
			return nil, fmt.Errorf("rename_symbol requires the new name in content")
		}
		arguments["kind"], arguments["new_name"] = "rename", operation.Content
	case kindApplyCodeAction:
		if strings.TrimSpace(operation.Content) == "" {
			return nil, fmt.Errorf("apply_code_action requires the action title in content; code_actions lists them")
		}
		arguments["kind"], arguments["title"] = "code_action", operation.Content
	case kindInlineSymbol:
		arguments["kind"], arguments["only"] = "code_action", inlineActionKinds
		if title := strings.TrimSpace(operation.Content); title != "" {
			arguments["title"] = title
		}
	}
	return arguments, nil
}

// planTargetPosition converts a plan target into the file and position the
// kernel takes, reusing the same resolution every other provider call uses.
func planTargetPosition(workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) (map[string]any, error) {
	if operation.Target == nil {
		return nil, fmt.Errorf("%s requires target", operation.Kind)
	}
	encoded := map[string]any{}
	switch {
	case operation.Target.SymbolLocator != nil:
		encoded["symbol_locator"] = map[string]any{
			"path": operation.Target.SymbolLocator.Path, "name_path": operation.Target.SymbolLocator.NamePath,
		}
	case operation.Target.Handle != "":
		encoded["handle"] = string(operation.Target.Handle)
	case operation.Target.FileRange != nil:
		var rangeMap map[string]any
		if err := decodeJSON(operation.Target.FileRange, &rangeMap); err != nil {
			return nil, err
		}
		encoded["file_range"] = rangeMap
	default:
		return nil, fmt.Errorf("%s requires target", operation.Kind)
	}
	return modernProviderTarget(workspace, encoded)
}

// rangeOperations turns the kernel's byte edits into replace_range operations.
// The kernel orders each file's edits from its end backwards, and the chain of
// dependencies keeps them in that order, so no edit moves the bytes another
// one is bound to.
func rangeOperations(workspace *workspacecore.Workspace, operation workspacecore.PlanOperation, changes []any) ([]workspacecore.PlanOperation, error) {
	var produced []workspacecore.PlanOperation
	previous := ""
	for _, rawFile := range changes {
		change, _ := rawFile.(map[string]any)
		path := workspacePath(workspace, fmt.Sprint(change["file"]))
		edits := mcpapi.AnySlice(change["edits"])
		sort.SliceStable(edits, func(i, j int) bool {
			return editStart(edits[i]) > editStart(edits[j])
		})
		for _, rawEdit := range edits {
			edit, _ := rawEdit.(map[string]any)
			start, end := editStart(rawEdit), argInt(edit, "byte_end", -1)
			handle, err := workspace.NewRange(path, start, end)
			if err != nil {
				return nil, fmt.Errorf("%s: %s: %w", operation.OpID, path, err)
			}
			next := workspacecore.PlanOperation{
				OpID:        fmt.Sprintf("%s_%d", operation.OpID, len(produced)+1),
				Kind:        workspacecore.OperationReplaceRange,
				Target:      &workspacecore.PlanTarget{FileRange: &handle},
				Content:     fmt.Sprint(edit["new_text"]),
				DependsOn:   operation.DependsOn,
				DerivedFrom: fmt.Sprintf("%s %s", operation.Kind, operation.OpID),
			}
			if previous != "" {
				next.DependsOn = append(append([]string(nil), operation.DependsOn...), previous)
			}
			previous = next.OpID
			produced = append(produced, next)
		}
	}
	if len(produced) == 0 {
		return nil, fmt.Errorf("%s: the language server proposed no edit for this operation", operation.OpID)
	}
	return produced, nil
}

// maxReportedReferences bounds the call sites a refusal lists; the rest are
// counted.
const maxReportedReferences = 5

// expandSafeDelete deletes a declaration only when nothing outside it still
// refers to it. The check is the language server's own reference list, which
// is the same question an agent would otherwise ask by hand and forget to ask
// under time pressure. A truncated list refuses too: a deletion that cannot
// be proved safe is not safe.
func (h *Handlers) expandSafeDelete(ctx context.Context, requestID string, workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) ([]workspacecore.PlanOperation, error) {
	path, startLine, endLine, err := declarationLines(workspace, operation)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	position, err := planTargetPosition(workspace, operation)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	position["root"] = workspace.Identity().Root
	value, err := h.callProvider(ctx, requestID+"_"+operation.OpID, workspace, "references", position)
	if err != nil {
		return nil, fmt.Errorf("%s: the references that would prove this deletion safe are unavailable: %w", operation.OpID, err)
	}
	payload, _ := value.(map[string]any)
	locations := mcpapi.AnySlice(payload["locations"])
	if total := argInt(payload, "count", len(locations)); total > len(locations) {
		return nil, fmt.Errorf("%s: the server reported %d references but listed %d, so this deletion cannot be proved safe", operation.OpID, total, len(locations))
	}
	if outside := referencesOutside(workspace, locations, path, startLine, endLine); len(outside) > 0 {
		return nil, fmt.Errorf("%s: %d reference(s) remain: %s", operation.OpID, len(outside), strings.Join(boundedReferences(outside), "; "))
	}
	deletion := operation
	deletion.Kind, deletion.Content = workspacecore.OperationDeleteSymbol, ""
	deletion.DerivedFrom = fmt.Sprintf("%s %s", operation.Kind, operation.OpID)
	return []workspacecore.PlanOperation{deletion}, nil
}

// referencesOutside are the reported references that do not fall inside the
// declaration itself: its own name and anything recursive are not reasons to
// keep it.
func referencesOutside(workspace *workspacecore.Workspace, locations []any, path string, startLine, endLine int) []string {
	var outside []string
	for _, raw := range locations {
		location, _ := raw.(map[string]any)
		file := workspacePath(workspace, fmt.Sprint(location["file"]))
		line := argInt(location, "line", 0)
		if file == path && line >= startLine && line <= endLine {
			continue
		}
		outside = append(outside, fmt.Sprintf("%s:%d", file, line))
	}
	return outside
}

func boundedReferences(outside []string) []string {
	if len(outside) <= maxReportedReferences {
		return outside
	}
	return append(outside[:maxReportedReferences:maxReportedReferences],
		fmt.Sprintf("and %d more", len(outside)-maxReportedReferences))
}

// declarationLines is the file and the line span a plan target covers.
func declarationLines(workspace *workspacecore.Workspace, operation workspacecore.PlanOperation) (string, int, int, error) {
	if operation.Target == nil {
		return "", 0, 0, fmt.Errorf("%s requires target", operation.Kind)
	}
	var path string
	var start, end int
	switch {
	case operation.Target.SymbolLocator != nil:
		record, err := workspace.ResolveSymbolLocator(operation.Target.SymbolLocator.Path, operation.Target.SymbolLocator.NamePath)
		if err != nil {
			return "", 0, 0, err
		}
		path, start, end = record.Locator.Path, record.Locator.ByteStart, record.Locator.ByteEnd
	case operation.Target.Handle != "":
		resolution, err := workspace.ResolveHandle(operation.Target.Handle)
		if err != nil {
			return "", 0, 0, err
		}
		handle, err := resolution.RangeHandle()
		if err != nil {
			return "", 0, 0, err
		}
		path, start, end = handle.Path, handle.ByteStart, handle.ByteEnd
	case operation.Target.FileRange != nil:
		path, start, end = operation.Target.FileRange.Path, operation.Target.FileRange.ByteStart, operation.Target.FileRange.ByteEnd
	default:
		return "", 0, 0, fmt.Errorf("%s requires target", operation.Kind)
	}
	read, err := workspace.Read(path)
	if err != nil {
		return "", 0, 0, err
	}
	if start < 0 || end > len(read.Content) || end < start {
		return "", 0, 0, fmt.Errorf("the declaration is outside the current document")
	}
	return path, lineOf(read.Content, start), lineOf(read.Content, end), nil
}

func lineOf(content []byte, offset int) int {
	return bytes.Count(content[:offset], []byte{'\n'}) + 1
}

func editStart(raw any) int {
	edit, _ := raw.(map[string]any)
	return argInt(edit, "byte_start", -1)
}

// callProvider runs one canonical provider operation for the workspace.
func (h *Handlers) callProvider(ctx context.Context, requestID string, workspace *workspacecore.Workspace, operation string, arguments map[string]any) (any, error) {
	backend, err := h.pool.Canonical(ctx, workspace)
	if err != nil {
		return nil, err
	}
	return providerpool.CallCanonical(ctx, requestID, workspace, backend, operation, arguments)
}
