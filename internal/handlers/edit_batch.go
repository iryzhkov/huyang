package handlers

import (
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editOperations is edit_apply with an operations list: several
// replace_literal and create_file operations applied in order in one call,
// with one formatter pass, one receipt and one diagnostics refresh at the
// end. Each operation is located against the bytes the previous ones left,
// so two edits to the same file compose. A refusal stops the list; the
// operations before it stay applied and the reply says how many.
func (h *Handlers) editOperations(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	operations := mcpapi.AnySlice(arguments["operations"])
	if len(operations) == 0 {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "operations must be a non-empty array of replace_literal or create_file operations", map[string]any{})
	}
	flags := map[string]any{"preview_only": arguments["preview_only"], "verbose": arguments["verbose"], "format": arguments["format"]}
	merged := appliedEdit{fromStateSeq: workspace.Identity().StateSeq, revisions: map[string]workspacecore.RevisionID{}}
	var base editRequest
	for index, raw := range operations {
		operation, _ := raw.(map[string]any)
		flags["operation"] = operation
		request, err := decodeEditRequest(flags)
		if err != nil {
			return operationFailure(mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", err.Error(), map[string]any{}), index, merged)
		}
		if request.Kind == "replace_range" {
			return operationFailure(mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "operations accepts replace_literal and create_file; use a single operation for replace_range", map[string]any{}), index, merged)
		}
		base = request
		var applied appliedEdit
		var failure map[string]any
		if request.Kind == "create_file" {
			applied, failure = applyCreateFile(requestID, workspace, request)
			// A new file is a changed path, not a replacement.
			applied.replacements = 0
		} else {
			applied, failure = applyLiteral(requestID, workspace, request)
		}
		if failure != nil {
			return operationFailure(failure, index, merged)
		}
		mergeApplied(&merged, applied)
	}
	noun := "file"
	if len(merged.files) != 1 {
		noun = "files"
	}
	merged.summary = fmt.Sprintf("Applied %d operations: %d replacement(s) in %d %s", len(operations), merged.replacements, len(merged.files), noun)
	if base.Preview {
		merged.summary = fmt.Sprintf("Preview of %d operations; canonical bytes unchanged", len(operations))
	}
	return h.finishEdit(ctx, requestID, workspace, base, merged)
}

// operationFailure labels a refusal with the operation that caused it and
// with how many operations before it were already applied.
func operationFailure(failure map[string]any, index int, merged appliedEdit) map[string]any {
	failure["summary"] = fmt.Sprintf("operation %d: %v", index, failure["summary"])
	data, _ := failure["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
		failure["data"] = data
	}
	data["failed_operation"] = index
	data["applied_operations"] = index
	if index > 0 {
		data["changed_paths"] = changedPaths(merged.files)
		warnings, _ := failure["warnings"].([]string)
		failure["warnings"] = append(warnings, fmt.Sprintf("operations 0 to %d were applied and stay applied", index-1))
	}
	return failure
}

// mergeApplied folds one applied operation into the list's record: files
// merge by path (first before, last after), counts and lists accumulate.
func mergeApplied(merged *appliedEdit, applied appliedEdit) {
	for _, file := range applied.files {
		found := false
		for index := range merged.files {
			if merged.files[index].Path == file.Path {
				merged.files[index].After, merged.files[index].AfterExists = file.After, file.AfterExists
				merged.files[index].Patch += file.Patch
				found = true
				break
			}
		}
		if !found {
			merged.files = append(merged.files, file)
		}
	}
	merged.patches = append(merged.patches, applied.patches...)
	for path, revision := range applied.revisions {
		merged.revisions[path] = revision
	}
	merged.replacements += applied.replacements
	merged.warnings = append(merged.warnings, applied.warnings...)
	merged.locations = append(merged.locations, applied.locations...)
}

func changedPaths(files []workspacecore.PlanStageFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
