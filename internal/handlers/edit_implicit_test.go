package handlers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

// A file outside any repository can be edited and created without a prior
// workspace_open: the edit names the file, a one-document workspace is
// opened implicitly, and the response says so.
func TestEditApplyWithoutWorkspaceOpensTheDocumentImplicitly(t *testing.T) {
	dir := t.TempDir()
	note := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(note, []byte("# Title\n\nold line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: dir}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	result := handlers.Execute(context.Background(), "req_i1", "edit_apply", map[string]any{
		"idempotency_key": "i1", "operation": map[string]any{"kind": "replace_literal", "path": note, "old": "old line", "new": "new line"},
	})
	if result["outcome"] == "failed" || result["outcome"] == "conflict" {
		t.Fatalf("implicit edit = %#v", result)
	}
	content, _ := os.ReadFile(note)
	if string(content) != "# Title\n\nnew line\n" || result["data"].(map[string]any)["implicit_workspace"] != true {
		t.Fatalf("file = %q result = %#v", content, result)
	}
	created := filepath.Join(dir, "scripts", "run.sh")
	result = handlers.Execute(context.Background(), "req_i2", "edit_apply", map[string]any{
		"idempotency_key": "i2", "operation": map[string]any{"kind": "create_file", "path": created, "content": "#!/bin/sh\necho hi\n"},
	})
	if result["outcome"] == "failed" || result["outcome"] == "conflict" {
		t.Fatalf("implicit create = %#v", result)
	}
	if content, _ := os.ReadFile(created); string(content) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("created file = %q", content)
	}
	missing := handlers.Execute(context.Background(), "req_i3", "edit_apply", map[string]any{
		"idempotency_key": "i3", "operation": map[string]any{"kind": "replace_range", "content": "x", "target": map[string]any{"handle": "rng_none"}},
	})
	if missing["code"] != "workspace_required" {
		t.Fatalf("range edit without workspace = %#v", missing)
	}
}
