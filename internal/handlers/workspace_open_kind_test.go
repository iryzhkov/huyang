package handlers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// workspace_open without a kind opens what the other arguments name: a
// root is a project and files alone are documents. The schema no longer
// requires kind, so both halves are checked.
func TestWorkspaceOpenDefaultsTheKindFromWhatWasSent(t *testing.T) {
	root := t.TempDir()
	document := filepath.Join(root, "notes.md")
	if err := os.WriteFile(document, []byte("# notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	for _, descriptor := range mcpapi.Tools {
		if descriptor.Name == "workspace_open" {
			schema = descriptor.InputSchema
		}
	}
	backend := &stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	cases := []struct {
		arguments map[string]any
		want      workspacecore.Kind
	}{
		{map[string]any{"root": root}, workspacecore.KindProject},
		{map[string]any{"files": []any{document}}, workspacecore.KindDocuments},
	}
	for _, test := range cases {
		if err := mcpapi.ValidateToolArguments(schema, test.arguments); err != nil {
			t.Fatalf("%v was refused by the schema: %v", test.arguments, err)
		}
		opened := handlers.Execute(context.Background(), "req_open", "workspace_open", test.arguments)
		identity, _ := opened["workspace"].(workspacecore.Identity)
		if opened["outcome"] != "ok" || identity.Kind != test.want {
			t.Fatalf("%v opened %#v, want a %s workspace", test.arguments, opened, test.want)
		}
	}
	refused := handlers.Execute(context.Background(), "req_nothing", "workspace_open", map[string]any{})
	if refused["code"] != "invalid_workspace_kind" || !strings.Contains(refused["summary"].(string), "root") {
		t.Fatalf("an open that names nothing = %#v", refused)
	}
}
