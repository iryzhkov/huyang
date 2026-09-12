package handlers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editRequest is the decoded edit_apply call: one replace_range operation
// addressed by an opaque handle or an exact file range, one replace_literal
// operation addressed by the text it replaces, one create_file (optionally
// a guarded whole-file replace), or one file-lifecycle operation
// (move_file, copy_file, delete_file).
type editRequest struct {
	Kind          string
	Preview       bool
	Verbose       bool
	Format        bool
	Content       string
	Handle        string
	Range         map[string]any
	Path          string
	Old           string
	New           string
	ExpectedCount int
	// File-lifecycle fields.
	From                  string
	To                    string
	RevisionID            string
	DestinationRevisionID string
	ExpectedSHA256        string
	Replace               bool
}

// optionalString renders an optional argument that may be a string or a
// typed string value; nil is the empty string.
func optionalString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// editKinds lists every operation kind edit_apply accepts.
var editKinds = map[string]bool{
	"replace_range": true, "replace_literal": true, "create_file": true,
	"move_file": true, "copy_file": true, "delete_file": true,
}

func decodeEditRequest(arguments map[string]any) (editRequest, error) {
	operation, ok := arguments["operation"].(map[string]any)
	if !ok {
		return editRequest{}, errors.New("operation is required")
	}
	request := editRequest{}
	request.Kind, _ = operation["kind"].(string)
	if !editKinds[request.Kind] {
		return editRequest{}, fmt.Errorf("edit_apply supports replace_literal, create_file, move_file, copy_file, delete_file and replace_range, not %q", request.Kind)
	}
	request.From, _ = operation["from"].(string)
	request.To, _ = operation["to"].(string)
	// Revisions arrive as strings over MCP and as RevisionID values from
	// in-process callers; both print the same.
	request.RevisionID = optionalString(operation["revision_id"])
	request.DestinationRevisionID = optionalString(operation["destination_revision_id"])
	request.ExpectedSHA256 = optionalString(operation["expected_sha256"])
	request.Replace, _ = operation["replace"].(bool)
	target, _ := operation["target"].(map[string]any)
	request.Preview, _ = arguments["preview_only"].(bool)
	request.Verbose, _ = arguments["verbose"].(bool)
	request.Format = true
	if value, ok := arguments["format"].(bool); ok {
		request.Format = value
	}
	request.Content, _ = operation["content"].(string)
	request.Handle, _ = target["handle"].(string)
	request.Range, _ = target["file_range"].(map[string]any)
	request.Path, _ = operation["path"].(string)
	request.Old, _ = operation["old"].(string)
	request.New, _ = operation["new"].(string)
	request.ExpectedCount = argInt(operation, "expected_count", 1)
	return request, nil
}

// appliedEdit is what every edit kind produces before the shared
// checkpoint, diagnostic refresh and response shaping.
type appliedEdit struct {
	files        []workspacecore.PlanStageFile
	patches      []string
	revisions    map[string]workspacecore.RevisionID
	fromStateSeq uint64
	replacements int
	summary      string
	warnings     []string
	// locations are the path:line:column of each literal replacement, the
	// cheap substitute for a patch the agent already knows the content of
	locations []string
	// format is what the language formatter did after the edit, when it ran
	format map[string]any
	// noFormat names files the formatter must leave alone: a moved, copied
	// or deleted file keeps its exact bytes
	noFormat map[string]bool
	// git is the tracked state of each lifecycle path, when in a repository
	git map[string]string
	// source describes what a copy read, so the receipt records it
	source map[string]any
	// next are the steps the agent takes after a successful lifecycle edit
	next []any
	// verbose detail, only reported on request
	change     *workspacecore.TextChange
	resolution *workspacecore.HandleResolution
}

