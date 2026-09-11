package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// TestMain installs the output-schema validator on every envelope the package
// produces, so any test that drives a tool, directly or through a client
// session, fails when the result would violate the advertised schema.
func TestMain(m *testing.M) {
	envelopeAudit = func(tool string, envelope map[string]any) {
		if err := validateModernOutput(envelope); err != nil {
			panic(fmt.Sprintf("tool %s produced an envelope that violates the output schema: %v\n%#v", tool, err, envelope))
		}
	}
	os.Exit(m.Run())
}

func validateModernOutput(value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return err
	}
	return validateSchemaValue(outputEnvelopeSchema(), decoded, "result")
}

func assertModernOutputValid(t *testing.T, value map[string]any) {
	t.Helper()
	if err := validateModernOutput(value); err != nil {
		t.Fatalf("invalid modern output: %v\n%#v", err, value)
	}
}

func TestEveryRegisteredToolDeclaresASchedulerClass(t *testing.T) {
	if err := validateModernRegistry(); err != nil {
		t.Fatal(err)
	}
	want := map[string]schedulerClass{
		"workspace_open": scheduleProviderRead, "workspace_inspect": scheduleProviderRead,
		"search": schedulePureRead, "symbol_find": scheduleProviderRead, "navigate": scheduleProviderRead,
		"read": schedulePureRead, "diagnostics": schedulePureRead, "code_actions": scheduleProviderRead,
		"edit_apply": scheduleCanonicalWrite, "change_plan": scheduleSandboxWrite, "verify_run": scheduleExternalJob,
		"revision_diff": scheduleCanonicalWrite, "evidence_get": schedulePureRead,
		"language_server_status": scheduleProviderRead, "language_server_setup": scheduleCanonicalWrite,
		"debug_session": scheduleProviderRead, "debug_breakpoints": scheduleProviderRead,
		"debug_control": scheduleProviderRead, "debug_inspect": scheduleProviderRead,
	}
	for _, descriptor := range modernTools {
		if !knownSchedulerClass(descriptor.Class) {
			t.Errorf("tool %s has no scheduler class", descriptor.Name)
		}
		if expected, ok := want[descriptor.Name]; !ok {
			t.Errorf("tool %s is not covered by the class table; add it here and on the descriptor", descriptor.Name)
		} else if descriptor.Class != expected {
			t.Errorf("tool %s class = %s, want %s", descriptor.Name, descriptor.Class, expected)
		}
		if modernSchedulerClass(descriptor.Name) != descriptor.Class {
			t.Errorf("modernSchedulerClass(%s) disagrees with the descriptor", descriptor.Name)
		}
	}
	for name := range want {
		if modernSchedulerClass(name) == scheduleCanonicalWrite && want[name] != scheduleCanonicalWrite {
			t.Errorf("tool %s is missing from the registry", name)
		}
	}
	if got := classForCall("change_plan", map[string]any{"action": "inspect"}); got != scheduleCanonicalWrite {
		t.Errorf("change_plan inspect class = %s", got)
	}
	if got := classForCall("change_plan", map[string]any{"action": "prepare"}); got != scheduleSandboxWrite {
		t.Errorf("change_plan prepare class = %s", got)
	}
	if got := classForCall("read", map[string]any{"target": map[string]any{"symbol_locator": map[string]any{}}}); got != scheduleProviderRead {
		t.Errorf("symbol read class = %s", got)
	}
	if got := classForCall("read", map[string]any{"target": map[string]any{"path": "a"}}); got != schedulePureRead {
		t.Errorf("path read class = %s", got)
	}
	missing := modernTool{Name: "unregistered", InputSchema: schemaObject(map[string]any{})}
	previous := modernTools
	modernTools = append(append([]modernTool(nil), previous...), missing)
	defer func() { modernTools = previous }()
	if err := validateModernRegistry(); err == nil || !strings.Contains(err.Error(), "scheduler class") {
		t.Errorf("registry accepted a tool without a class: %v", err)
	}
}

func TestTransactionStateEnumMatchesWorkspacePlanStates(t *testing.T) {
	schema := outputEnvelopeSchema()
	transaction := schema["properties"].(map[string]any)["transaction"].(map[string]any)
	state := transaction["properties"].(map[string]any)["state"].(map[string]any)
	var advertised []string
	for _, value := range state["enum"].([]any) {
		advertised = append(advertised, value.(string))
	}
	sort.Strings(advertised)
	workspaceStates := []workspacecore.PlanState{
		workspacecore.PlanOpen, workspacecore.PlanPreviewed, workspacecore.PlanPreparing, workspacecore.PlanConflicted,
		workspacecore.PlanFailed, workspacecore.PlanProvisional, workspacecore.PlanReady, workspacecore.PlanCommitting,
		workspacecore.PlanCommitted, workspacecore.PlanRecoveryRequired, workspacecore.PlanRollingBack,
		workspacecore.PlanRolledBack, workspacecore.PlanExpired, workspacecore.PlanDiscarded,
	}
	var expected []string
	for _, value := range workspaceStates {
		expected = append(expected, string(value))
	}
	sort.Strings(expected)
	if strings.Join(advertised, ",") != strings.Join(expected, ",") {
		t.Fatalf("transaction.state enum = %v, want %v", advertised, expected)
	}
}

func TestFinalizedEnvelopeBoundsNextAndFillsRequiredKeys(t *testing.T) {
	envelope := finalizeEnvelope("verify_run", map[string]any{
		"request_id": "req_x", "outcome": "ok", "summary": "s",
		"next": []any{map[string]any{"a": 1}, map[string]any{"b": 2}, map[string]any{"c": 3}},
	})
	if next := envelope["next"].([]any); len(next) != maxNextEntries || next[0].(map[string]any)["a"] != 1 {
		t.Fatalf("next = %#v", next)
	}
	assertModernOutputValid(t, envelope)

	verification := workspacecore.VerificationResult{Revision: "wsrev_1", Stages: []workspacecore.VerificationStage{
		{Stage: "parser", EvidenceIDs: []string{"ev_1"}}, {Stage: "check", EvidenceIDs: []string{"ev_2"}}, {Stage: "tests", EvidenceIDs: []string{"ev_3"}},
	}}
	envelope = finalizeEnvelope("verify_run", modernVerificationEnvelope("req_v", nil, "ok", "", "done", "revision_miss", verification))
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
	releaseJob, err := direct.scheduler.acquire(context.Background(), string(identity.ID), scheduleExternalJob)
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
	releaseLane, err := direct.scheduler.acquire(context.Background(), string(identity.ID), scheduleCanonicalWrite)
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
	if blocked["code"] != "scheduler_wait_cancelled" || blocked["data"].(map[string]any)["class"] != scheduleCanonicalWrite {
		t.Fatalf("verify behind the workspace lane = %#v", blocked)
	}
}
