package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

// A Go file still outlines natively, without asking the provider at all.
func TestOutlineOfGoFileStaysNative(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ledger.go"), []byte("package p\n\nfunc Balance() int { return 0 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &outlineProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "outline", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	result := handlers.Execute(context.Background(), "req_outline", "read", map[string]any{
		"workspace_id": string(opened["workspace"].(workspacecore.Identity).ID),
		"view":         "outline", "target": map[string]any{"path": "ledger.go"},
	})
	sections := result["data"].(map[string]any)["sections"].([]map[string]any)
	if len(sections) != 1 || sections[0]["name"] != "Balance" || len(backend.asked) != 0 {
		t.Fatalf("native outline = %#v, provider calls = %#v", sections, backend.asked)
	}
}
