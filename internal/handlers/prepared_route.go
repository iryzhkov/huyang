package handlers

import (
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/providerpool"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// preparedDiagnosticBudget bounds how many staged files one call diagnoses;
// a plan that touches more than this is asked about a file at a time.
const preparedDiagnosticBudget = 10

// preparedCapableTool reports whether a tool can answer about a prepared
// revision. The others read, write or verify the canonical workspace and say
// so rather than quietly answering about the wrong bytes.
func preparedCapableTool(name string) bool {
	switch name {
	case "read", "navigate", "diagnostics", "code_actions":
		return true
	}
	return false
}

// routePrepared answers one call against a prepared revision.
func (h *Handlers) routePrepared(ctx context.Context, requestID, name string, workspace *workspacecore.Workspace, selector preparedSelector, arguments map[string]any) map[string]any {
	view, failure := h.resolvePrepared(requestID, workspace, selector)
	if failure != nil {
		return failure
	}
	switch name {
	case "read":
		request, ok := decodeReadRequest(arguments)
		if !ok {
			return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "read requires target or targets", map[string]any{})
		}
		return h.readPrepared(requestID, workspace, view, request)
	case "navigate":
		return h.navigatePrepared(ctx, requestID, workspace, view, arguments)
	case "diagnostics":
		return h.diagnosticsPrepared(ctx, requestID, workspace, view, arguments)
	case "code_actions":
		return h.codeActionsPrepared(ctx, requestID, workspace, view, arguments)
	}
	return mcpapi.Envelope(requestID, workspace, "unavailable", "prepared_tool_unsupported",
		fmt.Sprintf("%s answers about the canonical workspace only", name), map[string]any{})
}

// navigatePrepared asks the sandbox's language server where something is in
// the staged bytes.
func (h *Handlers) navigatePrepared(ctx context.Context, requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, arguments map[string]any) map[string]any {
	relation, _ := arguments["relation"].(string)
	target, _ := arguments["target"].(map[string]any)
	if target == nil {
		target = map[string]any{}
	}
	if symbol, ok := arguments["symbol"].(string); ok && symbol != "" {
		target["symbol"] = symbol
		if target["path"] == nil {
			return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target",
				"a prepared navigation needs the file the symbol is in; the canonical symbol index describes other bytes",
				map[string]any{"revision": view.PreparedRevision})
		}
	}
	providerArguments, err := preparedProviderTarget(view, workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	value, failure := h.callPrepared(ctx, requestID, workspace, view, relation, providerArguments)
	if failure != nil {
		return failure
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "",
		fmt.Sprintf("%s resolved in %s", relation, view.PreparedRevision), map[string]any{
			"navigation": value, "revision": view.PreparedRevision, "plan_id": view.PlanID,
		})
}

// diagnosticsPrepared reports what the language server makes of the staged
// bytes, which is the question a prepared revision exists to answer.
func (h *Handlers) diagnosticsPrepared(ctx context.Context, requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, arguments map[string]any) map[string]any {
	// The language server answers about a file, so "the diagnostics of this
	// proposal" means the files the proposal staged, unless one is named.
	files := view.Files
	if target, ok := arguments["target"].(map[string]any); ok {
		if path, _ := target["path"].(string); path != "" {
			files = []string{workspacePath(workspace, path)}
		}
	}
	if len(files) == 0 {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "prepared_files_unknown",
			"this preparation staged no readable file to diagnose", map[string]any{"revision": view.PreparedRevision})
	}
	if len(files) > preparedDiagnosticBudget {
		files = files[:preparedDiagnosticBudget]
	}
	reports := make([]any, 0, len(files))
	var findings []workspacecore.ComparableFinding
	for _, path := range files {
		absolute, err := preparedPath(view, path)
		if err != nil {
			return mcpapi.Failure(requestID, workspace, "invalid_target", err)
		}
		value, failure := h.callPrepared(ctx, requestID+"_"+path, workspace, view, "diagnostics", map[string]any{
			"root": view.Tree, "file": absolute,
		})
		if failure != nil {
			return failure
		}
		reports = append(reports, map[string]any{"path": path, "report": value})
		findings = append(findings, comparableFindings(path, value)...)
	}
	data := map[string]any{
		"diagnostics": reports, "revision": view.PreparedRevision, "plan_id": view.PlanID,
		"plan_revision": view.PlanRevision,
	}
	summary := fmt.Sprintf("Diagnostics for %d file(s) at %s", len(reports), view.PreparedRevision)
	// What the proposal did, rather than what is wrong with the code: the
	// findings that are new against the canonical report, who caused each,
	// and what it resolved.
	if delta := h.preparedDelta(ctx, requestID, workspace, view, findings, files); delta != nil {
		data["delta"] = delta
		summary = fmt.Sprintf("%d new, %d resolved, %d unchanged at %s",
			len(delta.New), len(delta.Resolved), delta.UnchangedCount, view.PreparedRevision)
		if !delta.BaselineComplete {
			summary += "; the baseline was incomplete, so what is new cannot be established"
		}
	}
	return mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
}

// codeActionsPrepared lists what the server would offer for the staged bytes.
// Listing is read-only: selecting one is an operation added to the plan,
// which replaces the preparation rather than editing it in place.
func (h *Handlers) codeActionsPrepared(ctx context.Context, requestID string, workspace *workspacecore.Workspace, view providerpool.PreparedView, arguments map[string]any) map[string]any {
	target, _ := arguments["target"].(map[string]any)
	if target == nil {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "code_actions requires target", map[string]any{})
	}
	providerArguments, err := preparedProviderTarget(view, workspace, target)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "semantic_target_invalid", err)
	}
	value, failure := h.callPrepared(ctx, requestID, workspace, view, "code_actions", providerArguments)
	if failure != nil {
		return failure
	}
	result := mcpapi.Envelope(requestID, workspace, "ok", "",
		fmt.Sprintf("Code actions for %s", view.PreparedRevision), map[string]any{
			"code_actions": value, "revision": view.PreparedRevision, "plan_id": view.PlanID,
		})
	result["next"] = []any{map[string]any{
		"tool": "change_plan", "action": "edit", "plan_id": view.PlanID, "plan_revision": view.PlanRevision,
		"note": "Applying one of these adds an operation to the plan and needs a fresh prepare; a reviewed preparation is never edited in place.",
	}}
	return result
}
