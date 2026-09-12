package handlers

import (
	"errors"
	"fmt"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// The file-lifecycle kinds of edit_apply: move_file, copy_file and
// delete_file. Each is one guarded call through the native journal; none
// runs a formatter, because a moved or copied file must keep the source's
// exact bytes for Git to recognise the rename. Huyang never writes the Git
// index: the reply names the tracked state of each end and the git command
// that stages the change.

// applyMoveFile moves a regular file inside the workspace.
func applyMoveFile(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.From == "" || request.To == "" {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "move_file requires from and to", map[string]any{})
	}
	sourceRevision := workspacecore.RevisionID(request.RevisionID)
	if sourceRevision == "" {
		snapshot, err := workspace.Snapshot(request.From, workspacecore.ProviderLayer{})
		if err != nil {
			return appliedEdit{}, mcpapi.Failure(requestID, workspace, "move_failed", err)
		}
		sourceRevision = snapshot.Revision
	}
	fromStateSeq := workspace.Identity().StateSeq
	if request.Preview {
		source, err := workspace.TransferSource(request.From, "")
		if err != nil {
			return appliedEdit{}, lifecycleFailure(requestID, workspace, "move_failed", err)
		}
		applied := transferEdit(source, request.To, fromStateSeq, true)
		applied.summary = fmt.Sprintf("Preview of moving %s to %s; canonical bytes unchanged", source.Path, request.To)
		return applied, nil
	}
	moved, err := workspace.ApplyMove(workspace.Identity().ID, request.From, sourceRevision, request.To, workspacecore.RevisionID(request.DestinationRevisionID))
	if err != nil {
		return appliedEdit{}, lifecycleFailure(requestID, workspace, "move_failed", err)
	}
	applied := transferEdit(moved.Source, request.To, fromStateSeq, true)
	applied.revisions = map[string]workspacecore.RevisionID{request.To: moved.Destination.Revision, moved.Source.Path: moved.SourceAfter.Revision}
	applied.summary = fmt.Sprintf("Moved %s to %s", moved.Source.Path, request.To)
	applied.git = workspace.TrackedState([]string{moved.Source.Path, request.To})
	if applied.git[moved.Source.Path] == workspacecore.TrackedStateTracked {
		applied.next = append(applied.next, gitStep("stage_the_rename_so_git_records_it_as_one", "git add -A -- "+shellQuote(moved.Source.Path)+" "+shellQuote(request.To)))
	}
	return applied, nil
}

// applyCopyFile copies a file, from inside the workspace or from an
// absolute path outside it, to a new workspace document.
func applyCopyFile(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.From == "" || request.To == "" {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "copy_file requires from and to", map[string]any{})
	}
	source, err := workspace.TransferSource(request.From, request.ExpectedSHA256)
	if err != nil {
		return appliedEdit{}, lifecycleFailure(requestID, workspace, "copy_failed", err)
	}
	fromStateSeq := workspace.Identity().StateSeq
	applied := transferEdit(source, request.To, fromStateSeq, false)
	applied.source = map[string]any{"path": source.Path, "sha256": source.SHA256, "bytes": len(source.Content)}
	if !source.Inside {
		applied.source["outside_workspace"] = true
	}
	if request.Preview {
		applied.summary = fmt.Sprintf("Preview of copying %s to %s; canonical bytes unchanged", source.Path, request.To)
		return applied, nil
	}
	_, after, err := workspace.ApplyCopy(workspace.Identity().ID, source, request.To, workspacecore.RevisionID(request.DestinationRevisionID))
	if err != nil {
		return appliedEdit{}, lifecycleFailure(requestID, workspace, "copy_failed", err)
	}
	applied.revisions = map[string]workspacecore.RevisionID{request.To: after.Revision}
	applied.summary = fmt.Sprintf("Copied %s to %s (%d bytes)", source.Path, request.To, len(source.Content))
	applied.git = workspace.TrackedState([]string{request.To})
	if applied.git[request.To] == workspacecore.TrackedStateUntracked {
		applied.next = append(applied.next, gitStep("stage_the_new_file", "git add -- "+shellQuote(request.To)))
	}
	return applied, nil
}

