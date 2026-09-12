package handlers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func literalFixture(t *testing.T, files map[string]string) (*Handlers, string, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	if opened["outcome"] != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	return handlers, string(opened["workspace"].(workspacecore.Identity).ID), root
}

func editLiteral(t *testing.T, handlers *Handlers, workspaceID, key string, operation map[string]any, extra map[string]any) map[string]any {
	t.Helper()
	operation["kind"] = "replace_literal"
	arguments := map[string]any{"workspace_id": workspaceID, "idempotency_key": key, "operation": operation}
	for name, value := range extra {
		arguments[name] = value
	}
	return handlers.Execute(context.Background(), "req_"+key, "edit_apply", arguments)
}

// One call replaces known text and answers with the compact shape: changed
// paths, one diff record per file, the revisions and the diagnostics, and
// none of the hashes or handle resolution detail unless verbose is set.
func TestReplaceLiteralIsOneCallWithCompactResponse(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"ledger.go": "package ledger\n\nconst MaxEntries = 10000\n"})
	result := editLiteral(t, handlers, workspaceID, "e1", map[string]any{
		"path": "ledger.go", "old": "const MaxEntries = 10000", "new": "const MaxEntries = 20000",
	}, nil)
	if result["outcome"] != "ok" && result["outcome"] != "provisional" {
		t.Fatalf("literal edit = %#v", result)
	}
	content, _ := os.ReadFile(filepath.Join(root, "ledger.go"))
	if string(content) != "package ledger\n\nconst MaxEntries = 20000\n" {
		t.Fatalf("file after edit = %q", content)
	}
	data := result["data"].(map[string]any)
	for _, absent := range []string{"change", "resolution", "tool_delta"} {
		if _, present := data[absent]; present {
			t.Fatalf("compact response carries %s: %#v", absent, data)
		}
	}
	diffs := data["diffs"].([]map[string]any)
	if len(diffs) != 1 || diffs[0]["path"] != "ledger.go" || !strings.Contains(diffs[0]["patch"].(string), "20000") {
		t.Fatalf("diffs = %#v", diffs)
	}
	if data["document_revision"] == nil || data["revision"] == data["from_revision"] {
		t.Fatalf("revisions = %#v", data)
	}
	encoded, _ := json.Marshal(mcpapi.CompactTextEnvelope(result))
	t.Logf("compact one-line edit response (%d bytes): %s", len(encoded), encoded)
	if len(encoded) > 1500 {
		t.Fatalf("compact one-line edit response is %d bytes:\n%s", len(encoded), encoded)
	}
	verbose := editLiteral(t, handlers, workspaceID, "e1v", map[string]any{
		"path": "ledger.go", "old": "20000", "new": "30000",
	}, map[string]any{"verbose": true})
	if _, present := verbose["data"].(map[string]any)["tool_delta"]; !present {
		t.Fatalf("verbose response lacks the full detail: %#v", verbose)
	}
}

// A count other than expected changes nothing and lists where the text is.
func TestReplaceLiteralRefusesUnexpectedCount(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"sum.go": "var total int\ntotal++\nreturn total\n"})
	result := editLiteral(t, handlers, workspaceID, "e3", map[string]any{"path": "sum.go", "old": "total", "new": "sum"}, nil)
	if result["outcome"] != "conflict" || result["code"] != "literal_count_mismatch" {
		t.Fatalf("count mismatch = %#v", result)
	}
	locations := result["data"].(map[string]any)["locations"].([]string)
	if len(locations) != 3 || locations[1] != "sum.go:2:1" {
		t.Fatalf("locations = %#v", locations)
	}
	content, _ := os.ReadFile(filepath.Join(root, "sum.go"))
	if strings.Contains(string(content), "sum") {
		t.Fatalf("refused edit changed the file: %q", content)
	}
	accepted := editLiteral(t, handlers, workspaceID, "e3b", map[string]any{"path": "sum.go", "old": "total", "new": "sum", "expected_count": 3}, nil)
	if accepted["data"].(map[string]any)["replacements"] != 3 {
		t.Fatalf("expected_count edit = %#v", accepted)
	}
	content, _ = os.ReadFile(filepath.Join(root, "sum.go"))
	if string(content) != "var sum int\nsum++\nreturn sum\n" {
		t.Fatalf("file after rename = %q", content)
	}
}

