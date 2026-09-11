package bridge

import (
	"context"
	"errors"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *toolHandlers) edit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	operation, ok := arguments["operation"].(map[string]any)
	if !ok || operation["kind"] != "replace_range" {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "operation_unavailable", "S07 direct edit supports only replace_range", map[string]any{})
	}
	target, _ := operation["target"].(map[string]any)
	var handle workspacecore.RangeHandle
	var resolution *workspacecore.HandleResolution
	var err error
	if opaque, _ := target["handle"].(string); opaque != "" {
		resolved, resolveErr := workspace.ResolveHandle(workspacecore.HandleID(opaque))
		if resolveErr != nil {
			result := mcpapi.Failure(requestID, workspace, "handle_resolve_failed", resolveErr)
			result["next"] = []any{
				map[string]any{"tool": "workspace_inspect", "view": "status", "action": "confirm_current_revision"},
				map[string]any{"tool": "search", "action": "repeat_source_query_and_retry_with_fresh_handle", "expired_handle": opaque},
			}
			return result
		}
		resolution = &resolved
		if resolved.Status == workspacecore.ResolutionConflicted {
			return modernHandleConflict(requestID, workspace, resolved)
		}
		handle, err = resolved.RangeHandle()
	} else {
		fileRange, _ := target["file_range"].(map[string]any)
		handle, err = decodeRangeHandle(fileRange)
	}
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "invalid_target", err)
	}
	content, _ := operation["content"].(string)
	var change workspacecore.TextChange
	var after workspacecore.DocumentSnapshot
	preview, _ := arguments["preview_only"].(bool)
	if preview {
		change, err = workspace.PreviewReplace(workspace.Identity().ID, handle, []byte(content))
	} else {
		change, after, err = workspace.ApplyReplace(workspace.Identity().ID, handle, []byte(content))
		if err == nil {
			change.Workspace = workspace.Identity()
		}
	}
	if err != nil {
		var conflict *workspacecore.Conflict
		if errors.As(err, &conflict) {
			result := mcpapi.Envelope(requestID, workspace, "conflict", string(conflict.Code), conflict.Error(), map[string]any{
				"target": handle, "current_revision": fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
			})
			result["next"] = []any{map[string]any{"tool": "read", "action": "refresh_path", "path": handle.Path}, map[string]any{"tool": "search", "action": "relocate_target", "path": handle.Path}}
			return result
		}
		return mcpapi.Failure(requestID, workspace, "edit_failed", err)
	}
	summary, outcome := "Guarded range preview is ready; canonical bytes unchanged", "ok"
	data := map[string]any{
		"change": mcpapi.CompactTextChange(change), "resolution": resolution, "tool_delta": []any{},
		"diagnostic_delta": map[string]any{"new": []any{}, "resolved": []any{}},
		"from_revision":    fmt.Sprintf("wsrev_%d", change.Before.Workspace.StateSeq),
		"changed_paths":    []string{change.Diff.Path}, "canonical_changed": !preview,
	}
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if !preview {
		checkpoint := mcpapi.Envelope(requestID, workspace, "provisional", "",
			"Guarded range edit applied; canonical receipt persisted before semantic diagnostics", data)
		checkpoint["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
		checkpoint["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"},
			map[string]any{"tool": "verify_run", "action": "retry_diagnostics_for_exact_revision", "revision_or_transaction": data["revision"]},
		}
		if persistErr := checkpointStatefulReceipt(ctx, checkpoint); persistErr != nil {
			result := mcpapi.Envelope(requestID, workspace, "provisional", "mutation_receipt_persist_failed",
				"Guarded range edit applied, but its recovery receipt could not be persisted", data)
			result["warnings"] = []string{persistErr.Error()}
			result["next"] = []any{map[string]any{"tool": "revision_diff", "action": "inspect_current_revision_before_retry"}}
			return result
		}
	}
	evidenceIDs := make([]string, 0)
	diagnosticRecovery := false
	if !preview {
		revision := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
		data["document_revision"] = after.Revision
		verification := map[string]any{"confidence": "unavailable", "reasons": []string{"semantic_provider_unavailable"}}
		backend, providerErr := h.pool.resync(ctx, workspace)
		if providerErr == nil {
			report, evidenceErr := recordProviderDiagnostics(ctx, workspace, backend, []workspacecore.PlanStageFile{{
				Path: change.Diff.Path, Before: change.Diff.Before, After: change.Diff.After,
				BeforeExists: true, AfterExists: true,
			}}, revision, "edit_"+requestID)
			if evidenceErr == nil {
				evidenceIDs = append(evidenceIDs, report.EvidenceIDs...)
				data["diagnostic_delta"] = map[string]any{"new": report.New, "resolved": report.Resolved}
				verification = map[string]any{
					"confidence": report.Confidence,
					"reasons":    mcpapi.NonNilStrings(report.ProvisionalReasons),
				}
				if report.Confidence == workspacecore.ConfidenceAuthoritative || report.Confidence == workspacecore.ConfidenceCorroborated {
					outcome = "ok"
					summary = "Guarded range edit applied; current semantic diagnostics captured"
				} else {
					outcome = "provisional"
					summary = "Guarded range edit applied; semantic diagnostics remain incomplete"
					diagnosticRecovery = true
				}
			} else {
				providerErr = evidenceErr
			}
		}
		if providerErr != nil {
			outcome = "provisional"
			summary = "Guarded range edit applied; semantic diagnostic refresh failed"
			verification = map[string]any{"confidence": "unavailable", "reasons": []string{providerErr.Error()}}
			diagnosticRecovery = true
		}
		data["verification"] = verification
	}
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if resolution != nil && resolution.Status == workspacecore.ResolutionRelocated {
		if preview {
			summary = "Guarded range preview is ready after safely relocating the stale target; canonical bytes unchanged"
		} else if outcome == "ok" {
			summary = "Guarded range edit applied after safely relocating the stale target; current semantic diagnostics captured"
		} else {
			summary = "Guarded range edit applied after safely relocating the stale target; semantic diagnostics remain incomplete"
		}
	}
	result := mcpapi.Envelope(requestID, workspace, outcome, "", summary, data)
	result["evidence"] = map[string]any{"ids": mcpapi.NonNilStrings(evidenceIDs), "truncated": false}
	if diagnosticRecovery {
		result["next"] = []any{
			map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"},
			map[string]any{"tool": "verify_run", "action": "retry_diagnostics_for_exact_revision", "revision_or_transaction": data["revision"]},
		}
	}
	return result
}
