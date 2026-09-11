package bridge

import (
	"context"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
)

type editDiagnosticProvider struct {
	descriptor    provider.Descriptor
	authoritative bool
	lastOperation string
}

func (p *editDiagnosticProvider) Descriptor() provider.Descriptor { return p.descriptor }
func (p *editDiagnosticProvider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy}
}
func (p *editDiagnosticProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.lastOperation = request.Operation
	if request.Operation != "huyang_diagnostic_evidence" {
		return provider.Result{Value: map[string]any{"ok": true}}, nil
	}
	batches := []any{}
	if p.authoritative {
		files, _ := request.Arguments["files"].([]string)
		document := ""
		if len(files) > 0 {
			document = files[0]
		}
		batches = append(batches, map[string]any{
			"kind": "lsp_push", "provider_id": "pyright#1", "producer": "pyright",
			"document": document, "complete": true, "selected": true,
			"dimension": "edited_documents", "change_barrier": true,
		})
	}
	return provider.Result{Value: map[string]any{"batches": batches}}, nil
}
func (p *editDiagnosticProvider) Save(context.Context) error  { return nil }
func (p *editDiagnosticProvider) Close(context.Context) error { return nil }
func (p *editDiagnosticProvider) Done() <-chan struct{}       { return make(chan struct{}) }

type editDiagnosticFactory struct{ backend *editDiagnosticProvider }

func (f editDiagnosticFactory) Open(config providerOpenConfig) (provider.Provider, error) {
	f.backend.descriptor.Root = config.Root
	return f.backend, nil
}
func (f editDiagnosticFactory) Attach(string, string) provider.Provider { return f.backend }
func (editDiagnosticFactory) FindForeign(string) (string, int)          { return "", 0 }
func (editDiagnosticFactory) SweepStale()                               {}

func useEditDiagnosticFactory(t *testing.T, backend *editDiagnosticProvider) {
	t.Helper()
	previous := referenceProviders
	referenceProviders = editDiagnosticFactory{backend: backend}
	t.Cleanup(func() { referenceProviders = previous })
}

func TestDirectEditCapturesDiagnosticsFromHealthyCanonicalProvider(t *testing.T) {
	backend := &editDiagnosticProvider{authoritative: true, descriptor: provider.Descriptor{
		ID: "edit-diagnostics", Backend: "test", Epoch: 1,
		Capabilities: []provider.Capability{provider.CapabilityExecute, provider.CapabilityDiagnostics},
	}}
	useEditDiagnosticFactory(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"main.py": "risk = 1\n"})
	defer direct.closeProviders()

	applied := applyLiteralProbeEdit(t, direct, workspaceID, "risk = 1", "risk = 2", "provider-backed-edit")
	if applied["outcome"] != "ok" {
		t.Fatalf("healthy provider edit remained provisional: %#v", applied)
	}
	verification := applied["data"].(map[string]any)["verification"].(map[string]any)
	if verification["confidence"] != "authoritative" || backend.lastOperation != "huyang_diagnostic_evidence" {
		t.Fatalf("edit omitted current provider diagnostics: %#v", applied)
	}
	if len(applied["evidence"].(map[string]any)["ids"].([]any)) == 0 {
		t.Fatalf("edit omitted diagnostic evidence: %#v", applied)
	}
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	report := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	if report["outcome"] != "ok" || report["data"].(map[string]any)["diagnostics"].(map[string]any)["confidence"] != "authoritative" {
		t.Fatalf("durable diagnostics regressed after edit: %#v", report)
	}
}

func TestDirectEditDiagnosticFallbackIncludesPreciseRetry(t *testing.T) {
	backend := &editDiagnosticProvider{descriptor: provider.Descriptor{
		ID: "edit-diagnostics-empty", Backend: "test", Epoch: 1,
		Capabilities: []provider.Capability{provider.CapabilityExecute, provider.CapabilityDiagnostics},
	}}
	useEditDiagnosticFactory(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"main.py": "risk = 1\n"})
	defer direct.closeProviders()

	applied := applyLiteralProbeEdit(t, direct, workspaceID, "risk = 1", "risk = 2", "provider-empty-edit")
	if applied["outcome"] != "provisional" {
		t.Fatalf("empty provider evidence was promoted: %#v", applied)
	}
	next := applied["next"].([]any)
	if len(next) != 2 || next[0].(map[string]any)["tool"] != "language_server_status" ||
		next[1].(map[string]any)["revision_or_transaction"] != "wsrev_2" {
		t.Fatalf("diagnostic recovery is not actionable: %#v", applied)
	}
}
