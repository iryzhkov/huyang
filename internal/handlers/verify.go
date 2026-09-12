package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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

// VerifyJob carries the state of one verify_run between its two scheduler
// phases: the canonical-lane refresh and stager recovery, and the external
// job that runs the pipeline.
type VerifyJob struct {
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
func (h *Handlers) verify(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	job, early := h.VerifyPrepare(ctx, requestID, workspace, arguments)
	if early != nil {
		return early
	}
	return h.VerifyRun(ctx, requestID, workspace, job)
}

// verifyRequest is the decoded verify_run call. Stages already include the
// trusted project check when parser verification was requested alone.
type verifyRequest struct {
	revision  string
	stages    []string
	testScope string
}

func decodeVerifyRequest(workspace *workspacecore.Workspace, arguments map[string]any) (verifyRequest, error) {
	request := verifyRequest{
		revision:  fmt.Sprint(arguments["revision_or_transaction"]),
		testScope: fmt.Sprint(arguments["test_scope"]),
	}
	// "current" names whatever the workspace is at now, so an agent that
	// only wants to run the tests does not have to carry a revision token.
	if request.revision == "current" || request.revision == "" || request.revision == "<nil>" {
		request.revision = fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	}
	for _, value := range mcpapi.AnySlice(arguments["stages"]) {
		stage, ok := value.(string)
		if !ok {
			return request, errors.New("verification stage must be a string")
		}
		request.stages = append(request.stages, stage)
	}
	// For languages without a built-in exact parser, the trusted project check
	// is the declared parser corroborator. Include it when parser verification
	// is requested alone so explicit verification agrees with plan preparation
	// and with language_server_status recovery guidance.
	if slices.Contains(request.stages, "parser") && !slices.Contains(request.stages, "check") {
		if policy, err := workspacecore.LoadPipelinePolicy(workspace.Identity().Root, ""); err == nil && policy.Trusted && len(policy.Check) > 0 {
			request.stages = append(request.stages, "check")
		}
	}
	return request, nil
}

// VerifyPrepare resynchronises the canonical workspace, validates the request
// and locates or recovers the prepared stager. It runs in the workspace's
// canonical lane because the document refresh is an external resync.
func (h *Handlers) VerifyPrepare(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) (*VerifyJob, map[string]any) {
	if err := workspace.PrimeDocuments(); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if _, err := workspace.RefreshKnownDocuments(); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if err := h.registry.PersistIdentity(workspace.Identity().ID); err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "service_state_persist_failed", err)
	}
	request, err := decodeVerifyRequest(workspace, arguments)
	if err != nil {
		return nil, mcpapi.Failure(requestID, workspace, "invalid_verification_stage", err)
	}
	identity := workspace.Identity()
	job := &VerifyJob{
		request: workspacecore.VerificationRequest{
			Stages: request.stages, Revision: request.revision, TestScope: request.testScope,
			TestHistoryPath: filepath.Join(h.stateDir, "test-history", string(identity.ID)+".json"),
		},
		stages: request.stages, testScope: request.testScope, revision: request.revision, identity: identity,
		cacheKey: strings.Join([]string{
			string(identity.ID), request.revision, strings.Join(request.stages, "\x1f"), request.testScope, verificationPolicyFingerprint(identity.Root),
		}, "\x00"),
	}
	if cached, cacheHit := h.verification.lookup(job.cacheKey); cacheHit {
		return nil, modernVerificationEnvelope(requestID, workspace, cached.Outcome, "", "Verification reused for the exact revision and stage selection", "revision_hit", cached.Result)
	}
	stager, failure := h.locateVerifyStager(ctx, requestID, workspace, request.revision)
	if failure != nil {
		return nil, failure
	}
	job.stager = stager
	if current := fmt.Sprintf("wsrev_%d", identity.StateSeq); stager == nil && request.revision != current {
		result := mcpapi.Envelope(requestID, workspace, "conflict", "revision_changed",
			"Requested canonical revision is not current", map[string]any{
				"revision_or_transaction": request.revision, "current_revision": current,
			})
		result["next"] = []any{
			map[string]any{"tool": "revision_diff", "from_revision": request.revision, "to_revision_or_current": "current"},
			map[string]any{"tool": "verify_run", "revision_or_transaction": current, "stages": request.stages, "test_scope": request.testScope, "use_new_idempotency_key": true},
		}
		return nil, result
	}
	return job, nil
}

// locateVerifyStager finds the prepared sandbox the revision names, or
// recovers it from the durable plan record after a restart. A nil stager
// with no failure means the revision addresses the canonical tree.
func (h *Handlers) locateVerifyStager(ctx context.Context, requestID string, workspace *workspacecore.Workspace, revision string) (*providerpool.SandboxStager, map[string]any) {
	if stager := h.pool.PreparedStager(workspace, revision); stager != nil {
		return stager, nil
	}
	recovered, plan, recoverable, err := h.recoverPreparedStager(ctx, workspace, revision)
	if err != nil {
		result := mcpapi.Failure(requestID, workspace, "prepared_revision_recovery_failed", err)
		if plan.PlanID != "" {
			result["next"] = []any{map[string]any{
				"tool": "change_plan", "action": "prepare", "plan_id": plan.PlanID,
				"plan_revision": plan.PlanRevision, "use_new_idempotency_key": true,
			}}
		}
		return nil, result
	}
	if recoverable {
		return recovered, nil
	}
	return nil, nil
}

