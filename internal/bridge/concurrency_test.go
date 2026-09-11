package bridge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// blockingSandboxFactory wraps a provider factory and parks every Open whose
// root lies inside a sandbox tree until release is closed. It simulates a slow
// sandbox provider spawn inside a running plan preparation.
type blockingSandboxFactory struct {
	inner   providerpool.Factory
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (f *blockingSandboxFactory) Open(config providerpool.OpenConfig) (provider.Provider, error) {
	if strings.Contains(config.Root, string(filepath.Separator)+"sandboxes"+string(filepath.Separator)) {
		f.once.Do(func() { close(f.entered) })
		<-f.release
	}
	return f.inner.Open(config)
}

func openTestProject(t *testing.T, direct *directWorkspaces, files map[string]string) (string, *workspacecore.Workspace) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	identity, ok := opened["workspace"].(workspacecore.Identity)
	if !ok || opened["outcome"] != "ok" {
		t.Fatalf("workspace_open = %#v", opened)
	}
	return string(identity.ID), direct.get(identity.ID)
}

// startBlockedPrepare begins a change_plan prepare in workspace A whose
// sandbox provider spawn blocks until the returned release function runs.
func startBlockedPrepare(t *testing.T, inner providerpool.Factory) (*directWorkspaces, string, string, func() map[string]any) {
	t.Helper()
	previous := providerpool.DefaultFactory
	factory := &blockingSandboxFactory{
		inner:   inner,
		release: make(chan struct{}), entered: make(chan struct{}),
	}
	providerpool.DefaultFactory = factory
	t.Cleanup(func() { providerpool.DefaultFactory = previous })

	direct := newDirectWorkspaces(t.TempDir())
	t.Cleanup(direct.closeProviders)
	workspaceA, _ := openTestProject(t, direct, map[string]string{"a.txt": "alpha\n"})
	workspaceB, _ := openTestProject(t, direct, map[string]string{"b.txt": "beta\n"})

	done := make(chan map[string]any, 1)
	go func() {
		done <- direct.call(context.Background(), "change_plan", map[string]any{
			"workspace_id": workspaceA, "idempotency_key": "blocked-prepare", "action": "prepare",
			"operations": []any{map[string]any{"op_id": "create", "kind": "create_file", "path": "new.txt", "content": "created\n"}},
		})
	}()
	select {
	case <-factory.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("sandbox provider spawn never started")
	}
	finish := func() map[string]any {
		close(factory.release)
		select {
		case result := <-done:
			return result
		case <-time.After(30 * time.Second):
			t.Fatal("blocked prepare never finished")
			return nil
		}
	}
	return direct, workspaceA, workspaceB, finish
}

func TestSlowStagerInOneWorkspaceDoesNotStallOtherWorkspaces(t *testing.T) {
	direct, workspaceA, workspaceB, finish := startBlockedPrepare(t, stubProviderFactory{backend: newStubProvider()})
	workspaceBRecord := direct.get(workspacecore.ID(workspaceB))

	promptly := func(name string, run func()) {
		t.Helper()
		finished := make(chan struct{})
		go func() {
			run()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s stalled behind the slow stager in another workspace", name)
		}
	}
	promptly("canonicalProvider(B)", func() {
		if _, err := direct.handlers.providerPool().Canonical(context.Background(), workspaceBRecord); err != nil {
			t.Errorf("canonicalProvider(B): %v", err)
		}
	})
	promptly("restartCanonicalProvider(B)", func() {
		if _, err := direct.handlers.providerPool().Restart(context.Background(), workspaceBRecord); err != nil {
			t.Errorf("restartCanonicalProvider(B): %v", err)
		}
	})
	promptly("planStager lookup", func() {
		if _, err := direct.handlers.providerPool().PlanStager(workspaceBRecord, "missing", 1, false); err == nil {
			t.Error("missing stager lookup succeeded")
		}
	})
	promptly("preparedStager(A) bookkeeping", func() {
		stager := direct.handlers.providerPool().Stager(workspacecore.ID(workspaceA), "blocked-plan")
		for _, candidate := range direct.handlers.providerPool().StagersFor(workspacecore.ID(workspaceA)) {
			stager = candidate
		}
		if stager == nil {
			t.Error("workspace A has no stager while preparing")
			return
		}
		if _, _, available := stager.PreparedRequest(); available {
			t.Error("stager reported a prepared request while still staging")
		}
		if stager.Epoch() == 0 {
			t.Error("epoch unavailable during staging")
		}
	})
	promptly("verify_run(B)", func() {
		result := direct.call(context.Background(), "verify_run", map[string]any{
			"workspace_id": workspaceB, "idempotency_key": "verify-b", "stages": []any{"parser"},
			"revision_or_transaction": "wsrev_1",
		})
		if result["code"] == "request_timeout" || result["code"] == "scheduler_wait_cancelled" {
			t.Errorf("verify_run(B) = %#v", result)
		}
	})
	result := finish()
	if result["outcome"] == "failed" {
		t.Fatalf("blocked prepare = %#v", result)
	}
}

