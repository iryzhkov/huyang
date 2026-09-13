package handlers

import (
	"context"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A workspace_id this service does not hold is a refusal a caller cannot
// repair from the id, so it names the two ways back rather than leaving the
// agent to guess which of them exists.
func TestUnknownWorkspaceIDOffersAWayBack(t *testing.T) {
	handlers, _, root := literalFixture(t, map[string]string{"ledger.go": "package ledger\n"})
	result := handlers.Execute(context.Background(), "req_unknown", "read", map[string]any{
		"workspace_id": "ws_00000000000000000000000000000000",
		"target":       map[string]any{"path": "ledger.go"},
	})
	if result["code"] != "workspace_not_found" {
		t.Fatalf("unknown workspace = %#v", result)
	}
	next, _ := result["next"].([]any)
	if len(next) == 0 {
		t.Fatalf("the refusal leaves nothing to do next: %#v", result)
	}
	if first, _ := next[0].(map[string]any); first["tool"] != "workspace_open" {
		t.Fatalf("the first way back is not opening the workspace: %#v", next)
	}
	// And the way it names works: the same read, given the root instead,
	// answers.
	retried := handlers.Execute(context.Background(), "req_root", "read", map[string]any{
		"root": root, "target": map[string]any{"path": "ledger.go"},
	})
	if retried["outcome"] != "ok" {
		t.Fatalf("the recovery the refusal names does not work: %#v", retried)
	}
}

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
