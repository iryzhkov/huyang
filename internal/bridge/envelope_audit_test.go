package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// TestMain installs the output-schema validator on every envelope the package
// produces, so any test that drives a tool, directly or through a client
// session, fails when the result would violate the advertised schema.
func TestMain(m *testing.M) {
	mcpapi.SetEnvelopeAudit(func(tool string, envelope map[string]any) {
		if err := mcpapi.ValidateOutput(envelope); err != nil {
			panic(fmt.Sprintf("tool %s produced an envelope that violates the output schema: %v\n%#v", tool, err, envelope))
		}
	})
	os.Exit(m.Run())
}

func assertModernOutputValid(t *testing.T, value map[string]any) {
	t.Helper()
	if err := mcpapi.ValidateOutput(value); err != nil {
		t.Fatalf("invalid modern output: %v\n%#v", err, value)
	}
}

func TestFinalizedEnvelopeBoundsNextAndFillsRequiredKeys(t *testing.T) {
	envelope := mcpapi.FinalizeEnvelope("verify_run", map[string]any{
		"request_id": "req_x", "outcome": "ok", "summary": "s",
		"next": []any{map[string]any{"a": 1}, map[string]any{"b": 2}, map[string]any{"c": 3}},
	})
	if next := envelope["next"].([]any); len(next) != mcpapi.MaxNextEntries || next[0].(map[string]any)["a"] != 1 {
		t.Fatalf("next = %#v", next)
	}
	assertModernOutputValid(t, envelope)

	verification := workspacecore.VerificationResult{Revision: "wsrev_1", Stages: []workspacecore.VerificationStage{
		{Stage: "parser", EvidenceIDs: []string{"ev_1"}}, {Stage: "check", EvidenceIDs: []string{"ev_2"}}, {Stage: "tests", EvidenceIDs: []string{"ev_3"}},
	}}
	envelope = mcpapi.FinalizeEnvelope("verify_run", modernVerificationEnvelope("req_v", nil, "ok", "", "done", "revision_miss", verification))
	if ids := envelope["evidence"].(map[string]any)["ids"].([]string); len(ids) != 3 {
		t.Fatalf("evidence ids = %#v", ids)
	}
	if next := envelope["next"].([]any); len(next) != 1 || next[0].(map[string]any)["evidence_count"] != 3 {
		t.Fatalf("verification next = %#v", next)
	}
	assertModernOutputValid(t, envelope)
}

func TestVerifyRunSchedulesRefreshOnLaneAndPipelineAsExternalJob(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspacesWithQuotas(t.TempDir(), 1, 1)
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	identity := opened["workspace"].(workspacecore.Identity)

	// Holding the external-job slot must not stop the canonical-lane phase
	// from answering a stale revision, and holding the workspace lane must
	// block verify_run until it is released.
	releaseJob, err := direct.scheduler.acquire(context.Background(), string(identity.ID), mcpapi.ClassExternalJob)
	if err != nil {
		t.Fatal(err)
	}
	stale := direct.call(context.Background(), "verify_run", map[string]any{
		"workspace_id": string(identity.ID), "idempotency_key": "stale", "stages": []any{"parser"},
		"revision_or_transaction": "wsrev_9",
	})
	releaseJob()
	if stale["code"] != "revision_changed" {
		t.Fatalf("stale verify while external slot busy = %#v", stale)
	}
	releaseLane, err := direct.scheduler.acquire(context.Background(), string(identity.ID), mcpapi.ClassCanonicalWrite)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	blocked := direct.call(ctx, "verify_run", map[string]any{
		"workspace_id": string(identity.ID), "idempotency_key": "blocked", "stages": []any{"parser"},
		"revision_or_transaction": "wsrev_1",
	})
	releaseLane()
	if blocked["code"] != "scheduler_wait_cancelled" || blocked["data"].(map[string]any)["class"] != mcpapi.ClassCanonicalWrite {
		t.Fatalf("verify behind the workspace lane = %#v", blocked)
	}
}
