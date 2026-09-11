package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/provider"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// verificationDiagnosticSettleWait is how long diagnostics may settle
// during verification before the evidence is recorded as is; it stays well
// inside one tool call.
const verificationDiagnosticSettleWait = 1500 * time.Millisecond

type cachedVerification struct {
	Result  workspacecore.VerificationResult
	Outcome string
}

func fullVerificationFallback(result workspacecore.VerificationResult) map[string]any {
	if result.Impact == nil || !result.Impact.RecommendFull || result.Targeted == nil || result.Targeted.Status != "unavailable" {
		return nil
	}
	stages := make([]string, 0, len(result.Stages))
	seen := map[string]bool{}
	for _, stage := range result.Stages {
		if stage.Stage != "" && !seen[stage.Stage] {
			stages = append(stages, stage.Stage)
			seen[stage.Stage] = true
		}
	}
	return map[string]any{
		"tool":                    "verify_run",
		"action":                  "run_full_verification",
		"revision_or_transaction": result.Revision,
		"stages":                  stages,
		"test_scope":              "full",
		"use_new_idempotency_key": true,
	}
}

func modernVerificationEnvelope(requestID string, workspace *workspacecore.Workspace, outcome, code, summary, cache string, result workspacecore.VerificationResult) map[string]any {
	compacted, evidenceIDs := mcpapi.CompactVerificationResult(result)
	envelope := mcpapi.Envelope(requestID, workspace, outcome, code, summary, map[string]any{
		"verification": compacted, "cache": cache,
	})
	envelope["evidence"] = map[string]any{"ids": evidenceIDs, "truncated": false}
	next := make([]any, 0, 2)
	if fallback := fullVerificationFallback(result); fallback != nil {
		next = append(next, fallback)
	}
	if len(evidenceIDs) > 0 {
		next = append(next, map[string]any{
			"tool": "evidence_get", "action": "inspect_verification_evidence",
			"evidence_id": evidenceIDs[0], "evidence_count": len(evidenceIDs),
		})
	}
	if len(next) > 0 {
		envelope["next"] = next
	}
	return envelope
}

// verifyJob carries the state of one verify_run between its two scheduler
// phases: the canonical-lane refresh and stager recovery, and the external
// job that runs the pipeline.
type verifyJob struct {
	request   workspacecore.VerificationRequest
	stages    []string
	testScope string
	revision  string
	cacheKey  string
	identity  workspacecore.Identity
	stager    *providerpool.SandboxStager
}

// verify runs both phases back to back for callers that already hold the
// appropriate lanes; executeScheduled schedules the phases separately.
func (h *toolHandlers) verify(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	job, early := h.verifyPrepare(ctx, requestID, workspace, arguments)
	if early != nil {
		return early
	}
	return h.verifyRun(ctx, requestID, workspace, job)
}

