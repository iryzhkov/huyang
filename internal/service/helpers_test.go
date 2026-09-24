package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
)

// openProbeProject writes files into a fresh project root, opens it through
// an official client session and returns the service, the workspace ID and
// the root.
func openProbeProject(t *testing.T, files map[string]string) (*directWorkspaces, string, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	direct := newDirectWorkspaces(t.TempDir())
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	t.Cleanup(cleanup)
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	return direct, workspaceID, root
}

// applyLiteralProbeEdit searches for one literal occurrence of query and
// replaces its exact range through edit_apply.
func applyLiteralProbeEdit(t *testing.T, direct *directWorkspaces, workspaceID, query, replacement, key string) map[string]any {
	t.Helper()
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": query, "mode": "literal", "include_ranges": true,
	})
	hits := searched["data"].(map[string]any)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search %q returned %d hits: %#v", query, len(hits), searched)
	}
	return callModern(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": key,
		"operation": map[string]any{
			"kind":    "replace_range",
			"target":  map[string]any{"file_range": hits[0].(map[string]any)["range"]},
			"content": replacement,
		},
	})
}

// stubProvider is an in-process provider that answers the kernel operations
// the handlers issue with fixed, deterministic results. authoritative
// controls whether diagnostic evidence carries a complete batch or none.
type stubProvider struct {
	descriptor    provider.Descriptor
	opens         int
	authoritative bool
	lastOperation string
	// findSymbol, when set, is the find_symbol answer instead of the
	// default single match.
	findSymbol map[string]any
}

func newStubProvider() *stubProvider {
	return &stubProvider{authoritative: true, descriptor: provider.Descriptor{
		ID: "stub-provider", Backend: "test",
		Capabilities: []provider.Capability{
			provider.CapabilityExecute, provider.CapabilityNavigation, provider.CapabilityDiagnostics,
		},
	}}
}

func (p *stubProvider) Descriptor() provider.Descriptor { return p.descriptor }
func (p *stubProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy, Epoch: p.descriptor.Epoch}
}
func (p *stubProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.lastOperation = request.Operation
	switch request.Operation {
	case "workspace_support":
		return provider.Result{Value: map[string]any{"languages": []any{}}}, nil
	case "huyang_diagnostic_evidence":
		batches := []any{}
		if p.authoritative {
			files, _ := request.Arguments["files"].([]string)
			document := ""
			if len(files) > 0 {
				document = files[0]
			}
			batches = append(batches, map[string]any{
				"kind": "lsp_push", "provider_id": "gopls#1", "producer": "gopls",
				"document": document, "complete": true, "selected": true,
				"dimension": "edited_documents", "change_barrier": true,
			})
		}
		return provider.Result{Value: map[string]any{"batches": batches}}, nil
	case "install_language":
		return provider.Result{Value: map[string]any{"server": map[string]any{
			"status": "failed", "note": "Mason install failed; inspect :MasonLog and retry",
		}}}, nil
	case "find_symbol":
		if p.findSymbol != nil {
			return provider.Result{Value: p.findSymbol}, nil
		}
		return provider.Result{Value: map[string]any{"matches": []any{map[string]any{
			"file": "model.go", "name_path": "Shipment", "kind": "struct", "lines": "3-5",
		}}}}, nil
	default:
		return provider.Result{Value: map[string]any{}}, nil
	}
}
func (p *stubProvider) Close(context.Context) error { return nil }
func (p *stubProvider) Done() <-chan struct{}       { return make(chan struct{}) }

// stubProviderFactory hands out one stubProvider for every open and counts
// the opens so a test can assert a warm provider was reused.
type stubProviderFactory struct{ backend *stubProvider }

func (f stubProviderFactory) Open(config providerpool.OpenConfig) (provider.Provider, error) {
	f.backend.opens++
	f.backend.descriptor.Root = config.Root
	f.backend.descriptor.Epoch = uint64(f.backend.opens)
	return f.backend, nil
}

func useStubProvider(t *testing.T, backend *stubProvider) {
	t.Helper()
	previous := providerpool.DefaultFactory
	providerpool.DefaultFactory = stubProviderFactory{backend: backend}
	t.Cleanup(func() { providerpool.DefaultFactory = previous })
}
