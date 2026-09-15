package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// recordingProvider answers like the stub and remembers every operation,
// so a test can see what the handlers asked the kernel for. late is what
// huyang_late_evidence hands back, once.
type recordingProvider struct {
	stubProvider
	mu   sync.Mutex
	ops  []string
	late []any
}

func (p *recordingProvider) Call(_ context.Context, request provider.Request) (provider.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ops = append(p.ops, request.Operation)
	if request.Operation == "huyang_late_evidence" {
		batches := p.late
		p.late = nil
		return provider.Result{Value: map[string]any{"batches": batches}}, nil
	}
	return provider.Result{Value: map[string]any{}}, nil
}

func (p *recordingProvider) operations() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ops...)
}

// Opening a project workspace starts its language servers in the
// background: the open returns without waiting, and the warm probe follows
// once per provider generation.
func TestWorkspaceOpenWarmsLanguageServersOnce(t *testing.T) {
	// The open starts a canonical provider only when a kernel runtime is
	// shipped; the fixed factory answers with the stub regardless.
	t.Setenv("HUYANG_RUNTIME_PATH", t.TempDir())
	root := t.TempDir()
	backend := &recordingProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	for _, key := range []string{"open1", "open2"} {
		opened := handlers.Execute(context.Background(), "req_"+key, "workspace_open", map[string]any{"kind": "project", "root": root})
		if opened["outcome"] != "ok" {
			t.Fatalf("open = %#v", opened)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		warm := 0
		for _, operation := range backend.operations() {
			if operation == "workspace_support" {
				warm++
			}
		}
		if warm == 1 && time.Now().After(deadline.Add(-2500*time.Millisecond)) {
			return
		}
		if warm > 1 {
			t.Fatalf("warm probe ran %d times for one provider generation", warm)
		}
		if time.Now().After(deadline) {
			t.Fatalf("warm probe never ran; operations = %v", backend.operations())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Findings a server published after an unavailable verdict reach the
// ledger through the diagnostics tool, attributed to the transaction the
// kernel matched by version, without starting a provider.
func TestDiagnosticsCollectsLateEvidenceFromARunningProvider(t *testing.T) {
	root := t.TempDir()
	backend := &recordingProvider{stubProvider: stubProvider{descriptor: provider.Descriptor{ID: "stub", Backend: "test", Epoch: 1, Root: root}}}
	handlers := newTestHandlers(t, fixedFactory{backend: backend})
	opened := handlers.Execute(context.Background(), "req_open", "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	// A provider is running for the workspace, as it would be after any
	// edit; the diagnostics tool must not start one itself.
	_, release, err := handlers.pool.Canonical(context.Background(), handlers.registry.Lookup(workspacecore.ID(workspaceID)))
	defer release()
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.late = []any{map[string]any{
		"kind": "lsp_push", "provider_id": "pyright#late", "producer": "pyright", "document": root + "/main.py",
		"transaction_id": "edit_req_earlier", "complete": true, "selected": true, "dimension": "edited_documents", "reason": "late_attach",
		"findings":   []any{map[string]any{"range": map[string]any{"start_line": 1, "start_character": 1, "end_line": 1, "end_character": 5}, "severity": 1, "message": "late finding"}},
		"candidates": []any{map[string]any{"transaction_id": "edit_req_earlier", "postimage_version_match": true}},
	}}
	backend.mu.Unlock()
	result := handlers.Execute(context.Background(), "req_diag", "diagnostics", map[string]any{"workspace_id": workspaceID, "full": true})
	encoded := fmt.Sprintf("%#v", result)
	if !strings.Contains(encoded, "late finding") || !strings.Contains(encoded, "edit_req_earlier") {
		t.Fatalf("late finding did not reach the ledger: %s", encoded)
	}
	if backend.late != nil {
		t.Fatal("late evidence was not collected from the kernel")
	}
}

// The provisional wording names the missing server and the files instead
// of a generic sentence, and keeps the reason code for the agent.
func TestProvisionalSummaryNamesTheReason(t *testing.T) {
	files := []workspacecore.PlanStageFile{{Path: "main.py", AfterExists: true}}
	cases := map[string]string{
		"lsp_not_configured":           "no language server is configured for main.py",
		"lsp_not_startable":            "is not installed",
		"lsp_starting":                 "still starting",
		"lsp_attach_deadline_exceeded": "within the wait",
		"something_else":               "remain incomplete (something_else)",
	}
	for reason, want := range cases {
		got := provisionalSummary(workspacecore.DiagnosticReport{ProvisionalReasons: []string{reason}}, files)
		if !strings.Contains(got, want) {
			t.Fatalf("summary for %s = %q, want it to contain %q", reason, got, want)
		}
	}
}
