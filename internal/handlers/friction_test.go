package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Notices collapse to the last state of each finding, newest first, drop
// findings whose last word is stale, omit the empty attribution, and cap
// at MaxDiagnosticUpdates with a truncation flag.
func TestCollapseNoticesKeepsOneLinePerFinding(t *testing.T) {
	notice := func(cursor, kind, id string) workspacecore.DiagnosticNotice {
		return workspacecore.DiagnosticNotice{Cursor: cursor, Kind: kind, ID: id, Severity: 1, Document: "/repo/a.go", Attribution: workspacecore.CulpritAttribution{Rank: "unattributed"}}
	}
	notices := []workspacecore.DiagnosticNotice{
		notice("diagcur_1", "new", "d1"), notice("diagcur_2", "stale", "d1"), notice("diagcur_3", "new", "d1"),
		notice("diagcur_4", "new", "d2"), notice("diagcur_5", "stale", "d2"),
		notice("diagcur_6", "new", "d3"), notice("diagcur_7", "resolved", "d3"),
	}
	updates, dropped := collapseNotices("/repo", notices)
	if dropped || len(updates) != 2 {
		t.Fatalf("updates = %#v dropped=%v", updates, dropped)
	}
	if updates[0].ID != "d3" || updates[0].Kind != "resolved" || updates[1].ID != "d1" || updates[1].Kind != "new" {
		t.Fatalf("collapsed order = %#v", updates)
	}
	if updates[0].Path != "a.go" || updates[0].Attribution != nil {
		t.Fatalf("update is not compact: %#v", updates[0])
	}
	many := make([]workspacecore.DiagnosticNotice, 0, MaxDiagnosticUpdates+3)
	for index := 0; index < MaxDiagnosticUpdates+3; index++ {
		many = append(many, notice("diagcur_"+string(rune('a'+index)), "new", "id"+string(rune('a'+index))))
	}
	if updates, dropped = collapseNotices("/repo", many); !dropped || len(updates) != MaxDiagnosticUpdates {
		t.Fatalf("cap = %d dropped=%v", len(updates), dropped)
	}
}

// A call that names a project root instead of a workspace_id opens (or
// reuses) the workspace inside the call; the reply names the workspace it
// used and the second call reuses it.
func TestRootOpensTheProjectWorkspaceImplicitly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nconst Answer = 41\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handlers := newTestHandlers(t, fixedFactory{backend: &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}})
	read := handlers.Execute(context.Background(), "req_root_read", "read", map[string]any{"root": root, "target": map[string]any{"path": "main.go"}, "numbered": true})
	if read["outcome"] != "ok" {
		t.Fatalf("read through root = %#v", read)
	}
	identity := read["workspace"].(workspacecore.Identity)
	if identity.Kind != workspacecore.KindProject || identity.Root != root {
		t.Fatalf("implicit workspace = %#v", identity)
	}
	if content := read["data"].(map[string]any)["content"].(string); !strings.HasPrefix(content, "1\tpackage main\n2\t\n3\tconst Answer") {
		t.Fatalf("numbered content = %q", content)
	}
	edited := handlers.Execute(context.Background(), "req_root_edit", "edit_apply", map[string]any{
		"root": root, "idempotency_key": "root-edit",
		"operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "41", "new": "42"},
	})
	if edited["outcome"] != "ok" && edited["outcome"] != "provisional" {
		t.Fatalf("edit through root = %#v", edited)
	}
	if edited["workspace"].(workspacecore.Identity).ID != identity.ID {
		t.Fatalf("second call opened a different workspace: %#v", edited["workspace"])
	}
	content, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if !strings.Contains(string(content), "Answer = 42") {
		t.Fatalf("file after edit = %q", content)
	}
}

// search scopes hits with paths and attaches numbered context lines.
func TestSearchPathsAndContextLines(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"one.go": "package a\n\nfunc One() {}\n\nvar total = 1\n",
		"two.py": "total = 2\n",
	})
	result := handlers.Execute(context.Background(), "req_search", "search", map[string]any{
		"workspace_id": workspaceID, "query": "total", "paths": []any{"*.go"}, "context_lines": 1,
	})
	hits := result["data"].(map[string]any)["hits"].([]map[string]any)
	if result["outcome"] != "ok" || len(hits) != 1 || hits[0]["path"] != "one.go" {
		t.Fatalf("filtered hits = %#v", result)
	}
	if context, _ := hits[0]["context"].(string); context != "4\t\n5\tvar total = 1\n" {
		t.Fatalf("context = %q", context)
	}
}

