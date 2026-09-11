package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// The verification cache key includes the trust policy, so trusting a root
// after a cached run does not replay the untrusted result.
func TestVerificationCacheKeyChangesWhenTrustChanges(t *testing.T) {
	root := t.TempDir()
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "huyang")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[trust]\nroots = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untrusted := verificationPolicyFingerprint(root)
	trustedConfig := []byte("[trust]\nroots = [\"" + root + "\"]\n")
	if err := os.WriteFile(configPath, trustedConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	trusted := verificationPolicyFingerprint(root)
	if untrusted == trusted {
		t.Fatalf("verification cache fingerprint ignored trust change: %s", trusted)
	}
}

// verify_run refreshes the canonical documents before comparing revisions,
// so an external write since the requested revision is a revision conflict
// rather than a verification of stale bytes.
func TestVerifyRunObservesExternalBytesBeforeRevisionCheck(t *testing.T) {
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
	result := direct.handlers.verify(context.Background(), "external-verify", workspace, map[string]any{
		"revision_or_transaction": revision, "stages": []any{"parser"},
	})
	if result["outcome"] != "conflict" || result["code"] != "revision_changed" {
		t.Fatalf("verification observed external bytes under an old revision: %#v", result)
	}
}

// Affected-scope verification of the canonical revision runs the parser on
// the edited file only and reports unavailable diagnostics immediately
// instead of waiting for a provider that cannot start.
func TestVerifyRunAffectedScopeCoversOnlyEditedFileAndReturnsQuickly(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Changed = 1", "Changed = 3", "affected-edit")
	revision := applied["data"].(map[string]any)["revision"].(string)
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	started := time.Now()
	verified := callModern(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "affected-verify",
		"revision_or_transaction": revision,
		"stages":                  []string{"parser", "diagnostics"}, "test_scope": "affected",
	})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("verification waited for an unavailable provider: %s (%#v)", elapsed, verified)
	}
	if verified["outcome"] != "partial" {
		t.Fatalf("parser plus unavailable diagnostics should be partial: %#v", verified)
	}
	stages := verified["data"].(map[string]any)["verification"].(map[string]any)["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("verification stages = %#v", verified)
	}
	parser := stages[0].(map[string]any)
	scope := parser["scope"].([]any)
	if len(scope) != 1 || scope[0] != "changed.go" {
		t.Fatalf("affected parser scope includes unrelated files: %#v", verified)
	}
	if diagnostics := stages[1].(map[string]any); diagnostics["status"] != "unavailable" {
		t.Fatalf("unavailable diagnostics were not reported immediately: %#v", verified)
	}
}

// The changed paths of the target revision come from the receipt that
// produced it, including plan receipts that list several paths.
func TestCanonicalChangedPathsComeFromTheReceiptOfTheTargetRevision(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID := workspacecore.ID("ws_receipt_test")
	direct.receipts.replays[string(workspaceID)+"\x00change_plan\x00receipt"] = &directReplay{
		complete: true,
		result: map[string]any{
			"data": map[string]any{
				"canonical_changed": true,
				"from_revision":     "wsrev_19",
				"revision":          "wsrev_20",
				"changed_paths":     []any{"pkg/a.py", "tests/test_a.py"},
			},
		},
	}
	got, err := direct.receipts.canonicalChangedPaths(workspaceID, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/a.py", "tests/test_a.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %#v, want %#v", got, want)
	}
}

// When the impact graph recommends a full run and affected tests were
// unavailable, the verification envelope offers a runnable full
// verification of the same stages.
func TestVerificationEnvelopeOffersRunnableFullFallback(t *testing.T) {
	graph := workspacecore.ImpactGraph{Revision: "prep_123", RecommendFull: true}
	result := workspacecore.VerificationResult{
		Revision: "prep_123",
		Stages: []workspacecore.VerificationStage{
			{Stage: "check"},
			{Stage: "tests", TestScope: "affected", TestVerdict: "affected_tests_unavailable"},
		},
		Impact:   &graph,
		Targeted: &workspacecore.TargetedTestResult{Status: "unavailable", Graph: graph},
	}
	envelope := modernVerificationEnvelope("req_verify", nil, "partial", "", "verified", "revision_miss", result)
	next := envelope["next"].([]any)
	if len(next) != 1 {
		t.Fatalf("next = %#v, want one full-verification fallback", next)
	}
	action := next[0].(map[string]any)
	if action["tool"] != "verify_run" || action["revision_or_transaction"] != "prep_123" || action["test_scope"] != "full" || action["use_new_idempotency_key"] != true {
		t.Fatalf("fallback action = %#v", action)
	}
	if got := action["stages"].([]string); !reflect.DeepEqual(got, []string{"check", "tests"}) {
		t.Fatalf("fallback stages = %#v", got)
	}
}

// Provider attach and diagnostic settle waits during verification stay
// within a routine tool call.
func TestVerificationProviderWaitsStayBounded(t *testing.T) {
	if verificationProviderAttachWaitMS > 2000 || verificationDiagnosticSettleWait > 2*time.Second {
		t.Fatalf("verification provider waits are not routine-call bounded: attach=%dms settle=%s", verificationProviderAttachWaitMS, verificationDiagnosticSettleWait)
	}
}
