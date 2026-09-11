package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type hardeningR8Provider struct {
	descriptor provider.Descriptor
	opens      int
}

func (p *hardeningR8Provider) Descriptor() provider.Descriptor { return p.descriptor }
func (p *hardeningR8Provider) Health(context.Context) provider.Health {
	return provider.Health{State: provider.HealthHealthy, Epoch: p.descriptor.Epoch}
}
func (p *hardeningR8Provider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	switch request.Operation {
	case "workspace_support":
		return provider.Result{Value: map[string]any{"languages": []any{}}}, nil
	case "huyang_diagnostic_evidence":
		files, _ := request.Arguments["files"].([]string)
		document := ""
		if len(files) > 0 {
			document = files[0]
		}
		return provider.Result{Value: map[string]any{"batches": []any{map[string]any{
			"kind": "lsp_push", "provider_id": "gopls#1", "producer": "gopls",
			"document": document, "complete": true, "selected": true,
			"dimension": "edited_documents", "change_barrier": true,
		}}}}, nil
	case "install_language":
		return provider.Result{Value: map[string]any{"server": map[string]any{
			"status": "failed", "note": "Mason install failed; inspect :MasonLog and retry",
		}}}, nil
	case "find_symbol":
		return provider.Result{Value: map[string]any{"matches": []any{map[string]any{
			"file": "model.go", "name_path": "Shipment", "kind": "struct", "lines": "3-5",
		}}}}, nil
	default:
		return provider.Result{Value: map[string]any{}}, nil
	}
}
func (p *hardeningR8Provider) Save(context.Context) error  { return nil }
func (p *hardeningR8Provider) Close(context.Context) error { return nil }
func (p *hardeningR8Provider) Done() <-chan struct{}       { return make(chan struct{}) }

type hardeningR8Factory struct{ backend *hardeningR8Provider }

func (f hardeningR8Factory) Open(config providerOpenConfig) (provider.Provider, error) {
	f.backend.opens++
	f.backend.descriptor.Root = config.Root
	f.backend.descriptor.Epoch = uint64(f.backend.opens)
	return f.backend, nil
}
func (f hardeningR8Factory) Attach(string, string) provider.Provider { return f.backend }
func (hardeningR8Factory) FindForeign(string) (string, int)          { return "", 0 }
func (hardeningR8Factory) SweepStale()                               {}

func useHardeningR8Factory(t *testing.T, backend *hardeningR8Provider) {
	t.Helper()
	previous := referenceProviders
	referenceProviders = hardeningR8Factory{backend: backend}
	t.Cleanup(func() { referenceProviders = previous })
}

func newHardeningR8Provider() *hardeningR8Provider {
	return &hardeningR8Provider{descriptor: provider.Descriptor{
		ID: "hardening-r8", Backend: "test",
		Capabilities: []provider.Capability{
			provider.CapabilityExecute, provider.CapabilityNavigation, provider.CapabilityDiagnostics,
		},
	}}
}

func TestDirectEditResyncsWarmCanonicalProviderBeforeDiagnostics(t *testing.T) {
	backend := newHardeningR8Provider()
	useHardeningR8Factory(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"main.go": "package main\nvar risk = 1\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	callModern(t, session, "language_server_status", map[string]any{"workspace_id": workspaceID})
	if backend.opens != 1 {
		t.Fatalf("provider opens before edit = %d, want 1", backend.opens)
	}
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "risk = 1", "risk = missing", "r8-provider-resync")
	if applied["outcome"] != "ok" || backend.opens != 1 {
		t.Fatalf("edit did not diagnose through the warm resynced provider: opens=%d result=%#v", backend.opens, applied)
	}
}

func TestFailedLanguageServerInstallCannotReportAttached(t *testing.T) {
	backend := newHardeningR8Provider()
	useHardeningR8Factory(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"CMakeLists.txt": "project(sample)\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	result := callModern(t, session, "language_server_setup", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "r8-install-failure",
		"action": "install", "language": "cmake", "server": "cmake-language-server",
	})
	if result["outcome"] != "failed" || result["code"] != "language_server_install_failed" {
		t.Fatalf("failed provider installation was promoted: %#v", result)
	}
	if len(result["next"].([]any)) == 0 {
		t.Fatalf("failed installation omitted recovery: %#v", result)
	}
}