// A search that stopped at its match bound holds the bound, not the number
// of matches in the workspace. The count says it is a floor, the bound is
// named, and the follow-up offers the narrowing that reaches the files the
// capped search never opened.
func TestSearchStoppedAtItsBoundReportsAFloor(t *testing.T) {
	var many strings.Builder
	many.WriteString("package a\n")
	for index := range 4000 {
		fmt.Fprintf(&many, "// needle %d\n", index)
	}
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"many.go": many.String()})
	result := handlers.Execute(context.Background(), "req_search", "search", map[string]any{
		"workspace_id": workspaceID, "query": "needle",
	})
	data := result["data"].(map[string]any)
	limit, _ := data["match_limit"].(int)
	if data["total_is_lower_bound"] != true || limit == 0 || data["total"] != limit {
		t.Fatalf("a capped search reported %#v as if it were the whole count", data)
	}
	if summary, _ := result["summary"].(string); !strings.HasPrefix(summary, "at least ") {
		t.Fatalf("summary states the cap as a total: %q", summary)
	}
	warnings, _ := result["warnings"].([]string)
	if len(warnings) == 0 || !strings.Contains(warnings[0], "at least") {
		t.Fatalf("warning counts against the cap as if it were the total: %#v", warnings)
	}
	narrowing := false
	for _, raw := range result["next"].([]any) {
		if step, _ := raw.(map[string]any); step != nil && strings.Contains(fmt.Sprint(step["action"]), "narrow") {
			narrowing = true
		}
	}
	if !narrowing {
		t.Fatalf("a capped search only offered to refine what it already holds: %#v", result["next"])
	}
}

// An operations list applies several literal edits and a new file in one
// call, each located against the bytes the previous ones left; a refusal
// in the middle reports how many operations were applied and stops.
func TestEditOperationsApplyInOrderAndReportARefusal(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"main.go": "package main\n\nconst A = 1\nconst B = 2\n"})
	result := handlers.Execute(context.Background(), "req_ops", "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "ops-1",
		"operations": []any{
			map[string]any{"kind": "replace_literal", "path": "main.go", "old": "A = 1", "new": "A = 10"},
			map[string]any{"kind": "replace_literal", "path": "main.go", "old": "A = 10\nconst B = 2", "new": "A = 10\nconst B = 20"},
			map[string]any{"kind": "create_file", "path": "extra.go", "content": "package main\n"},
		},
	})
	if result["outcome"] != "ok" && result["outcome"] != "provisional" {
		t.Fatalf("operations = %#v", result)
	}
	data := result["data"].(map[string]any)
	if paths := data["changed_paths"].([]string); len(paths) != 2 || data["replacements"] != 2 || len(data["locations"].([]string)) != 2 {
		t.Fatalf("operations data = %#v", data)
	}
	content, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if string(content) != "package main\n\nconst A = 10\nconst B = 20\n" {
		t.Fatalf("file after operations = %q", content)
	}
	if _, err := os.Stat(filepath.Join(root, "extra.go")); err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	refused := handlers.Execute(context.Background(), "req_ops2", "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "ops-2",
		"operations": []any{
			map[string]any{"kind": "replace_literal", "path": "main.go", "old": "B = 20", "new": "B = 30"},
			map[string]any{"kind": "replace_literal", "path": "main.go", "old": "missing", "new": "x"},
		},
	})
	refusedData := refused["data"].(map[string]any)
	if refused["code"] != "literal_not_found" || refusedData["failed_operation"] != 1 || refusedData["applied_operations"] != 1 || !strings.HasPrefix(refused["summary"].(string), "operation 1:") {
		t.Fatalf("refusal = %#v", refused)
	}
	content, _ = os.ReadFile(filepath.Join(root, "main.go"))
	if !strings.Contains(string(content), "B = 30") {
		t.Fatalf("first operation was not kept: %q", content)
	}
}

// A semantic search mode resolves the query as a declaration name before
// asking the language server; an unknown name is a conflict that points at
// the literal search. Literal hits carry a handle only on request.
func TestSemanticSearchResolvesTheSymbolFirst(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"main.go": "package main\n\nfunc Target() {}\n\nvar _ = Target\n"})
	unknown := handlers.Execute(context.Background(), "req_sem", "search", map[string]any{"workspace_id": workspaceID, "query": "Nope", "mode": "references"})
	if unknown["code"] != "symbol_not_found" || len(unknown["next"].([]any)) != 1 {
		t.Fatalf("unknown symbol = %#v", unknown)
	}
	literal := handlers.Execute(context.Background(), "req_lit", "search", map[string]any{"workspace_id": workspaceID, "query": "Target"})
	hits := literal["data"].(map[string]any)["hits"].([]map[string]any)
	if len(hits) != 2 {
		t.Fatalf("literal hits = %#v", hits)
	}
	if _, present := hits[0]["handle"]; present {
		t.Fatalf("hit carries a handle without include_handles: %#v", hits[0])
	}
	withHandles := handlers.Execute(context.Background(), "req_lit2", "search", map[string]any{"workspace_id": workspaceID, "query": "Target", "include_handles": true})
	if hit := withHandles["data"].(map[string]any)["hits"].([]map[string]any)[0]; hit["handle"] == nil || hit["column"] == nil {
		t.Fatalf("hit lacks its handle on request: %#v", hit)
	}
}

// An edit to a file that no language server or parser can diagnose is
// plainly ok: no provisional verdict, no diagnostic recovery hints.
func TestEditOfProseFileIsPlainlyOK(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{"notes.md": "# Notes\n\nfirst\n"})
	result := editLiteral(t, handlers, workspaceID, "md", map[string]any{"path": "notes.md", "old": "first", "new": "second"}, nil)
	if result["outcome"] != "ok" || len(result["next"].([]any)) != 0 || strings.Contains(result["summary"].(string), ";") {
		t.Fatalf("prose edit = %#v", result)
	}
	data := result["data"].(map[string]any)
	for _, absent := range []string{"verification", "diagnostic_delta"} {
		if _, present := data[absent]; present {
			t.Fatalf("prose edit carries %s: %#v", absent, data)
		}
	}
}