func (h *Handlers) edit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if _, listed := arguments["operations"]; listed {
		return h.editOperations(ctx, requestID, workspace, arguments)
	}
	request, err := decodeEditRequest(arguments)
	if err != nil {
		return mcpapi.Envelope(requestID, workspace, "unavailable", "operation_unavailable", err.Error(), map[string]any{})
	}
	switch request.Kind {
	case "replace_literal":
		return h.editLiteral(ctx, requestID, workspace, request)
	case "create_file":
		return h.editCreateFile(ctx, requestID, workspace, request)
	case "move_file", "copy_file", "delete_file":
		applied, failure := applyLifecycle(requestID, workspace, request)
		if failure != nil {
			return failure
		}
		return h.finishEdit(ctx, requestID, workspace, request, applied)
	}
	return h.editRange(ctx, requestID, workspace, request)
}

// applyLifecycle dispatches one file-lifecycle kind; shared by the single
// call and the operations list.
func applyLifecycle(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	switch request.Kind {
	case "move_file":
		return applyMoveFile(requestID, workspace, request)
	case "copy_file":
		return applyCopyFile(requestID, workspace, request)
	default:
		return applyDeleteFile(requestID, workspace, request)
	}
}

// editRange applies one guarded range replacement.
func (h *Handlers) editRange(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request editRequest) map[string]any {
	handle, resolution, failure := resolveEditTarget(requestID, workspace, request)
	if failure != nil {
		return failure
	}
	change, _, err := applyEdit(workspace, handle, request)
	if err != nil {
		return editFailure(requestID, workspace, handle, err)
	}
	applied := appliedEdit{
		files:   []workspacecore.PlanStageFile{{Path: change.Diff.Path, Before: change.Diff.Before, After: change.Diff.After, BeforeExists: true, AfterExists: true, Patch: change.Diff.Patch}},
		patches: []string{change.Diff.Patch}, fromStateSeq: change.Before.Workspace.StateSeq, replacements: 1,
		change: &change, resolution: resolution,
	}
	if !request.Preview {
		if snapshot, snapErr := workspace.Snapshot(change.Diff.Path, workspacecore.ProviderLayer{}); snapErr == nil {
			applied.revisions = map[string]workspacecore.RevisionID{change.Diff.Path: snapshot.Revision}
		}
	}
	relocated := resolution != nil && resolution.Status == workspacecore.ResolutionRelocated
	applied.summary = editSummary(relocated, request.Preview)
	return h.finishEdit(ctx, requestID, workspace, request, applied)
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
			map[string]any{"tool": "edit_apply", "action": "retry_as_replace_literal_with_the_old_text", "expired_handle": request.Handle},
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
		map[string]any{"tool": "edit_apply", "action": "retry_as_replace_literal_with_the_old_text", "path": handle.Path},
		map[string]any{"tool": "read", "action": "refresh_path", "path": handle.Path},
	}
	return result
}

// finishEdit shapes the response every edit kind shares: the compact data,
// the persisted receipt, and the post-edit diagnostics of the changed files.
func (h *Handlers) finishEdit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request editRequest, applied appliedEdit) map[string]any {
	if !request.Preview && request.Format {
		formatEdited(workspace, &applied)
	}
	data := compactEditData(workspace, request, applied)
	if request.Preview {
		result := mcpapi.Envelope(requestID, workspace, "ok", "", applied.summary, data)
		result["warnings"] = mcpapi.NonNilStrings(applied.warnings)
		return result
	}
	if failure := h.checkpointEdit(ctx, requestID, workspace, data); failure != nil {
		return failure
	}
	outcome, summary := "ok", applied.summary
	var evidenceIDs []string
	if semanticEditTargets(workspace, applied.files) {
		var detail string
		outcome, detail, evidenceIDs = h.refreshEditDiagnostics(ctx, requestID, workspace, applied.files, data)
		summary += "; " + detail
	}
	data["revision"] = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	result := mcpapi.Envelope(requestID, workspace, outcome, "", summary, data)
	result["warnings"] = mcpapi.NonNilStrings(applied.warnings)
	if len(evidenceIDs) > 0 {
		result["evidence"] = map[string]any{"ids": evidenceIDs, "truncated": false}
	}
	if outcome != "ok" {
		result["next"] = editDiagnosticRecovery(data["revision"])
	} else if len(applied.next) > 0 {
		result["next"] = applied.next
	}
	return result
}

