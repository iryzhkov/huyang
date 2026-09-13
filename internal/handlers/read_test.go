package handlers

import (
	"context"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A locator that matched nothing and one that matched twice are different
// mistakes: the first needs another name, the second needs the name
// qualified. Both used to answer "did not resolve uniquely", which describes
// the second and misdescribes the first.
func TestSymbolLocatorMissSaysWhichMissItWas(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"ledger.go": "package ledger\n\nfunc Total() int { return 1 }\n",
		"twice.go":  "package ledger\n\nfunc Same() int { return 1 }\n\nfunc Same() int { return 2 }\n",
	})
	read := func(path, name string) map[string]any {
		t.Helper()
		return handlers.Execute(context.Background(), "req_symbol", "read", map[string]any{
			"workspace_id": workspaceID,
			"target":       map[string]any{"symbol_locator": map[string]any{"path": path, "name_path": name}},
		})
	}
	absent := read("ledger.go", "Missing")
	if absent["code"] != "symbol_not_found" || !strings.Contains(absent["summary"].(string), "No declaration named Missing") {
		t.Fatalf("a name that is not there = %#v", absent)
	}
	if count := absent["data"].(map[string]any)["match_count"]; count != 0 {
		t.Fatalf("match_count = %#v, want 0", count)
	}
	outlined := false
	for _, raw := range absent["next"].([]any) {
		if step, _ := raw.(map[string]any); step != nil && step["view"] == "outline" {
			outlined = true
		}
	}
	if !outlined {
		t.Fatalf("a missing name was not offered the list of names the file declares: %#v", absent["next"])
	}
	ambiguous := read("twice.go", "Same")
	if ambiguous["code"] != "symbol_not_found" || !strings.Contains(ambiguous["summary"].(string), "2 declarations named Same") {
		t.Fatalf("a name declared twice = %#v", ambiguous)
	}
}

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
