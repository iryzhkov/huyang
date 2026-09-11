package bridge

import (
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// An edit addressed by a handle the service no longer knows fails with
// handle_resolve_failed and tells the caller to repeat the query for a fresh
// handle rather than guessing a range.
func TestEditApplyUnknownHandleSuggestsFreshHandle(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "before\n"})
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "edit_apply", map[string]any{
		"workspace_id":    workspaceID,
		"idempotency_key": "expired-handle",
		"operation":       map[string]any{"kind": "replace_range", "target": map[string]any{"handle": "rng_pre_restart"}, "content": "after"},
	})
	if result["code"] != "handle_resolve_failed" {
		t.Fatalf("unknown handle result = %#v", result)
	}
	next := result["next"].([]any)
	if len(next) != 2 || !strings.Contains(next[1].(map[string]any)["action"].(string), "fresh_handle") {
		t.Fatalf("unknown handle recovery = %#v", result)
	}
}

// The post-edit diagnostic refresh reuses a healthy canonical provider by
// resynchronising it; it does not spawn a second provider.
func TestEditApplyReusesWarmCanonicalProviderForDiagnostics(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"main.go": "package main\nvar risk = 1\n"})
	defer direct.closeProviders()

	session, cleanup := connectOfficialClient(t, mcpapi.ProfileFull, direct)
	defer cleanup()
	callModern(t, session, "language_server_status", map[string]any{"workspace_id": workspaceID})
	if backend.opens != 1 {
		t.Fatalf("provider opens before edit = %d, want 1", backend.opens)
	}
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "risk = 1", "risk = missing", "warm-provider-resync")
	if applied["outcome"] != "ok" || backend.opens != 1 {
		t.Fatalf("edit did not diagnose through the warm resynced provider: opens=%d result=%#v", backend.opens, applied)
	}
}

// A healthy provider that reports complete diagnostics makes the edit ok
// with authoritative verification, evidence IDs, and a durable report.
func TestEditApplyCapturesAuthoritativeDiagnosticsFromHealthyProvider(t *testing.T) {
	backend := newStubProvider()
	useStubProvider(t, backend)
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
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	report := callModern(t, session, "diagnostics", map[string]any{"workspace_id": workspaceID})
	if report["outcome"] != "ok" || report["data"].(map[string]any)["diagnostics"].(map[string]any)["confidence"] != "authoritative" {
		t.Fatalf("durable diagnostics regressed after edit: %#v", report)
	}
}

// A provider that returns no diagnostic batches leaves the edit provisional
// and the recovery names the exact revision to verify.
func TestEditApplyWithEmptyDiagnosticsStaysProvisionalWithExactRetry(t *testing.T) {
	backend := newStubProvider()
	backend.authoritative = false
	useStubProvider(t, backend)
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
