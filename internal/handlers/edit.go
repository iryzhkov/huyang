package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editRequest is the decoded edit_apply call: one replace_range operation
// addressed by an opaque handle or an exact file range.
type editRequest struct {
	Preview bool
	Content string
	Handle  string
	Range   map[string]any
}

func decodeEditRequest(arguments map[string]any) (editRequest, error) {
	operation, ok := arguments["operation"].(map[string]any)
	if !ok || operation["kind"] != "replace_range" {
		return editRequest{}, errors.New("S07 direct edit supports only replace_range")
	}
	target, _ := operation["target"].(map[string]any)
	request := editRequest{}
	request.Preview, _ = arguments["preview_only"].(bool)
	request.Content, _ = operation["content"].(string)
	request.Handle, _ = target["handle"].(string)
	request.Range, _ = target["file_range"].(map[string]any)
	return request, nil
}

func (h *Handlers) edit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	request, err := decodeEditRequest(arguments)
	if err != nil {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "operation_unavailable", err.Error(), map[string]any{})
	}
	handle, resolution, failure := resolveEditTarget(requestID, workspace, request)
	if failure != nil {
		return failure
	}
	change, after, err := applyEdit(workspace, handle, request)
	if err != nil {
		return editFailure(requestID, workspace, handle, err)
	}
	data := map[string]any{
		"change": mcpapi.CompactTextChange(change), "resolution": resolution, "tool_delta": []any{},
		"diagnostic_delta": map[string]any{"new": []any{}, "resolved": []any{}},
		"from_revision":    fmt.Sprintf("wsrev_%d", change.Before.Workspace.StateSeq),
		"changed_paths":    []string{change.Diff.Path}, "canonical_changed": !request.Preview,
		"revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
	}
	if request.Preview {
		result := mcpapi.Envelope(requestID, workspace, "ok", "", editSummary(resolution, true, "ok"), data)
		result["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
		return result
	}
	if failure := h.checkpointEdit(ctx, requestID, workspace, data); failure != nil {
		return failure
	}
	data["document_revision"] = after.Revision
	outcome, summary, evidenceIDs := h.refreshEditDiagnostics(ctx, requestID, workspace, change, data)
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if resolution != nil && resolution.Status == workspacecore.ResolutionRelocated {
		summary = editSummary(resolution, false, outcome)
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, "", summary, data)
	result["evidence"] = map[string]any{"ids": mcpapi.NonNilStrings(evidenceIDs), "truncated": false}
	if outcome != "ok" {
		result["next"] = editDiagnosticRecovery(data["revision"])
	}
	return result
}

// resolveEditTarget turns the request's handle or file range into the exact
// range to replace. A handle that cannot be resolved or that conflicts is
// answered directly; the returned resolution is nil for a file range.
func resolveEditTarget(requestID string, workspace *workspacecore.Workspace, request editRequest) (workspacecore.RangeHandle, *workspacecore.HandleResolution, map[string]any) {
	if request.Handle == "" {
		handle, err := decodeRangeHandle(request.Range)
		if err != nil {
			return handle, nil, mcpapi.Failure(requestID, workspace, "invalid_target", err)
		}
		return handle, nil, nil
	}
	resolved, err := workspace.ResolveHandle(workspacecore.HandleID(request.Handle))
	if err != nil {
		result := mcpapi.Failure(requestID, workspace, "handle_resolve_failed", err)
		result["next"] = []any{
			map[string]any{"tool": "workspace_inspect", "view": "status", "action": "confirm_current_revision"},
			map[string]any{"tool": "search", "action": "repeat_source_query_and_retry_with_fresh_handle", "expired_handle": request.Handle},
		}
		return workspacecore.RangeHandle{}, nil, result
	}
	if resolved.Status == workspacecore.ResolutionConflicted {
		return workspacecore.RangeHandle{}, nil, modernHandleConflict(requestID, workspace, resolved)
	}
	handle, err := resolved.RangeHandle()
	if err != nil {
		return handle, nil, mcpapi.Failure(requestID, workspace, "invalid_target", err)
	}
	return handle, &resolved, nil
}

// applyEdit previews or applies the replacement against the canonical
// document; the snapshot is only meaningful for an applied edit.
func applyEdit(workspace *workspacecore.Workspace, handle workspacecore.RangeHandle, request editRequest) (workspacecore.TextChange, workspacecore.DocumentSnapshot, error) {
	if request.Preview {
		change, err := workspace.PreviewReplace(workspace.Identity().ID, handle, []byte(request.Content))
		return change, workspacecore.DocumentSnapshot{}, err
	}
	change, after, err := workspace.ApplyReplace(workspace.Identity().ID, handle, []byte(request.Content))
	if err == nil {
		change.Workspace = workspace.Identity()
	}
	return change, after, err
}