// semanticEditTargets reports whether the edited files can have semantic
// diagnostics at all: a project workspace and at least one source file. A
// note, a config file or a lone document outside a repository gets a plain
// ok instead of a provisional verdict about diagnostics that cannot exist.
func semanticEditTargets(workspace *workspacecore.Workspace, files []workspacecore.PlanStageFile) bool {
	if workspace.Identity().Kind != workspacecore.KindProject {
		return false
	}
	for _, file := range files {
		if workspacecore.IsSemanticSource(file.Path) {
			return true
		}
	}
	return false
}

// compactEditData is the default edit response: what changed, the new
// revision, one patch, and nothing the agent did not ask for. verbose adds
// the full change record and the handle resolution.
func compactEditData(workspace *workspacecore.Workspace, request editRequest, applied appliedEdit) map[string]any {
	changed := make([]string, 0, len(applied.files))
	for _, file := range applied.files {
		changed = append(changed, file.Path)
	}
	data := map[string]any{
		"changed_paths": changed, "canonical_changed": !request.Preview,
		"diffs":         editDiffs(applied, request.Verbose || request.Preview),
		"from_revision": fmt.Sprintf("wsrev_%d", applied.fromStateSeq),
		"revision":      fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
	}
	if applied.replacements != 1 {
		data["replacements"] = applied.replacements
	}
	if len(applied.locations) > 0 {
		data["locations"] = applied.locations
	}
	if applied.format != nil {
		data["format"] = applied.format
	}
	if len(applied.git) > 0 {
		data["git"] = applied.git
	}
	if applied.source != nil {
		data["source"] = applied.source
	}
	switch len(applied.revisions) {
	case 0:
	case 1:
		for _, revision := range applied.revisions {
			data["document_revision"] = revision
		}
	default:
		data["document_revisions"] = applied.revisions
	}
	if applied.resolution != nil && applied.resolution.Status == workspacecore.ResolutionRelocated && applied.resolution.Current != nil {
		data["relocated"] = map[string]any{
			"from_byte": applied.resolution.Original.ByteStart, "to_byte": applied.resolution.Current.ByteStart,
			"code": applied.resolution.Code,
		}
	}
	if request.Verbose {
		if applied.change != nil {
			data["change"] = mcpapi.CompactTextChange(*applied.change)
		}
		if applied.resolution != nil {
			data["resolution"] = applied.resolution
		}
		data["tool_delta"] = []any{}
	}
	return data
}

// checkpointEdit persists the canonical mutation receipt before the
// post-mutation diagnostics run, so a lost response can still be replayed.
func (h *Handlers) checkpointEdit(ctx context.Context, requestID string, workspace *workspacecore.Workspace, data map[string]any) map[string]any {
	checkpoint := mcpapi.Envelope(requestID, workspace, "provisional", "",
		"Edit applied; canonical receipt persisted before semantic diagnostics", data)
	checkpoint["evidence"] = map[string]any{"ids": []string{}, "truncated": false}
	checkpoint["next"] = editDiagnosticRecovery(data["revision"])
	persistErr := checkpointStatefulReceipt(ctx, checkpoint)
	if persistErr == nil {
		return nil
	}
	result := mcpapi.Envelope(requestID, workspace, "provisional", "mutation_receipt_persist_failed",
		"Edit applied, but its recovery receipt could not be persisted", data)
	result["warnings"] = []string{persistErr.Error()}
	result["next"] = []any{map[string]any{"tool": "revision_diff", "action": "inspect_current_revision_before_retry"}}
	return result
}

