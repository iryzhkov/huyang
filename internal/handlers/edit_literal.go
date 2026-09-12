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
	applied, failure := applyLiteral(requestID, workspace, request)
	if failure != nil {
		return failure
	}
	return h.finishEdit(ctx, requestID, workspace, request, applied)
}

// applyLiteral locates and replaces the literal; the failure envelope is
// non-nil when nothing changed. It is shared by the single-operation call
// and the operations list.
func applyLiteral(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.Old == "" {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "replace_literal requires old, the exact text to replace", map[string]any{})
	}
	match, err := workspace.LocateLiteral(request.Path, request.Old)
	if err != nil {
		return appliedEdit{}, mcpapi.Failure(requestID, workspace, "literal_locate_failed", err)
	}
	if refusal := literalRefusal(requestID, workspace, request, match); refusal != nil {
		return appliedEdit{}, refusal
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
		return appliedEdit{}, editFailure(requestID, workspace, workspacecore.RangeHandle{Path: match.Hits[0].Path}, err)
	}
	return appliedEdit{
		files: change.Files, patches: change.Patches, revisions: change.Revisions,
		fromStateSeq: fromStateSeq, replacements: change.Replacements, warnings: warnings,
		summary: literalSummary(change, request.Preview), locations: literalLocations(match.Hits),
	}, nil
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
// when the path already exists unless replace is set with the existing
// file's revision_id.
func (h *Handlers) editCreateFile(ctx context.Context, requestID string, workspace *workspacecore.Workspace, request editRequest) map[string]any {
	applied, failure := applyCreateFile(requestID, workspace, request)
	if failure != nil {
		return failure
	}
	return h.finishEdit(ctx, requestID, workspace, request, applied)
}

// applyCreateFile writes the new file; the failure envelope is non-nil when
// nothing changed. Shared by the single-operation call and the list.
func applyCreateFile(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.Path == "" {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "create_file requires path", map[string]any{})
	}
	if request.Replace {
		return applyReplaceFile(requestID, workspace, request)
	}
	snapshot, err := workspace.Snapshot(request.Path, workspacecore.ProviderLayer{})
	if err != nil {
		return appliedEdit{}, mcpapi.Failure(requestID, workspace, "create_failed", err)
	}
	if snapshot.Disk.Kind != workspacecore.ObjectMissing {
		result := mcpapi.Envelope(requestID, workspace, "conflict", workspacecore.CodeCreateTargetExists,
			fmt.Sprintf("%s already exists; nothing changed. To overwrite it, pass replace: true with its revision_id (%s)", request.Path, snapshot.Revision),
			map[string]any{"path": request.Path, "revision_id": snapshot.Revision})
		result["next"] = []any{
			map[string]any{"tool": "edit_apply", "action": "replace_literal_inside_the_existing_file", "path": request.Path},
			map[string]any{"tool": "edit_apply", "action": "create_file_with_replace_true_and_this_revision_id", "path": request.Path, "revision_id": snapshot.Revision},
		}
		return appliedEdit{}, result
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
		return applied, nil
	}
	diff, after, err := workspace.ApplyFile(workspace.Identity().ID, request.Path, snapshot.Revision, workspacecore.FileCreate, content)
	if err != nil {
		return appliedEdit{}, editFailure(requestID, workspace, workspacecore.RangeHandle{Path: request.Path}, err)
	}
	applied.files[0].Path = diff.Path
	applied.revisions = map[string]workspacecore.RevisionID{diff.Path: after.Revision}
	return applied, nil
}

// applyReplaceFile is create_file with replace: a guarded whole-file
// overwrite of an existing document, pinned to the revision the agent read.
func applyReplaceFile(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.RevisionID == "" {
		snapshot, err := workspace.Snapshot(request.Path, workspacecore.ProviderLayer{})
		if err != nil {
			return appliedEdit{}, mcpapi.Failure(requestID, workspace, "create_failed", err)
		}
		result := mcpapi.Envelope(requestID, workspace, "conflict", workspacecore.CodeReplaceRevisionRequired,
			"create_file with replace needs the revision_id of the file it overwrites; nothing changed",
			map[string]any{"path": request.Path, "revision_id": snapshot.Revision})
		result["next"] = []any{map[string]any{"tool": "edit_apply", "action": "retry_with_this_revision_id", "path": request.Path, "revision_id": snapshot.Revision}}
		return appliedEdit{}, result
	}
	fromStateSeq := workspace.Identity().StateSeq
	content := []byte(request.Content)
	if request.Preview {
		read, err := workspace.Read(request.Path)
		if err != nil {
			return appliedEdit{}, mcpapi.Failure(requestID, workspace, "create_failed", err)
		}
		return appliedEdit{
			files:        []workspacecore.PlanStageFile{{Path: read.Path, Before: read.Content, After: content, BeforeExists: true, AfterExists: true, Patch: replacePatch(read.Path, len(read.Content), len(content))}},
			fromStateSeq: fromStateSeq, replacements: 1, summary: "Preview of replacing " + read.Path + "; canonical bytes unchanged",
		}, nil
	}
	diff, after, err := workspace.ApplyFile(workspace.Identity().ID, request.Path, workspacecore.RevisionID(request.RevisionID), workspacecore.FileReplace, content)
	if err != nil {
		return appliedEdit{}, editFailure(requestID, workspace, workspacecore.RangeHandle{Path: request.Path}, err)
	}
	patch := replacePatch(diff.Path, len(diff.Before), len(diff.After))
	return appliedEdit{
		files:        []workspacecore.PlanStageFile{{Path: diff.Path, Before: diff.Before, After: diff.After, BeforeExists: true, AfterExists: true, Patch: patch}},
		patches:      []string{patch},
		fromStateSeq: fromStateSeq, replacements: 1, summary: "Replaced " + diff.Path,
		revisions: map[string]workspacecore.RevisionID{diff.Path: after.Revision},
	}, nil
}

func replacePatch(path string, before, after int) string {
	return fmt.Sprintf("--- %s\n+++ %s\n@@ replaced, %d bytes before, %d after @@\n", path, path, before, after)
}
