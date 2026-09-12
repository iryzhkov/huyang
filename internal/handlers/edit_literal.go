package handlers

import (
	"context"
	"fmt"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// editLiteral is edit_apply with kind replace_literal: replace the text the
// agent already knows, in one file or across the workspace, without a
// preceding search. The number of occurrences must equal expected_count
// (default one) or nothing changes.
func (h *Handlers) editLiteral(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request editRequest) map[string]any {
	if request.Old == "" {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "replace_literal requires old, the exact text to replace", map[string]any{})
	}
	match, err := workspace.LocateLiteral(request.Path, request.Old)
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "literal_locate_failed", err)
	}
	if refusal := literalRefusal(requestID, workspace, request, match); refusal != nil {
		return refusal
	}
	replacement := request.New
	var warnings []string
	if match.Normalised {
		replacement = match.AdjustIndentation(request.New)
		warnings = append(warnings, match.Hint)
	}
	fromStateSeq := workspace.Identity().StateSeq
	change, err := workspace.ReplaceLiteral(workspace.Identity().ID, match.Hits, []byte(replacement), request.Preview)
	if err != nil {
		return editFailure(requestID, workspace, workspacecore.RangeHandle{Path: match.Hits[0].Path}, err)
	}
	applied := appliedEdit{
		files: change.Files, patches: change.Patches, revisions: change.Revisions,
		fromStateSeq: fromStateSeq, replacements: change.Replacements, warnings: warnings,
		summary: literalSummary(change, request.Preview), locations: literalLocations(match.Hits),
	}
	return h.finishEdit(ctx, requestID, workspace, request, applied)
}

// literalRefusal answers a literal that was not found, matches only with
// different whitespace, or occurs a different number of times than
// expected. Nothing has been changed when it returns non-nil.
func literalRefusal(requestID string, workspace *workspacecore.Workspace, request editRequest, match workspacecore.LiteralMatch) map[string]any {
	scope := "the workspace"
	if request.Path != "" {
		scope = request.Path
	}
	if len(match.Hits) == 0 {
		result := mcpapi.Envelope(requestID, workspace, "conflict", "literal_not_found",
			fmt.Sprintf("old text not found in %s, even after collapsing whitespace; nothing changed", scope), map[string]any{"path": request.Path})
		result["next"] = []any{
			map[string]any{"tool": "search", "action": "locate_a_shorter_distinctive_line", "mode": "literal"},
			map[string]any{"tool": "read", "action": "read_known_path", "path": request.Path},
		}
		return result
	}
	if match.Normalised && match.IndentPrefix == "" {
		result := mcpapi.Envelope(requestID, workspace, "conflict", "literal_whitespace_mismatch",
			"old text matches only after collapsing whitespace: "+match.Hint+"; nothing changed",
			map[string]any{"locations": literalLocations(match.Hits), "actual": match.Actual})
		result["next"] = []any{map[string]any{"tool": "edit_apply", "action": "retry_with_old_set_to_actual"}}
		return result
	}
	if len(match.Hits) != request.ExpectedCount {
		result := mcpapi.Envelope(requestID, workspace, "conflict", "literal_count_mismatch",
			fmt.Sprintf("old text occurs %d time(s) in %s, expected %d; nothing changed", len(match.Hits), scope, request.ExpectedCount),
			map[string]any{"expected_count": request.ExpectedCount, "found": len(match.Hits), "locations": literalLocations(match.Hits)})
		result["next"] = []any{
			map[string]any{"tool": "edit_apply", "action": "retry_with_expected_count_or_a_narrower_path", "found": len(match.Hits)},
			map[string]any{"tool": "edit_apply", "action": "include_more_surrounding_text_in_old"},
		}
		return result
	}
	return nil
}

func literalLocations(hits []workspacecore.LiteralHit) []string {
	locations := make([]string, 0, len(hits))
	for _, hit := range hits {
		locations = append(locations, fmt.Sprintf("%s:%d:%d", hit.Path, hit.Line, hit.Column))
	}
	return locations
}

func literalSummary(change workspacecore.LiteralChange, preview bool) string {
	noun := "occurrence"
	if change.Replacements != 1 {
		noun = "occurrences"
	}
	files := "1 file"
	if len(change.Files) != 1 {
		files = fmt.Sprintf("%d files", len(change.Files))
	}
	if preview {
		return fmt.Sprintf("Preview of replacing %d %s in %s; canonical bytes unchanged", change.Replacements, noun, files)
	}
	return fmt.Sprintf("Replaced %d %s in %s", change.Replacements, noun, files)
}

// editCreateFile is edit_apply with kind create_file: one new file, refused
// when the path already exists.
func (h *Handlers) editCreateFile(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request editRequest) map[string]any {
	if request.Path == "" {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "create_file requires path", map[string]any{})
	}
	snapshot, err := workspace.Snapshot(request.Path, workspacecore.ProviderLayer{})
	if err != nil {
		return mcpapi.Failure(requestID, workspace, "create_failed", err)
	}
	if snapshot.Disk.Kind != workspacecore.ObjectMissing {
		result := mcpapi.Envelope(requestID, workspace, "conflict", "create_target_exists",
			fmt.Sprintf("%s already exists; nothing changed", request.Path), map[string]any{"path": request.Path})
		result["next"] = []any{map[string]any{"tool": "edit_apply", "action": "replace_literal_inside_the_existing_file", "path": request.Path}}
		return result
	}
	fromStateSeq := workspace.Identity().StateSeq
	content := []byte(request.Content)
	applied := appliedEdit{
		files: []workspacecore.PlanStageFile{{Path: request.Path, After: content, AfterExists: true,
			Patch: fmt.Sprintf("--- /dev/null\n+++ %s\n@@ new file, %d bytes @@\n", request.Path, len(content))}},
		patches:      []string{fmt.Sprintf("--- /dev/null\n+++ %s\n@@ new file, %d bytes @@\n", request.Path, len(content))},
		fromStateSeq: fromStateSeq, replacements: 1, summary: "Created " + request.Path,
	}
	if request.Preview {
		applied.summary = "Preview of creating " + request.Path + "; canonical bytes unchanged"
		return h.finishEdit(ctx, requestID, workspace, request, applied)
	}
	diff, after, err := workspace.ApplyFile(workspace.Identity().ID, request.Path, snapshot.Revision, workspacecore.FileCreate, content)
	if err != nil {
		return editFailure(requestID, workspace, workspacecore.RangeHandle{Path: request.Path}, err)
	}
	applied.files[0].Path = diff.Path
	applied.revisions = map[string]workspacecore.RevisionID{diff.Path: after.Revision}
	return h.finishEdit(ctx, requestID, workspace, request, applied)
}