// refreshEditDiagnostics resyncs the canonical provider and records the
// diagnostics of the edited documents. It fills data's diagnostic_delta and
// verification and returns the outcome the evidence supports, the wording
// for the summary, and the evidence IDs.
func (h *Handlers) refreshEditDiagnostics(ctx context.Context, requestID string, workspace *workspacecore.Workspace, files []workspacecore.PlanStageFile, data map[string]any) (string, string, []string) {
	revision := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	backend, err := h.pool.Resync(ctx, workspace)
	var report workspacecore.DiagnosticReport
	if err == nil {
		report, err = providerpool.RecordDiagnostics(ctx, workspace, backend, files, revision, "edit_"+requestID)
	}
	if err != nil {
		data["verification"] = map[string]any{"confidence": "unavailable", "reasons": []string{err.Error()}}
		return "provisional", "semantic diagnostic refresh failed", nil
	}
	delta := compactDiagnosticDelta(workspace, report)
	if len(delta["new"].([]map[string]any)) > 0 || len(delta["resolved"].([]string)) > 0 {
		data["diagnostic_delta"] = delta
	}
	if report.Confidence == workspacecore.ConfidenceAuthoritative || report.Confidence == workspacecore.ConfidenceCorroborated {
		// The verdict is authoritative: the summary says so and the
		// verification block would only repeat it.
		return "ok", diagnosticSummary(report), report.EvidenceIDs
	}
	data["verification"] = map[string]any{
		"confidence": report.Confidence,
		"reasons":    mcpapi.NonNilStrings(report.ProvisionalReasons),
	}
	return "provisional", "semantic diagnostics remain incomplete", report.EvidenceIDs
}

// diagnosticSummary words the post-edit diagnostic outcome so the agent
// can read the verdict without opening the delta.
func diagnosticSummary(report workspacecore.DiagnosticReport) string {
	current := 0
	for _, item := range report.New {
		if item.Status != workspacecore.DiagnosticStatusStale {
			current++
		}
	}
	switch {
	case current == 0 && len(report.Resolved) == 0:
		return "no new diagnostics"
	case current == 0:
		return fmt.Sprintf("%d diagnostic(s) resolved, none new", len(report.Resolved))
	default:
		return fmt.Sprintf("%d new diagnostic(s), %d resolved", current, len(report.Resolved))
	}
}

// compactDiagnosticDelta keeps what an agent acts on from each new finding:
// where it is, how bad it is and what it says. Resolved findings collapse
// to their IDs.
func compactDiagnosticDelta(workspace *workspacecore.Workspace, report workspacecore.DiagnosticReport) map[string]any {
	root := workspace.Identity().Root + "/"
	newItems := make([]map[string]any, 0, len(report.New))
	for _, item := range report.New {
		if item.Status == workspacecore.DiagnosticStatusStale {
			continue
		}
		compact := map[string]any{
			"id": item.ID, "severity": item.Finding.Severity, "message": item.Finding.Message,
			"path": strings.TrimPrefix(item.Document, root), "line": item.Finding.Range.StartLine + 1,
		}
		if item.Finding.Code != "" {
			compact["code"] = item.Finding.Code
		}
		if item.Producer != "" {
			compact["producer"] = item.Producer
		}
		newItems = append(newItems, compact)
	}
	resolved := make([]string, 0, len(report.Resolved))
	for _, item := range report.Resolved {
		resolved = append(resolved, item.ID)
	}
	return map[string]any{"new": newItems, "resolved": resolved}
}

// editSummary words a range edit: preview or applied, relocated or exact.
func editSummary(relocated, preview bool) string {
	switch {
	case preview && relocated:
		return "Guarded range preview is ready after safely relocating the stale target; canonical bytes unchanged"
	case preview:
		return "Guarded range preview is ready; canonical bytes unchanged"
	case relocated:
		return "Guarded range edit applied after safely relocating the stale target"
	default:
		return "Guarded range edit applied"
	}
}

func editDiagnosticRecovery(revision any) []any {
	return []any{
		map[string]any{"tool": "language_server_status", "action": "inspect_attachment_and_install_options"},
		map[string]any{"tool": "verify_run", "action": "retry_diagnostics_for_exact_revision", "revision_or_transaction": revision},
	}
}