// verifyPrepare resynchronises the canonical workspace, validates the request
// and locates or recovers the prepared stager. It runs in the workspace's
// canonical lane because the document refresh is an external resync.
func (h *toolHandlers) verifyPrepare(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) (*verifyJob, map[string]any) {
	if err := workspace.PrimeDocuments(); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if _, err := workspace.RefreshKnownDocuments(); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if err := h.registry.PersistIdentity(workspace.Identity().ID); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "service_state_persist_failed", err)
	}
	revision := fmt.Sprint(arguments["revision_or_transaction"])
	var stages []string
	for _, value := range mcpapi.AnySlice(arguments["stages"]) {
		stage, ok := value.(string)
		if !ok {
			return nil, mcpapi.Failure(requestID, workspace, "invalid_verification_stage", errors.New("verification stage must be a string"))
		}
		stages = append(stages, stage)
	}
	// For languages without a built-in exact parser, the trusted project check
	// is the declared parser corroborator. Include it when parser verification
	// is requested alone so explicit verification agrees with plan preparation
	// and with language_server_status recovery guidance.
	hasParser, hasCheck := false, false
	for _, stage := range stages {
		hasParser = hasParser || stage == "parser"
		hasCheck = hasCheck || stage == "check"
	}
	if hasParser && !hasCheck {
		if policy, err := workspacecore.LoadPipelinePolicy(workspace.Identity().Root, ""); err == nil && policy.Trusted && len(policy.Check) > 0 {
			stages = append(stages, "check")
		}
	}
	testScope := fmt.Sprint(arguments["test_scope"])
	identity := workspace.Identity()
	job := &verifyJob{
		request: workspacecore.VerificationRequest{
			Stages: stages, Revision: revision, TestScope: testScope,
			TestHistoryPath: filepath.Join(h.stateDir, "test-history", string(identity.ID)+".json"),
		},
		stages: stages, testScope: testScope, revision: revision, identity: identity,
		cacheKey: strings.Join([]string{
			string(identity.ID), revision, strings.Join(stages, "\x1f"), testScope, verificationPolicyFingerprint(identity.Root),
		}, "\x00"),
	}
	cached, cacheHit := h.verification.lookup(job.cacheKey)
	if cacheHit {
		return nil, modernVerificationEnvelope(requestID, workspace, cached.Outcome, "", "Verification reused for the exact revision and stage selection", "revision_hit", cached.Result)
	}
	job.stager = h.pool.PreparedStager(workspace, revision)
	if job.stager == nil {
		recoveredStager, recoveredPlan, recoverable, recoveryErr := h.recoverPreparedStager(ctx, workspace, revision)
		if recoveryErr != nil {
			result := mcpapi.Failure(requestID, workspace, "prepared_revision_recovery_failed", recoveryErr)
			if recoveredPlan.PlanID != "" {
				result["next"] = []any{map[string]any{
					"tool": "change_plan", "action": "prepare", "plan_id": recoveredPlan.PlanID,
					"plan_revision": recoveredPlan.PlanRevision, "use_new_idempotency_key": true,
				}}
			}
			return nil, result
		}
		if recoverable {
			job.stager = recoveredStager
		}
	}
	if job.stager == nil {
		current := fmt.Sprintf("wsrev_%d", identity.StateSeq)
		if revision != current {
			result := mcpapi.Envelope(requestID, workspace, "conflict", "revision_changed",
				"Requested canonical revision is not current", map[string]any{
					"revision_or_transaction": revision, "current_revision": current,
				})
			result["next"] = []any{
				map[string]any{"tool": "revision_diff", "from_revision": revision, "to_revision_or_current": "current"},
				map[string]any{"tool": "verify_run", "revision_or_transaction": current, "stages": stages, "test_scope": testScope, "use_new_idempotency_key": true},
			}
			return nil, result
		}
	}
	return job, nil
}