// VerifyRun executes the pipeline for a prepared job as an external job.
func (h *Handlers) VerifyRun(ctx context.Context, requestID string, workspace *workspacecore.Workspace, job *VerifyJob) map[string]any {
	var result workspacecore.VerificationResult
	var err error
	if job.stager != nil {
		result, err = job.stager.Verify(ctx, job.request)
	} else {
		var failure map[string]any
		result, failure, err = h.verifyCanonical(ctx, requestID, workspace, job)
		if failure != nil {
			return failure
		}
	}
	if err != nil {
		resultEnvelope := modernVerificationEnvelope(requestID, workspace, "failed", "verification_failed", err.Error(), "", result)
		if timeoutCode, timeoutSummary, recovery, ok := verificationTimeoutRecovery(job.revision, job.stages, job.testScope, result); ok {
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

// verifyCanonical materialises a throwaway sandbox of the canonical tree,
// narrows the staged files to the paths the receipts say changed, and runs
// the pipeline there. A sandbox that cannot be materialised is answered
// directly; every other failure is returned as the pipeline error.
func (h *Handlers) verifyCanonical(ctx context.Context, requestID string, workspace *workspacecore.Workspace, job *VerifyJob) (workspacecore.VerificationResult, map[string]any, error) {
	identity := job.identity
	sandbox, materializeErr := workspacecore.MaterializeSandbox(
		ctx, identity.Root, h.pool.SandboxBaseDir(), identity.ID,
		"verify_"+requestID, 1, fmt.Sprintf("wsrev_%d", identity.StateSeq), workspacecore.DefaultSandboxLimits(),
	)
	if materializeErr != nil {
		return workspacecore.VerificationResult{}, mcpapi.Failure(requestID, workspace, "verification_sandbox_failed", materializeErr), nil
	}
	var result workspacecore.VerificationResult
	var err error
	files, filesErr := sandbox.BaseStageFiles()
	if filesErr == nil {
		files, filesErr = h.selectChangedFiles(files, identity, job.testScope)
	}
	if filesErr == nil {
		result, filesErr, err = h.runCanonicalPipeline(ctx, requestID, workspace, sandbox, job, files)
	}
	if cleanupErr := sandbox.Cleanup(); err == nil && filesErr == nil && cleanupErr != nil {
		err = cleanupErr
	}
	if filesErr != nil {
		err = filesErr
	}
	return result, nil, err
}

// selectChangedFiles keeps the staged files the receipts record as changed
// at the current revision. Without receipt coverage a full verification
// keeps every file, while an affected-scope verification refuses.
func (h *Handlers) selectChangedFiles(files []workspacecore.PlanStageFile, identity workspacecore.Identity, testScope string) ([]workspacecore.PlanStageFile, error) {
	changedPaths, err := h.provenance.CanonicalChangedPaths(identity.ID, identity.StateSeq)
	if err != nil {
		if testScope == "affected" {
			return nil, err
		}
		return files, nil
	}
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
	return filtered, nil
}

// runCanonicalPipeline loads the policy, attaches a sandbox provider for the
// diagnostics stage when it was requested, and runs the pipeline. The
// second result is a policy load failure, which the caller reports instead
// of the pipeline error.
func (h *Handlers) runCanonicalPipeline(ctx context.Context, requestID string, workspace *workspacecore.Workspace, sandbox *workspacecore.Sandbox, job *VerifyJob, files []workspacecore.PlanStageFile) (workspacecore.VerificationResult, error, error) {
	policy, policyErr := workspacecore.LoadPipelinePolicy(job.identity.Root, "")
	if policyErr != nil {
		return workspacecore.VerificationResult{}, policyErr, nil
	}
	request := job.request
	probe := h.openDiagnosticProbe(ctx, requestID, workspace, sandbox.Tree, slices.Contains(job.stages, "diagnostics"))
	if probe.ready {
		request.DiagnosticVerifier = probe.verifier(workspace, requestID)
	}
	result, err := workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, files)
	if err == nil && probe.report != nil {
		if report, evidenceErr := providerpool.CorroborateWithProjectCheck(workspace, job.revision, "verify_"+requestID, result.Stages, *probe.report); evidenceErr == nil {
			providerpool.ReplaceDiagnosticVerificationStage(&result, job.revision, report)
		}
	}
	if probe.backend != nil {
		if closeErr := probe.backend.Close(context.Background()); err == nil && closeErr != nil {
			err = closeErr
		}
	}
	return result, nil, err
}

// diagnosticProbe is the sandbox provider that records diagnostics during
// a canonical verification. backend is set when a provider was opened and
// must be closed; ready is set when it also attached language servers.
type diagnosticProbe struct {
	backend provider.Provider
	ready   bool
	report  *workspacecore.DiagnosticReport
}

func (h *Handlers) openDiagnosticProbe(ctx context.Context, requestID string, workspace *workspacecore.Workspace, tree string, wanted bool) *diagnosticProbe {
	probe := &diagnosticProbe{}
	if !wanted {
		return probe
	}
	backend, err := h.pool.Open(tree, false)
	if err != nil {
		return probe
	}
	probe.backend = backend
	_, err = providerpool.CallCanonical(ctx, "workspace_support_"+requestID, workspace, backend,
		"workspace_support", map[string]any{"root": tree, "attach_wait_ms": providerpool.VerificationAttachWaitMS})
	probe.ready = err == nil
	return probe
}

func (p *diagnosticProbe) verifier(workspace *workspacecore.Workspace, requestID string) func(context.Context, string, []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
	return func(verifyCtx context.Context, revision string, staged []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
		report, evidenceErr := providerpool.RecordDiagnostics(verifyCtx, workspace, p.backend, staged, revision, "verify_"+requestID, verificationDiagnosticSettleWait)
		p.report = &report
		return providerpool.DiagnosticVerificationStage(revision, report), evidenceErr
	}
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
