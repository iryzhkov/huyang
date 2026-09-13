package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// outlineProvider answers file_symbols the way the kernel does: one entry
// per declaration in the named file, with a first-last line range.
type outlineProvider struct {
	stubProvider
	asked []string
}

func (p *outlineProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	if request.Operation != "file_symbols" {
		return provider.Result{Value: map[string]any{}}, nil
	}
	file, _ := request.Arguments["file"].(string)
	p.asked = append(p.asked, file)
	return provider.Result{Value: map[string]any{"count": 2, "complete": true, "matches": []any{
		map[string]any{"file": file, "name_path": "M/setup", "kind": "function", "lines": "5-7"},
		map[string]any{"file": file, "name_path": "M", "kind": "variable", "lines": "1-1"},
	}}}, nil
}

// The native sectioner covers Go and Python only, so an outline of any other
// language asks the semantic provider for the file's declarations instead of
// answering a handle to the whole document.
func TestOutlineOfUnparsedLanguageUsesProvider(t *testing.T) {
	root := t.TempDir()
	source := "local M = {}\n\n-- Set the module up.\n\nfunction M.setup(options)\n    return options\nend\n\nreturn M\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.lua"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &outlineProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "outline", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	if opened["outcome"] != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	result := handlers.Execute(context.Background(), "req_outline", "read", map[string]any{
		"workspace_id": workspaceID, "view": "outline", "target": map[string]any{"path": "plugin.lua"},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("outline = %#v", result)
	}
	outline := result["data"].(map[string]any)
	sections := outline["sections"].([]map[string]any)
	// Sections come back in file order, whatever order the provider listed
	// them in, each with the lines it spans and the handle that addresses
	// it; the whole-document fallback is gone because there are declarations
	// to address instead.
	if len(sections) != 2 || sections[0]["name"] != "M" || sections[1]["name"] != "M/setup" {
		t.Fatalf("outline sections = %#v", sections)
	}
	if sections[1]["start_line"] != 5 || sections[1]["end_line"] != 7 || sections[1]["handle"] == nil {
		t.Fatalf("outline section detail = %#v", sections[1])
	}
	if _, fallback := outline["fallback_handle"]; fallback || outline["coverage"].(workspacecore.Coverage).Semantic != "embedded_nvim" {
		t.Fatalf("outline coverage = %#v", outline)
	}
	if len(backend.asked) != 1 {
		t.Fatalf("provider calls = %#v", backend.asked)
	}
	// Each section is a durable symbol handle, so the declaration the
	// outline names can be read back by locator without another search.
	read := handlers.Execute(context.Background(), "req_read", "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"symbol_locator": map[string]any{"path": "plugin.lua", "name_path": "M/setup"}},
	})
	if read["outcome"] != "ok" {
		t.Fatalf("read by outline locator = %#v", read)
	}
	if content := read["data"].(map[string]any)["content"].(string); content != "function M.setup(options)\n    return options\nend\n" {
		t.Fatalf("read by outline locator = %q", content)
	}
}

// A generated file can declare thousands of symbols, and the outline is the
// call the guide recommends for exactly that file. The list is bounded, the
// count stays the file's own, and the reply says the list was cut.
func TestOutlineOfAGeneratedFileIsBoundedAndSaysSo(t *testing.T) {
	root := t.TempDir()
	var source strings.Builder
	source.WriteString("package generated\n")
	declarations := mcpapi.MaxOutlineSections + 50
	for index := range declarations {
		fmt.Fprintf(&source, "\nfunc Generated%04d() int { return %d }\n", index, index)
	}
	if err := os.WriteFile(filepath.Join(root, "generated.go"), []byte(source.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &outlineProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "outline", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	result := handlers.Execute(context.Background(), "req_outline", "read", map[string]any{
		"workspace_id": workspaceID, "view": "outline", "target": map[string]any{"path": "generated.go"},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("outline = %#v", result)
	}
	data := result["data"].(map[string]any)
	sections := data["sections"].([]map[string]any)
	if len(sections) != mcpapi.MaxOutlineSections {
		t.Fatalf("outline listed %d declarations, want the %d-section bound", len(sections), mcpapi.MaxOutlineSections)
	}
	if data["declaration_count"] != declarations || data["listed_count"] != mcpapi.MaxOutlineSections {
		t.Fatalf("outline counts = %#v / %#v, want %d declared and %d listed",
			data["declaration_count"], data["listed_count"], declarations, mcpapi.MaxOutlineSections)
	}
	if data["sections_truncated"] != true {
		t.Fatalf("a cut outline did not say so: %#v", data)
	}
	if next, _ := result["next"].([]any); len(next) == 0 {
		t.Fatalf("a cut outline offered no way to reach the rest: %#v", result)
	}
	if !strings.Contains(result["summary"].(string), "truncated") {
		t.Fatalf("summary does not mention the cut: %q", result["summary"])
	}
}

// Everything the native sectioner reads - Go, Python, Markdown, TOML -
// outlines without asking the provider at all, and a document it read and
// found nothing in does not ask either.
func TestOutlineOfNativelySectionedFilesStaysNative(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"ledger.go":   "package p\n\nfunc Balance() int { return 0 }\n",
		"plan.md":     "# Plan\n\nwhy\n\n## Waves\n\none\n",
		"config.toml": "[tool.ruff]\nline-length = 100\n",
		"notes.txt":   "nothing to declare\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	backend := &outlineProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "outline", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	outline := func(path string) []map[string]any {
		t.Helper()
		result := handlers.Execute(context.Background(), "req_outline", "read", map[string]any{
			"workspace_id": workspaceID, "view": "outline", "target": map[string]any{"path": path},
		})
		if result["outcome"] != "ok" {
			t.Fatalf("outline of %s = %#v", path, result)
		}
		sections, _ := result["data"].(map[string]any)["sections"].([]map[string]any)
		return sections
	}
	if sections := outline("ledger.go"); len(sections) != 1 || sections[0]["name"] != "Balance" {
		t.Fatalf("Go outline = %#v", sections)
	}
	// A heading is addressed by its path through the document, and the
	// section spans the lines a reader would edit.
	sections := outline("plan.md")
	if len(sections) != 2 || sections[1]["name"] != "Waves" || sections[1]["start_line"] != 5 || sections[1]["end_line"] != 7 {
		t.Fatalf("Markdown outline = %#v", sections)
	}
	if sections := outline("config.toml"); len(sections) != 1 || sections[0]["name"] != "tool/ruff" {
		t.Fatalf("TOML outline = %#v", sections)
	}
	if sections := outline("notes.txt"); len(sections) != 0 {
		t.Fatalf("text outline = %#v", sections)
	}
	if len(backend.asked) != 0 {
		t.Fatalf("the provider was asked about natively sectioned files: %#v", backend.asked)
	}
}
