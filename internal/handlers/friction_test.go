package handlers

import (
	"context"
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