// applyDeleteFile removes a file the agent has identified by revision or
// content hash. Without either guard nothing changes and the reply carries
// the current revision and hash, so the retry is one call.
func applyDeleteFile(requestID string, workspace *workspacecore.Workspace, request editRequest) (appliedEdit, map[string]any) {
	if request.Path == "" {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "failed", "invalid_target", "delete_file requires path", map[string]any{})
	}
	snapshot, err := workspace.Snapshot(request.Path, workspacecore.ProviderLayer{})
	if err != nil {
		return appliedEdit{}, mcpapi.Failure(requestID, workspace, "delete_failed", err)
	}
	if snapshot.Disk.Kind == workspacecore.ObjectMissing {
		return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "conflict", "delete_target_missing", request.Path+" does not exist; nothing changed", map[string]any{"path": request.Path})
	}
	revision := workspacecore.RevisionID(request.RevisionID)
	switch {
	case revision != "":
	case request.ExpectedSHA256 != "":
		if snapshot.ContentSHA256 != request.ExpectedSHA256 {
			return appliedEdit{}, mcpapi.Envelope(requestID, workspace, "conflict", workspacecore.CodeDeleteTargetChanged,
				fmt.Sprintf("%s has sha256 %s, expected %s; nothing changed", request.Path, snapshot.ContentSHA256, request.ExpectedSHA256),
				map[string]any{"path": request.Path, "revision_id": snapshot.Revision, "sha256": snapshot.ContentSHA256})
		}
		revision = snapshot.Revision
	default:
		result := mcpapi.Envelope(requestID, workspace, "conflict", workspacecore.CodeDeleteGuardRequired,
			"delete_file needs revision_id or expected_sha256 of the file it removes; nothing changed",
			map[string]any{"path": request.Path, "revision_id": snapshot.Revision, "sha256": snapshot.ContentSHA256, "bytes": snapshot.Disk.Size})
		result["next"] = []any{map[string]any{"tool": "edit_apply", "action": "retry_delete_with_this_revision_id", "path": request.Path, "revision_id": snapshot.Revision}}
		return appliedEdit{}, result
	}
	fromStateSeq := workspace.Identity().StateSeq
	if request.Preview {
		read, err := workspace.TransferSource(request.Path, "")
		if err != nil {
			return appliedEdit{}, lifecycleFailure(requestID, workspace, "delete_failed", err)
		}
		return appliedEdit{
			files:        []workspacecore.PlanStageFile{{Path: read.Path, Before: read.Content, BeforeExists: true, Patch: deletePatch(read.Path, len(read.Content))}},
			fromStateSeq: fromStateSeq, replacements: 1, noFormat: map[string]bool{read.Path: true},
			summary: "Preview of deleting " + read.Path + "; canonical bytes unchanged",
		}, nil
	}
	diff, _, err := workspace.ApplyFile(workspace.Identity().ID, request.Path, revision, workspacecore.FileDelete, nil)
	if err != nil {
		return appliedEdit{}, editFailure(requestID, workspace, workspacecore.RangeHandle{Path: request.Path}, err)
	}
	applied := appliedEdit{
		files:        []workspacecore.PlanStageFile{{Path: diff.Path, Before: diff.Before, BeforeExists: true, Patch: deletePatch(diff.Path, len(diff.Before))}},
		patches:      []string{deletePatch(diff.Path, len(diff.Before))},
		fromStateSeq: fromStateSeq, replacements: 1, noFormat: map[string]bool{diff.Path: true},
		summary: "Deleted " + diff.Path,
		git:     workspace.TrackedState([]string{diff.Path}),
	}
	if applied.git[diff.Path] == workspacecore.TrackedStateTracked {
		applied.next = append(applied.next, gitStep("stage_the_deletion", "git add -A -- "+shellQuote(diff.Path)))
	}
	return applied, nil
}

// transferEdit is the applied record of a move or copy before the shared
// response shaping: the destination as a new file and, for a move, the
// source as a removed one. Neither is formatted.
func transferEdit(source workspacecore.TransferSource, to string, fromStateSeq uint64, move bool) appliedEdit {
	patch := fmt.Sprintf("--- /dev/null\n+++ %s\n@@ %d bytes from %s @@\n", to, len(source.Content), source.Path)
	applied := appliedEdit{
		files:        []workspacecore.PlanStageFile{{Path: to, After: source.Content, AfterExists: true, Patch: patch}},
		patches:      []string{patch},
		fromStateSeq: fromStateSeq, replacements: 1, noFormat: map[string]bool{to: true},
	}
	if move {
		removed := deletePatch(source.Path, len(source.Content))
		applied.files = append(applied.files, workspacecore.PlanStageFile{Path: source.Path, Before: source.Content, BeforeExists: true, Patch: removed})
		applied.patches = append(applied.patches, removed)
		applied.noFormat[source.Path] = true
	}
	return applied
}

func deletePatch(path string, size int) string {
	return fmt.Sprintf("--- %s\n+++ /dev/null\n@@ deleted, %d bytes @@\n", path, size)
}

// gitStep is a next entry that is a shell command rather than a tool call:
// Huyang does not write the Git index, so the agent runs it.
func gitStep(action, command string) map[string]any {
	return map[string]any{"tool": "shell", "action": action, "command": command}
}

// shellQuote quotes a path for the git command in a next entry.
func shellQuote(path string) string {
	if path != "" && !strings.ContainsAny(path, " \t\n'\"\\$`!*?[]{}()<>|&;~#") {
		return path
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// lifecycleFailure classifies a refusal from the workspace: a coded error
// is a conflict under its own code, a revision conflict is answered like
// any other edit, and anything else is a failure under fallback.
func lifecycleFailure(requestID string, workspace *workspacecore.Workspace, fallback string, err error) map[string]any {
	if code := workspacecore.ErrorCode(err); code != "" {
		return mcpapi.Envelope(requestID, workspace, "conflict", code, err.Error()+"; nothing changed", map[string]any{})
	}
	var conflict *workspacecore.Conflict
	if errors.As(err, &conflict) {
		return editFailure(requestID, workspace, workspacecore.RangeHandle{Path: conflict.Path}, err)
	}
	return mcpapi.Failure(requestID, workspace, fallback, err)
}