func TestLanguageServerAttachmentAcceptsProviderServerName(t *testing.T) {
	if !languageServerAttachmentConfirmed("rust_analyzer") || !languageServerAttachmentConfirmed(true) {
		t.Fatal("positive provider attachment was not recognized")
	}
	for _, value := range []any{nil, false, "", "none", "false"} {
		if languageServerAttachmentConfirmed(value) {
			t.Fatalf("false attachment %#v was recognized", value)
		}
	}
}

func TestPlanCreateResolvesProviderBackedSymbolWithoutPriorFind(t *testing.T) {
	backend := newHardeningR8Provider()
	useHardeningR8Factory(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"model.go": "package dispatch\n\ntype Shipment struct {\n\tID string\n}\n",
	})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	result := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "r8-symbol-plan", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-shipment", "kind": "replace_symbol",
			"target":  map[string]any{"symbol_locator": map[string]any{"path": "model.go", "name_path": "Shipment"}},
			"content": "type Shipment struct {\n\tID string\n\tPriority int\n}\n",
		}},
	})
	if result["outcome"] != "ok" {
		t.Fatalf("provider-backed locator failed without prior symbol_find: %#v", result)
	}
	plan := result["data"].(map[string]any)["plan"].(map[string]any)
	operation := plan["operations"].([]any)[0].(map[string]any)
	target := operation["target"].(map[string]any)
	if target["file_range"] == nil {
		t.Fatalf("symbol locator was not normalized to an exact range: %#v", operation)
	}
}

func TestProjectCheckDoesNotHideMissingLSPReason(t *testing.T) {
	root := t.TempDir()
	workspace, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	document := filepath.Join(root, "Planner.cs")
	report, err := workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
		Kind: workspacecore.EvidencePush, ProviderID: "csharp#1", Producer: "csharp",
		Document: document, DocumentRevision: "prep_1", TransactionID: "plan_1",
		TimedOut: true, Selected: true, Dimension: "edited_documents",
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err = workspace.RecordDiagnosticEvidence(workspacecore.DiagnosticBatch{
		Kind: workspacecore.EvidenceUnavailable, ProviderID: "csharp#1", Producer: "csharp",
		Document: document, DocumentRevision: "prep_1", TransactionID: "plan_1",
		Reason: "lsp_not_configured", Selected: true, Dimension: "edited_documents",
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err = corroborateDiagnosticsWithProjectCheck(workspace, "prep_1", "plan_1", []workspacecore.VerificationStage{{
		Stage: "check", Status: workspacecore.VerificationPassed,
	}}, report)
	if err != nil {
		t.Fatal(err)
	}
	if report.Confidence == workspacecore.ConfidenceCorroborated || diagnosticVerificationStage("prep_1", report).Status == workspacecore.VerificationPassed {
		t.Fatalf("missing LSP was hidden by project check: %#v", report)
	}
}

func TestVerifyRefreshesExternalBytesBeforeRevisionCheck(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{"main.go": "package sample\nvar Value = 1\n"})
	defer direct.closeProviders()
	workspace := direct.get(workspacecore.ID(workspaceID))
	revision := "wsrev_1"
	if got := workspace.Identity().StateSeq; got != 1 {
		revision = fmt.Sprintf("wsrev_%d", got)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package sample\nvar Value = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := direct.verify(context.Background(), "r8-external-verify", workspace, map[string]any{
		"revision_or_transaction": revision, "stages": []any{"parser"},
	})
	if result["outcome"] != "conflict" || result["code"] != "revision_changed" {
		t.Fatalf("verification observed external bytes under an old revision: %#v", result)
	}
}

func TestRevisionDiffRefreshesExternalGapWithoutPriorInspect(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{"main.go": "package sample\n"})
	defer direct.closeProviders()
	workspace := direct.get(workspacecore.ID(workspaceID))
	from := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := direct.revisionDiff("r8-external-diff", workspace, map[string]any{
		"from_revision": from, "to_revision_or_current": "current",
	})
	if result["outcome"] != "partial" || result["code"] != "diff_evidence_incomplete" {
		t.Fatalf("revision diff did not report the external gap precisely: %#v", result)
	}
	gaps := anySlice(result["data"].(map[string]any)["gaps"])
	if len(gaps) != 1 {
		t.Fatalf("external change was not exposed as an exact gap: %#v", result)
	}
}