func TestPreparedRevisionIsScopedToItsWorkspace(t *testing.T) {
	direct, workspaceA, workspaceB, finish := startBlockedPrepare(t, stubProviderFactory{backend: newStubProvider()})
	prepared := finish()
	transaction, _ := prepared["transaction"].(map[string]any)
	plan, _ := prepared["data"].(map[string]any)["plan"].(workspacecore.PlanRecord)
	if transaction == nil || plan.Preparation == nil || plan.Preparation.PreparedRevision == "" {
		t.Fatalf("prepare = %#v", prepared)
	}
	revision := plan.Preparation.PreparedRevision

	if stager := direct.handlers.providerPool().PreparedStager(direct.get(workspacecore.ID(workspaceA)), revision); stager == nil {
		t.Fatal("workspace A cannot find its own prepared revision")
	}
	if stager := direct.handlers.providerPool().PreparedStager(direct.get(workspacecore.ID(workspaceB)), revision); stager != nil {
		t.Fatal("workspace B resolved a prepared revision that belongs to workspace A")
	}
	if stager := direct.handlers.providerPool().PreparedStager(direct.get(workspacecore.ID(workspaceB)), plan.PlanID); stager != nil {
		t.Fatal("workspace B resolved a plan ID that belongs to workspace A")
	}
	result := direct.call(context.Background(), "verify_run", map[string]any{
		"workspace_id": workspaceB, "idempotency_key": "cross-verify", "stages": []any{"parser"},
		"revision_or_transaction": revision,
	})
	if result["code"] != "revision_changed" {
		t.Fatalf("cross-workspace verify = %#v", result)
	}
	if identity, _ := result["workspace"].(workspacecore.Identity); string(identity.ID) != workspaceB {
		t.Fatalf("verify was labelled with %q, want %q", identity.ID, workspaceB)
	}
}

func TestReadProxyLinesStopsWhenProxyLoopEnds(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	events := make(chan proxyLineEvent)
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		readProxyLines(reader, 1, events, done)
		close(exited)
	}()
	if _, err := writer.Write([]byte("{\"id\":1}\n")); err != nil {
		t.Fatal(err)
	}
	// Nobody drains events, so the goroutine is parked on the send.
	close(done)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("readProxyLines kept blocking after the proxy loop ended")
	}
}

// silentDiagnosticsFactory opens providers that never publish diagnostics, so
// every prepared plan stays PROVISIONAL.
type silentDiagnosticsFactory struct{}

func (silentDiagnosticsFactory) Open(config providerpool.OpenConfig) (provider.Provider, error) {
	return &stubProvider{descriptor: provider.Descriptor{ID: "silent", Backend: "test", Epoch: 1, Root: config.Root}}, nil
}

func TestApplyRefusesProvisionalPlanWithoutAcceptance(t *testing.T) {
	direct, workspaceA, _, finish := startBlockedPrepare(t, silentDiagnosticsFactory{})
	prepared := finish()
	plan, _ := prepared["data"].(map[string]any)["plan"].(workspacecore.PlanRecord)
	if plan.State != workspacecore.PlanProvisional {
		t.Fatalf("silent provider produced %s, want PROVISIONAL: %#v", plan.State, prepared)
	}
	refused := direct.call(context.Background(), "change_plan", map[string]any{
		"workspace_id": workspaceA, "idempotency_key": "apply-refused", "action": "apply",
		"plan_id": plan.PlanID, "plan_revision": float64(plan.PlanRevision), "prepared_revision": plan.Preparation.PreparedRevision,
	})
	if refused["outcome"] != "conflict" || refused["code"] != workspacecore.CodeProvisionalNotAccepted {
		t.Fatalf("apply without acceptance = %#v", refused)
	}
	next, _ := refused["next"].([]any)
	if len(next) == 0 || next[0].(map[string]any)["accept_provisional"] != true {
		t.Fatalf("refusal next = %#v", next)
	}
	accepted := direct.call(context.Background(), "change_plan", map[string]any{
		"workspace_id": workspaceA, "idempotency_key": "apply-accepted", "action": "apply",
		"plan_id": plan.PlanID, "plan_revision": float64(plan.PlanRevision), "prepared_revision": plan.Preparation.PreparedRevision,
		"accept_provisional": true,
	})
	if accepted["outcome"] != "provisional" || accepted["transaction"].(map[string]any)["state"] != workspacecore.PlanCommitted {
		t.Fatalf("accepted apply = %#v", accepted)
	}
	data := accepted["data"].(map[string]any)
	if accepted, _ := data["provisional_accepted"].([]string); len(accepted) != 1 || accepted[0] != "diagnostics" {
		t.Fatalf("provisional_accepted = %#v", data["provisional_accepted"])
	}
}

func TestProviderFailuresAreClassifiedByKernelCode(t *testing.T) {
	workspace, err := workspacecore.New(workspacecore.KindProject, t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	busy := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "workspace_busy", Message: "staging"})
	if busy["outcome"] != "conflict" || busy["code"] != "workspace_busy" {
		t.Fatalf("workspace_busy = %#v", busy)
	}
	unconfigured := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "lsp_not_configured", Message: "no server"})
	if unconfigured["outcome"] != "unavailable" || unconfigured["code"] != "language_server_unavailable" || len(unconfigured["next"].([]any)) != 1 {
		t.Fatalf("lsp_not_configured = %#v", unconfigured)
	}
	cancelled := modernProviderFailure("req", workspace, "language_server_unavailable", &provider.ProviderError{Code: "provider_cancelled", Message: "cancelled"})
	if cancelled["outcome"] != "failed" || cancelled["code"] != "request_cancelled" {
		t.Fatalf("provider_cancelled = %#v", cancelled)
	}
	plain := modernProviderFailure("req", workspace, "language_server_probe_failed", errors.New("boom"))
	if plain["code"] != "language_server_probe_failed" {
		t.Fatalf("uncoded failure = %#v", plain)
	}
}