// Without a path the literal is looked for in every file, and a missing
// literal is a conflict that names the scope.
func TestReplaceLiteralSpansTheWorkspaceAndReportsMisses(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{
		"a.go": "call(x)\n", "b.go": "call(x)\n", "c.txt": "nothing\n",
	})
	result := editLiteral(t, handlers, workspaceID, "e6", map[string]any{"old": "call(x)", "new": "call(x, false)", "expected_count": 2}, nil)
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("workspace-wide edit = %#v", result)
	}
	if paths := result["data"].(map[string]any)["changed_paths"].([]string); len(paths) != 2 {
		t.Fatalf("changed paths = %#v", paths)
	}
	for _, name := range []string{"a.go", "b.go"} {
		content, _ := os.ReadFile(filepath.Join(root, name))
		if string(content) != "call(x, false)\n" {
			t.Fatalf("%s after edit = %q", name, content)
		}
	}
	missing := editLiteral(t, handlers, workspaceID, "e7", map[string]any{"old": "absent text", "new": "x"}, nil)
	if missing["outcome"] != "conflict" || missing["code"] != "literal_not_found" || !strings.Contains(missing["summary"].(string), "the workspace") {
		t.Fatalf("missing literal = %#v", missing)
	}
}

// Text given with less indentation than the file still applies, the
// replacement is indented to match, and the response says so.
func TestReplaceLiteralAdjustsIndentationWithWarning(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"f.go": "func f() {\n\tif a {\n\t\treturn 1\n\t}\n}\n"})
	result := editLiteral(t, handlers, workspaceID, "e2", map[string]any{
		"path": "f.go", "old": "if a {\n\treturn 1\n}\n", "new": "if b {\n\treturn 2\n}\n",
	}, nil)
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("indented edit = %#v", result)
	}
	if warnings := result["warnings"].([]string); len(warnings) != 1 || !strings.Contains(warnings[0], "indented more") {
		t.Fatalf("warnings = %#v", warnings)
	}
	content, _ := os.ReadFile(filepath.Join(root, "f.go"))
	if string(content) != "func f() {\n\tif b {\n\t\treturn 2\n\t}\n}\n" {
		t.Fatalf("file after edit = %q", content)
	}
	spaces := editLiteral(t, handlers, workspaceID, "e2s", map[string]any{
		"path": "f.go", "old": "    if b {\n        return 2\n    }\n", "new": "x",
	}, nil)
	if spaces["code"] != "literal_whitespace_mismatch" || spaces["data"].(map[string]any)["actual"] != "\tif b {\n\t\treturn 2\n\t}\n" {
		t.Fatalf("tab/space mismatch = %#v", spaces)
	}
}

// create_file writes a new file in one call and refuses an existing path.
func TestCreateFileIsOneCallAndRefusesExistingPath(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"go.mod": "module x\n"})
	arguments := map[string]any{"workspace_id": workspaceID, "idempotency_key": "e4", "operation": map[string]any{
		"kind": "create_file", "path": "audit.go", "content": "package x\n\nfunc Audit() {}\n",
	}}
	result := handlers.Execute(context.Background(), "req_e4", "edit_apply", arguments)
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("create = %#v", result)
	}
	content, _ := os.ReadFile(filepath.Join(root, "audit.go"))
	if string(content) != "package x\n\nfunc Audit() {}\n" {
		t.Fatalf("created file = %q", content)
	}
	if paths := result["data"].(map[string]any)["changed_paths"].([]string); len(paths) != 1 || paths[0] != "audit.go" {
		t.Fatalf("changed paths = %#v", paths)
	}
	arguments["idempotency_key"] = "e4-again"
	again := handlers.Execute(context.Background(), "req_e4b", "edit_apply", arguments)
	if again["outcome"] != "conflict" || again["code"] != "create_target_exists" {
		t.Fatalf("second create = %#v", again)
	}
}
