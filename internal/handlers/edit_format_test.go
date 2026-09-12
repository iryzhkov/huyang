package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A Go edit that leaves misformatted code is reformatted by gofmt in the
// same call, the response says which files changed, and format=false keeps
// the bytes exactly as written.
func TestGoEditsAreFormattedUnlessDisabled(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"main.go": "package main\n\nfunc main() {\n\tx := 1\n\t_ = x\n}\n"})
	result := editLiteral(t, handlers, workspaceID, "fmt1", map[string]any{
		"path": "main.go", "old": "\tx := 1\n", "new": "\tx:=1\n\ty   :=  2\n\t_ = y\n",
	}, nil)
	if result["outcome"] == "conflict" || result["outcome"] == "failed" {
		t.Fatalf("edit = %#v", result)
	}
	content, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if string(content) != "package main\n\nfunc main() {\n\tx := 1\n\ty := 2\n\t_ = y\n\t_ = x\n}\n" {
		t.Fatalf("file after formatted edit = %q", content)
	}
	format := result["data"].(map[string]any)["format"].(map[string]any)
	if format["formatter"] != "gofmt" || len(format["reformatted"].([]string)) != 1 {
		t.Fatalf("format report = %#v", format)
	}
	diffs := result["data"].(map[string]any)["diffs"].([]map[string]any)
	if diffs[0]["after_sha256"] != contentHash(content) {
		t.Fatalf("diff hash does not describe the formatted file: %#v", diffs[0])
	}
	raw := editLiteral(t, handlers, workspaceID, "fmt2", map[string]any{
		"path": "main.go", "old": "\ty := 2\n", "new": "\ty:=3\n",
	}, map[string]any{"format": false})
	if raw["outcome"] == "conflict" || raw["outcome"] == "failed" {
		t.Fatalf("raw edit = %#v", raw)
	}
	content, _ = os.ReadFile(filepath.Join(root, "main.go"))
	if string(content) != "package main\n\nfunc main() {\n\tx := 1\n\ty:=3\n\t_ = y\n\t_ = x\n}\n" {
		t.Fatalf("format=false changed bytes: %q", content)
	}
	if _, present := raw["data"].(map[string]any)["format"]; present {
		t.Fatalf("format=false still reported a formatter run: %#v", raw)
	}
}

// A file the formatter cannot parse is left as edited and the response
// says the formatter skipped it, so the syntax error surfaces once, from
// the diagnostics, not as a refused edit.
func TestFormatterSkipsUnparsableGo(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	result := editLiteral(t, handlers, workspaceID, "fmt3", map[string]any{
		"path": "main.go", "old": "func main() {}\n", "new": "func main() {\n",
	}, nil)
	content, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if string(content) != "package main\n\nfunc main() {\n" {
		t.Fatalf("unparsable edit was altered: %q", content)
	}
	format := result["data"].(map[string]any)["format"].(map[string]any)
	if len(format["skipped"].([]string)) != 1 {
		t.Fatalf("format report = %#v", format)
	}
}

// A navigate target given as a symbol locator points the language server
// at the declaration line, not at a mention in a doc comment above it.
func TestProviderTargetPointsAtTheDeclarationNotItsComment(t *testing.T) {
	handlers, workspaceID, _ := literalFixture(t, map[string]string{
		"lines.go": "package p\n\n// boundedLines returns the requested window; see boundedLines.\nfunc boundedLines() {}\n",
	})
	workspace := handlers.registry.Lookup(workspacecore.ID(workspaceID))
	target, err := modernProviderTarget(workspace, map[string]any{"symbol_locator": map[string]any{"path": "lines.go", "name_path": "boundedLines"}})
	if err != nil {
		t.Fatal(err)
	}
	if target["line"] != 4 || target["col"] != 6 {
		t.Fatalf("declaration target = %#v", target)
	}
	_ = context.Background()
}
