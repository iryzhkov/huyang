package handlers

import (
	"bytes"
	"fmt"
	"go/format"
	"path/filepath"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// formatEdited runs the native formatter over every edited file whose
// language has one (gofmt for Go) and writes the result as a second
// canonical change, so the agent never leaves formatting drift behind. A
// file the formatter cannot parse is left as edited and reported; the
// diagnostics that follow will name the syntax error.
func formatEdited(workspace *workspacecore.Workspace, applied *appliedEdit) {
	var reformatted, skipped []string
	for index := range applied.files {
		file := &applied.files[index]
		if !file.AfterExists || applied.noFormat[file.Path] || strings.ToLower(filepath.Ext(file.Path)) != ".go" {
			continue
		}
		formatted, err := format.Source(file.After)
		if err != nil {
			skipped = append(skipped, file.Path+": "+strings.SplitN(err.Error(), "\n", 2)[0])
			continue
		}
		if bytes.Equal(formatted, file.After) {
			continue
		}
		if err := applyFormatted(workspace, file, formatted, applied); err != nil {
			skipped = append(skipped, file.Path+": "+err.Error())
			continue
		}
		reformatted = append(reformatted, file.Path)
	}
	if len(reformatted) == 0 && len(skipped) == 0 {
		return
	}
	applied.format = map[string]any{"formatter": "gofmt"}
	if len(reformatted) > 0 {
		applied.format["reformatted"] = reformatted
	}
	if len(skipped) > 0 {
		applied.format["skipped"] = skipped
	}
}

// applyFormatted replaces the whole edited file with its formatted form
// through the same guarded path as the edit itself.
func applyFormatted(workspace *workspacecore.Workspace, file *workspacecore.PlanStageFile, formatted []byte, applied *appliedEdit) error {
	handle, err := workspace.NewRange(file.Path, 0, len(file.After))
	if err != nil {
		return err
	}
	change, snapshot, err := workspace.ApplyReplace(workspace.Identity().ID, handle, formatted)
	if err != nil {
		return err
	}
	file.Patch += fmt.Sprintf("--- %s\n+++ %s\n@@ gofmt: %d bytes before, %d after @@\n", file.Path, file.Path, len(file.After), len(formatted))
	file.After = change.Diff.After
	if applied.revisions == nil {
		applied.revisions = map[string]workspacecore.RevisionID{}
	}
	applied.revisions[file.Path] = snapshot.Revision
	return nil
}
