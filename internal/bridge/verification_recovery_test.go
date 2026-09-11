package bridge

import (
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// A timed-out stage wins over other failures in the verification summary
// and the recovery names the stage, the revision and a fresh key.
func TestVerificationTimeoutRecoveryPrioritizesTimeout(t *testing.T) {
	result := workspacecore.VerificationResult{Stages: []workspacecore.VerificationStage{
		{Stage: "format_gate", Status: workspacecore.VerificationFailed},
		{Stage: "check", Status: workspacecore.VerificationTimedOut, DurationMS: 20006},
	}}
	code, summary, recovery, ok := verificationTimeoutRecovery("wsrev_8", []string{"check"}, "full", result)
	if !ok || code != "verification_timed_out" || !strings.Contains(summary, "check stage after 20006 ms") {
		t.Fatalf("timeout summary is not truthful: %q %q %#v", code, summary, recovery)
	}
	if recovery["action"] != "retry_timed_out_stage" || recovery["revision_or_transaction"] != "wsrev_8" || recovery["use_new_idempotency_key"] != true {
		t.Fatalf("timeout recovery is not precise: %#v", recovery)
	}
}
