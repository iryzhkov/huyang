package bridge

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"agent99/internal/provider"
	workspacecore "agent99/internal/workspace"
)

func TestOfficialClientPreparesExclusiveUnsavedProviderBuffersAndDiscards(t *testing.T) {
	previousFactory := referenceProviders
	referenceProviders = configuredProviderFactory{backend: "embed"}
	defer func() { referenceProviders = previousFactory }()
	runtimeRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT99_RUNTIME_PATH", runtimeRoot)
	t.Setenv("AGENT99_HEADLESS_INIT", filepath.Join(runtimeRoot, "tests", "minimal_init.lua"))
	root := t.TempDir()
	stateDir := t.TempDir()
	file := filepath.Join(root, "note.txt")
	original := []byte("alpha beta gamma\n")
	if err := os.WriteFile(file, original, 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	defer direct.closeProviders()
	session, cleanup := connectOfficialClient(t, profileFull, direct)
	defer cleanup()
	opened := callModern(t, session, "workspace_open", map[string]any{
		"kind": "documents", "files": []string{file},
	})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": "beta", "mode": "literal",
	})
	hit := searched["data"].(map[string]any)["hits"].([]any)[0].(map[string]any)
	created := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "prepare-create", "action": "create",
		"operations": []any{map[string]any{
			"op_id": "replace-beta", "kind": "replace_range",
			"target": map[string]any{"file_range": hit["range"]}, "content": "DELTA",
		}},
	})
	plan := created["data"].(map[string]any)["plan"].(map[string]any)
	planID := plan["plan_id"].(string)
	planRevision := plan["plan_revision"].(float64)
	prepared := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "prepare", "action": "prepare",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if prepared["outcome"] != "ok" || prepared["transaction"].(map[string]any)["state"] != "READY" {
		t.Fatalf("prepare = %#v", prepared)
	}
	if current, err := os.ReadFile(file); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("prepare changed canonical disk: %q, %v", current, err)
	}
	blocked := callModern(t, session, "debug_session", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "non-owner-provider-call", "action": "start",
	})
	if blocked["outcome"] != "conflict" || blocked["code"] != "workspace_busy" {
		t.Fatalf("non-owner provider call observed staged view: %#v", blocked)
	}
	backend := direct.providers[workspaceIDValue(workspaceID)]
	staged, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "inspect-staged", WorkspaceID: workspaceID, TransactionID: planID},
		Operation: "buffer_lines", Arguments: map[string]any{"file": file, "first": 1, "last": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstProviderLine(staged.Value); got != "1: alpha DELTA gamma" {
		t.Fatalf("provider did not expose staged owner bytes: %q (%#v)", got, staged.Value)
	}
	discarded := callModern(t, session, "change_plan", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "discard", "action": "discard",
		"plan_id": planID, "plan_revision": planRevision,
	})
	if discarded["outcome"] != "ok" || discarded["transaction"].(map[string]any)["state"] != "ROLLED_BACK" {
		t.Fatalf("discard = %#v", discarded)
	}
	restored, err := backend.Call(context.Background(), provider.Request{
		Context:   provider.RequestContext{RequestID: "inspect-restored", WorkspaceID: workspaceID},
		Operation: "buffer_lines", Arguments: map[string]any{"file": file, "first": 1, "last": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := firstProviderLine(restored.Value); got != "1: alpha beta gamma" {
		t.Fatalf("provider rollback did not restore exact preimage: %q (%#v)", got, restored.Value)
	}
}

func workspaceIDValue(value string) workspacecore.ID {
	return workspacecore.ID(value)
}

func firstProviderLine(value any) string {
	result, _ := value.(map[string]any)
	raw, _ := result["lines"].([]any)
	if len(raw) == 0 {
		if strings, ok := result["lines"].([]string); ok && len(strings) > 0 {
			return strings[0]
		}
		return ""
	}
	line, _ := raw[0].(string)
	return line
}

func TestProviderBackedPrepareReceiptIsNotReplayedAfterRestart(t *testing.T) {
	if !providerBackedReplay(map[string]any{"transaction": map[string]any{"state": "READY"}}) {
		t.Fatal("READY receipt would replay after its provider buffers vanished")
	}
	if providerBackedReplay(map[string]any{"transaction": map[string]any{"state": "PREVIEWED"}}) {
		t.Fatal("durable preview receipt was incorrectly invalidated")
	}
}
