package handlers

import (
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A range handle whose document changed suggests refreshing the path; it
// never suggests a symbol search when the handle carried no symbol name.
func TestHandleConflictWithoutSymbolNeverSuggestsEmptySearch(t *testing.T) {
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: t.TempDir(), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := modernHandleConflict("req_test", workspace, workspacecore.HandleResolution{
		Status:   workspacecore.ResolutionConflicted,
		Code:     workspacecore.ConflictDocumentChanged,
		Original: workspacecore.SemanticLocator{Path: "note.txt"},
	})
	next := result["next"].([]any)
	if len(next) != 1 || next[0].(map[string]any)["tool"] != "read" {
		t.Fatalf("range conflict suggested a search with no query: %#v", result)
	}
}
