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

// An operations list outside any repository opens one documents workspace
// over every path it names, so editing two files needs one call and not two.
func TestEditApplyOperationsWithoutWorkspaceOpensEveryDocument(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "config.yaml")
	second := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(first, []byte("mode: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("# notes\nold\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: dir}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	created := filepath.Join(dir, "new", "added.txt")
	result := handlers.Execute(context.Background(), "req_ops", "edit_apply", map[string]any{
		"idempotency_key": "ops",
		"operations": []any{
			map[string]any{"kind": "replace_literal", "path": first, "old": "mode: old", "new": "mode: new"},
			map[string]any{"kind": "replace_literal", "path": second, "old": "old", "new": "new"},
			map[string]any{"kind": "create_file", "path": created, "content": "added\n"},
		},
	})
	if result["outcome"] == "failed" || result["outcome"] == "conflict" {
		t.Fatalf("implicit operations = %#v", result)
	}
	for path, want := range map[string]string{first: "mode: new\n", second: "# notes\nnew\n", created: "added\n"} {
		if content, _ := os.ReadFile(path); string(content) != want {
			t.Fatalf("%s = %q, want %q", path, content, want)
		}
	}
	if result["data"].(map[string]any)["implicit_workspace"] != true {
		t.Fatalf("implicit operations did not say so: %#v", result)
	}
	// A relative path still cannot be resolved without a workspace, and the
	// refusal names the whole call rather than one operation.
	relative := handlers.Execute(context.Background(), "req_rel", "edit_apply", map[string]any{
		"idempotency_key": "rel",
		"operations":      []any{map[string]any{"kind": "replace_literal", "path": "config.yaml", "old": "a", "new": "b"}},
	})
	if relative["code"] != "invalid_target" {
		t.Fatalf("relative path without a workspace = %#v", relative)
	}
}