func editFailure(requestID string, workspace *workspacecore.Workspace, handle workspacecore.RangeHandle, err error) map[string]any {
	var conflict *workspacecore.Conflict
	if !errors.As(err, &conflict) {
		return mcpapi.Failure(requestID, workspace, "edit_failed", err)
	}
	result := mcpapi.Envelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{
		"target": handle, "current_revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
	})
	result["next"] = []any{
		map[string]any{"tool": "read", "action": "refresh_path", "path": handle.Path},
		map[string]any{"tool": "search", "action": "relocate_target", "path": handle.Path},
	}
	return result
}

// checkpointEdit persists the canonical mutation receipt before the
// post-mutation diagnostics run, so a lost response can still be replayed.
func (h *Handlers) checkpointEdit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, data map[string]any) map[string]any {
	checkpoint := mcpapi.Envelope(requestID, workspace, "provisional", "",
		"Guarded range edit applied; canonical receipt persisted before semantic diagnostics", data)
	checkpoint["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
	checkpoint["next"] = editDiagnosticRecovery(data["revision"])
	persistErr := checkpointStatefulReceipt(ctx, checkpoint)
	if persistErr == nil {
		return nil
	}
	result := mcpapi.Envelope(requestID, workspace, "provisional", "mutation_receipt_persist_failed",
		"Guarded range edit applied, but its recovery receipt could not be persisted", data)
	result["warnings"] = []string{persistErr.Error()}
	result["next"] = []any{map[string]any{"tool": "revision_diff", "action": "inspect_current_revision_before_retry"}}
	return result
}

// refreshEditDiagnostics resyncs the canonical provider and records the
// diagnostics of the edited document. It fills data's diagnostic_delta and
// verification and returns the outcome the evidence supports, the summary
// that words it, and the evidence IDs.
func (h *Handlers) refreshEditDiagnostics(ctx context.Context, requestID string, workspace *workspacecore.Workspace, change workspacecore.TextChange, data map[string]any) (string, string, []string) {
	revision := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	backend, err := h.pool.Resync(ctx, workspace)
	var report workspacecore.DiagnosticReport
	if err == nil {
		report, err = providerpool.RecordDiagnostics(ctx, workspace, backend, []workspacecore.PlanStageFile{{
			Path: change.Diff.Path, Before: change.Diff.Before, After: change.Diff.After,
			BeforeExists: true, AfterExists: true,
		}}, revision, "edit_"+requestID)
	}
	if err != nil {
		data["verification"] = map[string]any{"confidence": "unavailable", "reasons": []string{err.Error()}}
		return "provisional", "Guarded range edit applied; semantic diagnostic refresh failed", nil
	}
	data["diagnostic_delta"] = map[string]any{"new": report.New, "resolved": report.Resolved}
	data["verification"] = map[string]any{
		"confidence": report.Confidence,
		"reasons":    mcpapi.NonNilStrings(report.ProvisionalReasons),
	}
	if report.Confidence == workspacecore.ConfidenceAuthoritative || report.Confidence == workspacecore.ConfidenceCorroborated {
		return "ok", "Guarded range edit applied; current semantic diagnostics captured", report.EvidenceIDs
	}
	return "provisional", "Guarded range edit applied; semantic diagnostics remain incomplete", report.EvidenceIDs
}

// editSummary words a preview, or an applied edit whose stale target was
// relocated first; applied edits without relocation are worded by the
// diagnostic refresh.
func editSummary(resolution *workspacecore.HandleResolution, preview bool, outcome string) string {
	relocated := resolution != nil && resolution.Status == workspacecore.ResolutionRelocated
	switch {
	case preview && relocated:
		return "Guarded range preview is ready after safely relocating the stale target; canonical bytes unchanged"
	case preview:
		return "Guarded range preview is ready; canonical bytes unchanged"
	case outcome == "ok":
		return "Guarded range edit applied after safely relocating the stale target; current semantic diagnostics captured"
	default:
		return "Guarded range edit applied after safely relocating the stale target; semantic diagnostics remain incomplete"
	}
}

func editDiagnosticRecovery(revision any) []any {
	return []any{
		map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"},
		map[string]any{"tool": "verify_run", "action": "retry_diagnostics_for_exact_revision", "revision_or_transaction": revision},
	}
}
