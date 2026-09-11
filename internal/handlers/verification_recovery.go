package handlers

import (
	"fmt"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func verificationTimeoutRecovery(revision string, requestedStages []string, testScope string, result workspacecore.VerificationResult) (string, string, map[string]any, bool) {
	for _, stage := range result.Stages {
		if stage.Status != workspacecore.VerificationTimedOut {
			continue
		}
		stageName := stage.Stage
		if stageName == "" {
			stageName = "configured"
		}
		summary := fmt.Sprintf("Verification timed out in the %s stage after %d ms", stageName, stage.DurationMS)
		recovery := map[string]any{
			"tool": "verify_run", "action": "retry_timed_out_stage",
			"revision_or_transaction": revision, "stages": append([]string(nil), requestedStages...),
			"test_scope": testScope, "use_new_idempotency_key": true,
			"prerequisite": "increase [resource].timeout_seconds or reduce the timed-out command workload",
		}
		return "verification_timed_out", summary, recovery, true
	}
	return "", "", nil, false
}