// verifyRun executes the pipeline for a prepared job as an external job.
func (h *toolHandlers) verifyRun(ctx context.Context, requestID string, workspace *workspacecore.Workspace, job *verifyJob) map[string]any {
	request, stages, testScope, revision, identity, stager := job.request, job.stages, job.testScope, job.revision, job.identity, job.stager
	var result workspacecore.VerificationResult
	var err error
	if stager != nil {
		result, err = stager.Verify(ctx, request)
	} else {
		current := fmt.Sprintf("wsrev_%d", identity.StateSeq)
		sandbox, materializeErr := workspacecore.MaterializeSandbox(
			ctx, identity.Root, h.pool.SandboxBaseDir(), identity.ID,
			"verify_"+requestID, 1, current, workspacecore.DefaultSandboxLimits(),
		)
		if materializeErr != nil {
			return mcpapi.Failure(requestID, workspace, "verification_sandbox_failed", materializeErr)
		}
		files, filesErr := sandbox.BaseStageFiles()
		if filesErr == nil {
			var changedPaths []string
			changedPaths, provenanceErr := h.provenance.CanonicalChangedPaths(identity.ID, identity.StateSeq)
			if provenanceErr != nil && testScope != "affected" {
				for _, file := range files {
					changedPaths = append(changedPaths, file.Path)
				}
			} else {
				filesErr = provenanceErr
			}
			if filesErr == nil {
				changed := make(map[string]bool, len(changedPaths))
				for _, path := range changedPaths {
					changed[path] = true
				}
				filtered := files[:0]
				for _, file := range files {
					if changed[file.Path] {
						filtered = append(filtered, file)
					}
				}
				files = filtered
			}
		}
		if filesErr == nil {
			var policy workspacecore.PipelinePolicy
			policy, filesErr = workspacecore.LoadPipelinePolicy(identity.Root, "")
			var diagnosticProvider provider.Provider
			var diagnosticReport *workspacecore.DiagnosticReport
			providerOpened := false
			wantsDiagnostics := false
			for _, stage := range stages {
				if stage == "diagnostics" {
					wantsDiagnostics = true
					break
				}
			}

			if filesErr == nil && wantsDiagnostics {
				var providerErr error
				diagnosticProvider, providerErr = h.pool.Open(sandbox.Tree, false)
				providerOpened = providerErr == nil
				if providerErr == nil {
					_, providerErr = providerpool.CallCanonical(ctx, "workspace_support_"+requestID, workspace, diagnosticProvider,
						"workspace_support", map[string]any{"root": sandbox.Tree, "attach_wait_ms": providerpool.VerificationAttachWaitMS})
				}
				if providerErr == nil {

					request.DiagnosticVerifier = func(verifyCtx context.Context, revision string, staged []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
						report, evidenceErr := providerpool.RecordDiagnostics(verifyCtx, workspace, diagnosticProvider, staged, revision, "verify_"+requestID, verificationDiagnosticSettleWait)
						diagnosticReport = &report
						return providerpool.DiagnosticVerificationStage(revision, report), evidenceErr
					}
				}
			}
			if filesErr == nil {
				result, err = workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, files)
				if err == nil && diagnosticReport != nil {
					if report, evidenceErr := providerpool.CorroborateWithProjectCheck(workspace, revision, "verify_"+requestID, result.Stages, *diagnosticReport); evidenceErr == nil {
						providerpool.ReplaceDiagnosticVerificationStage(&result, revision, report)
					}
				}
			}
			if providerOpened {
				if closeErr := diagnosticProvider.Close(context.Background()); err == nil && closeErr != nil {
					err = closeErr
				}
			}
		}
		if cleanupErr := sandbox.Cleanup(); err == nil && filesErr == nil && cleanupErr != nil {
			err = cleanupErr
		}
		if filesErr != nil {
			err = filesErr
		}
	}
	if err != nil {
		code, summary := "verification_failed", err.Error()
		resultEnvelope := modernVerificationEnvelope(requestID, workspace, "failed", code, summary, "", result)
		if timeoutCode, timeoutSummary, recovery, ok := verificationTimeoutRecovery(revision, stages, testScope, result); ok {
			resultEnvelope["code"] = timeoutCode
			resultEnvelope["summary"] = timeoutSummary
			next, _ := resultEnvelope["next"].([]any)
			resultEnvelope["next"] = append([]any{recovery}, next...)
		}
		return resultEnvelope
	}
	outcome := "ok"
	for _, stage := range result.Stages {
		if stage.Status == workspacecore.VerificationSkipped {
			outcome = "partial"
			break
		}
	}
	h.verification.store(job.cacheKey, cachedVerification{Result: result, Outcome: outcome})
	return modernVerificationEnvelope(requestID, workspace, outcome, "", "Verification completed against exact sandbox bytes", "revision_miss", result)
}

// maxVerificationCacheEntries bounds the exact-revision verification cache;
// each entry holds full stage output, so the oldest entries are dropped.
const maxVerificationCacheEntries = 32

// verificationCache remembers the result of verifying an exact revision and
// stage selection, so a repeated verify_run answers without re-running the
// pipeline. Entries are evicted oldest first.
type verificationCache struct {
	mu      sync.Mutex
	entries map[string]cachedVerification
	order   []string
}

func newVerificationCache() *verificationCache {
	return &verificationCache{entries: make(map[string]cachedVerification)}
}

func (c *verificationCache) lookup(key string) (cachedVerification, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.entries[key]
	return cached, ok
}

func (c *verificationCache) store(key string, value cachedVerification) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, present := c.entries[key]; !present {
		c.order = append(c.order, key)
	}
	c.entries[key] = value
	for len(c.order) > maxVerificationCacheEntries {
		delete(c.entries, c.order[0])
		c.order = c.order[1:]
	}
}

func verificationPolicyFingerprint(root string) string {
	policy, err := workspacecore.LoadPipelinePolicy(root, "")
	if err != nil {
		return "error:" + err.Error()
	}
	encoded, err := json.Marshal(policy)
	if err != nil {
		return "error:" + err.Error()
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:])
}
