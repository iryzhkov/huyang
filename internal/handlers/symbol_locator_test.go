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

// symbolProvider answers find_symbol the way the kernel does: matches carry
// the slash-separated name path, a workspace-relative file and a line
// range. It records every name it was asked for.
type symbolProvider struct {
	stubProvider
	asked []string
}

func (p *symbolProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	if request.Operation != "find_symbol" {
		return provider.Result{Value: map[string]any{}}, nil
	}
	name, _ := request.Arguments["name"].(string)
	p.asked = append(p.asked, name)
	if name != "Shipment/Deliver" {
		return provider.Result{Value: map[string]any{"count": 0, "matches": []any{}}}, nil
	}
	return provider.Result{Value: map[string]any{"count": 1, "matches": []any{map[string]any{
		"file": "model.go", "name_path": "Shipment/Deliver", "kind": "method_declaration", "lines": "7-7",
	}}}}, nil
}

func newSymbolFixture(t *testing.T) (*Handlers, *symbolProvider, string) {
	t.Helper()
	root := t.TempDir()
	source := "package model\n\ntype Shipment struct {\n\tID string\n}\n\nfunc (s *Shipment) Deliver() error { return nil }\n"
	if err := os.WriteFile(filepath.Join(root, "model.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &symbolProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "symbols", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	if opened["outcome"] != "ok" {
		t.Fatalf("open = %#v", opened)
	}
	return handlers, backend, string(opened["workspace"].(workspacecore.Identity).ID)
}

// Every accepted name-path shape becomes the kernel's Parent/Name form.
func TestCanonicalNamePathAcceptsGoAndDottedShapes(t *testing.T) {
	cases := map[string]string{
		"Deliver":             "Deliver",
		"Shipment/Deliver":    "Shipment/Deliver",
		"Shipment.Deliver":    "Shipment/Deliver",
		"(*Shipment).Deliver": "Shipment/Deliver",
		"(Shipment).Deliver":  "Shipment/Deliver",
		"Widget#call":         "Widget/call",
		"Widget::call":        "Widget/call",
		"scripts/build.dev":   "scripts/build.dev",
	}
	for input, want := range cases {
		if got := canonicalNamePath(input); got != want {
			t.Errorf("canonicalNamePath(%q) = %q, want %q", input, got, want)
		}
	}
}

// A Go method addressed as (*Type).Method or Type.Method resolves through
// the provider for read, symbol_find and a change_plan symbol locator.
func TestGoMethodNamePathsResolveThroughProvider(t *testing.T) {
	handlers, backend, workspaceID := newSymbolFixture(t)
	read := handlers.Execute(context.Background(), "req_read", "read", map[string]any{
		"workspace_id": workspaceID,
		"target":       map[string]any{"symbol_locator": map[string]any{"path": "model.go", "name_path": "(*Shipment).Deliver"}},
	})
	if read["outcome"] != "ok" || !strings.HasPrefix(read["data"].(map[string]any)["content"].(string), "func (s *Shipment) Deliver()") {
		t.Fatalf("read by Go receiver path = %#v", read)
	}
	found := handlers.Execute(context.Background(), "req_find", "symbol_find", map[string]any{
		"workspace_id": workspaceID, "query": "Shipment.Deliver",
	})
	handles := found["data"].(map[string]any)["ranked_handles"].([]map[string]any)
	if found["outcome"] != "ok" || len(handles) != 1 || handles[0]["handle"].(workspacecore.HandleRecord).Locator.NamePath != "Shipment/Deliver" {
		t.Fatalf("symbol_find by dotted path = %#v", found)
	}
	created := handlers.Execute(context.Background(), "req_plan", "change_plan", map[string]any{
		"workspace_id": workspaceID, "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-deliver", "kind": "replace_symbol",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": filepath.Join(handlers.registry.Lookup(workspacecore.ID(workspaceID)).Identity().Root, "model.go"), "name_path": "Shipment.Deliver"}},
			"content": "func (s *Shipment) Deliver() error { return errors.New(\"later\") }\n",
		}},
	})
	if created["outcome"] != "ok" {
		t.Fatalf("plan create by dotted path = %#v", created)
	}
	plan := created["data"].(map[string]any)["plan"].(workspacecore.PlanRecord)
	if plan.Operations[0].Target == nil || plan.Operations[0].Target.FileRange == nil {
		t.Fatalf("symbol locator was not normalised to a range: %#v", plan.Operations[0])
	}
	for _, asked := range backend.asked {
		if asked != "Shipment/Deliver" {
			t.Fatalf("provider was asked for %q, want the canonical Shipment/Deliver", asked)
		}
	}
}
