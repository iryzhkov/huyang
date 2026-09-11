package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type providerPlanStager struct {
	workspace *workspacecore.Workspace
	provider  provider.Provider
	epoch     uint64
	requests  atomic.Uint64
}

func (s *providerPlanStager) Epoch() uint64 {
	return s.epoch
}

func (s *providerPlanStager) Stage(ctx context.Context, request workspacecore.PlanStageRequest) error {
	files := make([]map[string]any, 0, len(request.Files))
	for _, file := range request.Files {
		if file.BeforeDisk.Kind != workspacecore.ObjectRegularText && file.AfterDisk.Kind != workspacecore.ObjectRegularText {
			continue
		}
		files = append(files, map[string]any{
			"path": file.Path, "before_b64": base64.StdEncoding.EncodeToString(file.Before),
			"after_b64":     base64.StdEncoding.EncodeToString(file.After),
			"before_exists": file.BeforeExists, "after_exists": file.AfterExists,
		})
	}
	_, err := s.call(ctx, "huyang_prepare", request.PlanID, map[string]any{
		"plan_id": request.PlanID, "plan_revision": request.PlanRevision, "files": files,
	})
	return err
}

func (s *providerPlanStager) Commit(ctx context.Context, planID string) error {
	_, err := s.call(ctx, "huyang_commit", planID, map[string]any{"plan_id": planID})
	return err
}

func (s *providerPlanStager) Rollback(ctx context.Context, planID string) error {
	_, err := s.call(ctx, "huyang_rollback", planID, map[string]any{"plan_id": planID})
	return err
}

func (s *providerPlanStager) call(ctx context.Context, operation, planID string, arguments map[string]any) (provider.Result, error) {
	return callProvider(ctx, s.workspace, s.provider, providerCall{
		RequestID: fmt.Sprintf("huyang-plan-%d", s.requests.Add(1)), TransactionID: planID, Timeout: defaultToolCallTimeout,
	}, operation, arguments)
}

// sandboxPlanStager prepares one plan in an isolated sandbox. Two locks keep
// bookkeeping separate from the long-running work: opMu serialises Stage,
// Verify, Commit and Rollback, each of which may materialise a sandbox, spawn
// a provider and run the pipeline; stateMu guards the small state below and
// is only ever held briefly, so lookups such as PreparedRequest or Epoch never
// wait behind a running operation.
type sandboxPlanStager struct {
	opMu             sync.Mutex
	stateMu          sync.Mutex
	workspace        *workspacecore.Workspace
	sandboxBase      string
	planID           string
	planRevision     uint64
	baseRevision     string
	sandbox          *workspacecore.Sandbox
	provider         provider.Provider
	stager           *providerPlanStager
	prepared         workspacecore.PlanStageRequest
	verification     workspacecore.VerificationResult
	preparedRevision string
	done             bool
}

func (s *sandboxPlanStager) Epoch() uint64 {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.stager != nil {
		return s.stager.Epoch()
	}
	if s.provider != nil {
		return s.provider.Descriptor().Epoch
	}
	return s.workspace.Identity().Epoch
}

func (s *sandboxPlanStager) PreparationMetadata() (string, string, string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.sandbox == nil {
		return "", s.baseRevision, "canonical"
	}
	return s.sandbox.Backend, s.baseRevision, "canonical"
}

func (s *sandboxPlanStager) PreparedRequest() (workspacecore.PlanStageRequest, workspacecore.VerificationResult, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.sandbox == nil || len(s.prepared.Files) == 0 {
		return workspacecore.PlanStageRequest{}, workspacecore.VerificationResult{}, false
	}
	request := s.prepared
	request.Files = append([]workspacecore.PlanStageFile(nil), s.prepared.Files...)
	for index := range request.Files {
		request.Files[index].Before = append([]byte(nil), request.Files[index].Before...)
		request.Files[index].After = append([]byte(nil), request.Files[index].After...)
	}
	result := s.verification
	result.Stages = append([]workspacecore.VerificationStage(nil), result.Stages...)
	result.ToolDelta = append([]workspacecore.ToolDelta(nil), result.ToolDelta...)
	for index := range result.ToolDelta {
		result.ToolDelta[index].Before = append([]byte(nil), result.ToolDelta[index].Before...)
		result.ToolDelta[index].After = append([]byte(nil), result.ToolDelta[index].After...)
	}
	if s.preparedRevision != "" {
		result.Revision = s.preparedRevision
	}
	return request, result, true
}

func (s *sandboxPlanStager) SetPreparedRevision(revision string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.preparedRevision = revision
	s.verification.Revision = revision
}

func (s *sandboxPlanStager) Verify(ctx context.Context, request workspacecore.VerificationRequest) (workspacecore.VerificationResult, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	sandbox, prepared, done := s.snapshot()
	if sandbox == nil || done {
		return workspacecore.VerificationResult{}, workspacecore.Coded(workspacecore.CodeProviderUnavailable, errors.New("prepared sandbox is not available"))
	}
	policy, err := workspacecore.LoadPipelinePolicyForTrustedRoot(sandbox.Tree, s.workspace.Identity().Root, "")
	if err != nil {
		return workspacecore.VerificationResult{}, err
	}
	var diagnosticReport *workspacecore.DiagnosticReport
	request.DiagnosticVerifier = func(verifyCtx context.Context, revision string, files []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
		report, evidenceErr := recordProviderDiagnostics(verifyCtx, s.workspace, s.provider, files, revision, s.planID)
		diagnosticReport = &report
		return diagnosticVerificationStage(revision, report), evidenceErr
	}
	result, err := workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, prepared.Files)
	if err == nil && diagnosticReport != nil {
		if report, evidenceErr := corroborateDiagnosticsWithProjectCheck(s.workspace, request.Revision, s.planID, result.Stages, *diagnosticReport); evidenceErr == nil {
			replaceDiagnosticVerificationStage(&result, request.Revision, report)
		}
	}
	if len(result.Stages) > 0 {
		s.stateMu.Lock()
		s.verification.Stages = append(s.verification.Stages, result.Stages...)
		s.stateMu.Unlock()
	}
	return result, err
}

func (s *sandboxPlanStager) Stage(ctx context.Context, request workspacecore.PlanStageRequest) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if current, _, done := s.snapshot(); done {
		return workspacecore.Coded(workspacecore.CodePlanStateInvalid, errors.New("sandbox preparation is already closed"))
	} else if current != nil {
		return workspacecore.Coded(workspacecore.CodePlanStateInvalid, errors.New("sandbox preparation is already staged"))
	}
	sandbox, err := workspacecore.MaterializeSandbox(
		ctx, s.workspace.Identity().Root, s.sandboxBase, s.workspace.Identity().ID,
		s.planID, s.planRevision, s.baseRevision, workspacecore.DefaultSandboxLimits(),
	)
	if err != nil {
		return err
	}
	s.setResources(sandbox, nil, nil)
	backend, err := openReferenceProvider(sandbox.Tree, false)
	if err != nil {
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return err
	}
	stager := &providerPlanStager{workspace: s.workspace, provider: backend, epoch: backend.Descriptor().Epoch}
	s.setResources(sandbox, backend, stager)
	_, _ = callProvider(ctx, s.workspace, backend, providerCall{
		RequestID: fmt.Sprintf("workspace_support_%s", s.planID), TransactionID: s.planID, Timeout: defaultToolCallTimeout,
	}, "workspace_support", map[string]any{"root": sandbox.Tree})
	if err := stager.Stage(ctx, request); err != nil {
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return err
	}
	if err := sandbox.ApplyPrepared(request); err != nil {
		_ = stager.Rollback(context.Background(), request.PlanID)
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return err
	}
	// The provider staged identical bytes in modified buffers while the
	// coordinator wrote the durable sandbox copy. Commit only the disposable
	// provider transaction now so its buffers are marked synchronized before
	// diagnostics; the sandbox remains the rollback boundary for the plan.
	if err := stager.Commit(ctx, request.PlanID); err != nil {
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return err
	}
	s.setResources(sandbox, backend, nil)
	if err := backend.Close(context.Background()); err != nil {
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return err
	}
	s.setResources(sandbox, nil, nil)
	policy, err := workspacecore.LoadPipelinePolicyForTrustedRoot(sandbox.Tree, s.workspace.Identity().Root, "")
	if err != nil {
		return err
	}
	verification, err := workspacecore.RunVerificationPipeline(ctx, sandbox, policy, workspacecore.VerificationRequest{
		Stages: []string{"format_gate", "parser", "check", "tests"}, Revision: s.baseRevision,
		Transform: request.RequiresFormatter, ApplyConfiguredTransform: true,
	}, request.Files)
	if err != nil {
		return err
	}
	if len(verification.ToolDelta) > 0 {
		request.Files = verification.PreparedFiles
		known := make(map[string]bool, len(request.Files))
		for _, file := range request.Files {
			known[file.Path] = true
		}
		for _, delta := range verification.ToolDelta {
			if known[delta.Path] {
				continue
			}
			file, fileErr := sandbox.StageFileForToolDelta(delta)
			if fileErr != nil {
				return fileErr
			}
			request.Files = append(request.Files, file)
			known[delta.Path] = true
		}
	}
	replacement, err := openReferenceProvider(sandbox.Tree, false)
	if err != nil {
		return err
	}
	s.setResources(sandbox, replacement, nil)
	_, _ = callProvider(ctx, s.workspace, replacement, providerCall{
		RequestID: fmt.Sprintf("workspace_support_%s", s.planID), TransactionID: s.planID, Timeout: defaultToolCallTimeout,
	}, "workspace_support", map[string]any{"root": sandbox.Tree, "attach_wait_ms": verificationProviderAttachWaitMS})
	diagnosticReport, err := recordProviderDiagnostics(ctx, s.workspace, replacement, request.Files, s.baseRevision, s.planID)
	if err != nil {
		return err
	}
	diagnosticReport, err = corroborateDiagnosticsWithProjectCheck(s.workspace, s.baseRevision, s.planID, verification.Stages, diagnosticReport)
	if err != nil {
		return err
	}

	verification.Stages = append(verification.Stages, diagnosticVerificationStage(s.baseRevision, diagnosticReport))
	s.stateMu.Lock()
	s.prepared, s.verification = request, verification
	s.stateMu.Unlock()
	return nil
}

// snapshot reads the bookkeeping state without waiting on a running operation.
func (s *sandboxPlanStager) snapshot() (*workspacecore.Sandbox, workspacecore.PlanStageRequest, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.sandbox, s.prepared, s.done
}

func (s *sandboxPlanStager) setResources(sandbox *workspacecore.Sandbox, backend provider.Provider, stager *providerPlanStager) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.sandbox, s.provider, s.stager = sandbox, backend, stager
}

// reusable reports whether the stager still serves the given plan revision.
func (s *sandboxPlanStager) reusable(planRevision uint64) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return !s.done && s.planRevision == planRevision
}

// release finishes the provider transaction, closes the provider and removes
// the sandbox. It runs under opMu so it cannot interleave with Stage or Verify.
func (s *sandboxPlanStager) release(ctx context.Context, planID string, commit bool) error {
	s.stateMu.Lock()
	done, stager, backend, sandbox := s.done, s.stager, s.provider, s.sandbox
	s.stateMu.Unlock()
	if done {
		return nil
	}
	var result error
	if stager != nil {
		if commit {
			result = stager.Commit(ctx, planID)
		} else {
			result = stager.Rollback(ctx, planID)
		}
	}
	if backend != nil {
		if err := backend.Close(context.Background()); result == nil {
			result = err
		}
	}
	if sandbox != nil {
		if err := sandbox.Cleanup(); result == nil {
			result = err
		}
	}
	s.stateMu.Lock()
	s.done = result == nil
	s.stateMu.Unlock()
	return result
}

func replaceDiagnosticVerificationStage(result *workspacecore.VerificationResult, revision string, report workspacecore.DiagnosticReport) {
	replacement := diagnosticVerificationStage(revision, report)
	for index := range result.Stages {
		if result.Stages[index].Stage == "diagnostics" {
			result.Stages[index] = replacement
			return
		}
	}
	result.Stages = append(result.Stages, replacement)
}

func (s *sandboxPlanStager) Commit(ctx context.Context, planID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.release(ctx, planID, true)
}

func (s *sandboxPlanStager) Rollback(ctx context.Context, planID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.release(ctx, planID, false)
}

// stagerKey identifies a sandbox stager by workspace and plan so a plan ID
// from one workspace can never select another workspace's sandbox.
type stagerKey struct {
	workspace workspacecore.ID
	planID    string
}

// preparedStager returns the stager of one workspace whose plan ID or prepared
// revision matches reference. The map is snapshotted under d.providerMu and
// inspected after it is released; PreparedRequest only takes the stager's
// short state lock, so a stager inside a long Stage never blocks the lookup.
func (d *directWorkspaces) preparedStager(workspace *workspacecore.Workspace, reference string) *sandboxPlanStager {
	workspaceID := workspace.Identity().ID
	d.providerMu.Lock()
	candidates := make([]*sandboxPlanStager, 0, len(d.sandboxStagers))
	if exact := d.sandboxStagers[stagerKey{workspace: workspaceID, planID: reference}]; exact != nil {
		candidates = append(candidates, exact)
	}
	for key, candidate := range d.sandboxStagers {
		if key.workspace == workspaceID && key.planID != reference {
			candidates = append(candidates, candidate)
		}
	}
	d.providerMu.Unlock()
	for _, candidate := range candidates {
		if candidate.workspace.Identity().ID != workspaceID {
			continue
		}
		if candidate.planID == reference {
			return candidate
		}
		if _, result, available := candidate.PreparedRequest(); available && result.Revision == reference {
			return candidate
		}
	}
	return nil
}

func (d *directWorkspaces) planStager(workspace *workspacecore.Workspace, planID string, planRevision uint64, create bool) (workspacecore.PlanStager, error) {
	identity := workspace.Identity()
	key := stagerKey{workspace: identity.ID, planID: planID}
	d.providerMu.Lock()
	existing := d.sandboxStagers[key]
	d.providerMu.Unlock()
	if existing != nil {
		if existing.reusable(planRevision) {
			return existing, nil
		}
		if !create {
			// A lookup with the wrong plan revision must not destroy the
			// sandbox another caller prepared; report the mismatch instead.
			return nil, workspacecore.Codedf(workspacecore.CodePlanRevisionChanged, "prepared sandbox belongs to plan revision %d, not %d", existing.planRevision, planRevision)
		}
		// The stale stager is rolled back outside d.providerMu because the
		// rollback closes a provider and removes a sandbox tree.
		_ = existing.Rollback(context.Background(), planID)
		d.providerMu.Lock()
		if d.sandboxStagers[key] == existing {
			delete(d.sandboxStagers, key)
		}
		d.providerMu.Unlock()
	}
	if !create {
		return nil, workspacecore.Coded(workspacecore.CodeProviderUnavailable, errors.New("prepared sandbox is not available"))
	}
	stager := &sandboxPlanStager{
		workspace: workspace, sandboxBase: filepath.Join(d.stateDir, "sandboxes"),
		planID: planID, planRevision: planRevision, baseRevision: fmt.Sprintf("wsrev_%d", identity.StateSeq),
	}
	d.providerMu.Lock()
	if current := d.sandboxStagers[key]; current != nil && current.reusable(planRevision) {
		d.providerMu.Unlock()
		return current, nil
	}
	d.sandboxStagers[key] = stager
	d.providerMu.Unlock()
	return stager, nil
}

type recordedRevisionDiff struct {
	from, to                  uint64
	path, beforeSHA, afterSHA string
	diff                      any
}

func exactDiffReceiptMap(diff workspacecore.ExactDiff) map[string]any {
	return map[string]any{
		"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256,
		"before": diff.Before, "after": diff.After, "patch": diff.Patch,
	}
}

func (d *directWorkspaces) recordedRevisionDiffs(workspaceID string, fromSeq, toSeq uint64) []recordedRevisionDiff {
	d.replayMu.Lock()
	defer d.replayMu.Unlock()

	var recorded []recordedRevisionDiff
	seen := map[string]bool{}
	prefix := workspaceID + "\x00"
	for key, replay := range d.replays {
		if !replay.complete || !strings.HasPrefix(key, prefix) {
			continue
		}
		data, _ := replay.result["data"].(map[string]any)
		if changed, _ := data["canonical_changed"].(bool); !changed {
			continue
		}
		left, leftErr := workspaceRevisionSequence(fmt.Sprint(data["from_revision"]))
		right, rightErr := workspaceRevisionSequence(fmt.Sprint(data["revision"]))
		if leftErr != nil || rightErr != nil || left < fromSeq || right > toSeq {
			continue
		}

		var diffs []any
		switch {
		case strings.HasPrefix(key, prefix+"edit_apply\x00"):
			change, _ := data["change"].(map[string]any)
			if diff, ok := change["diff"].(map[string]any); ok {
				diffs = []any{diff}
			}
		case strings.HasPrefix(key, prefix+"change_plan\x00"):
			switch plan := data["plan"].(type) {
			case workspacecore.PlanRecord:
				if plan.Preparation != nil {
					for _, diff := range plan.Preparation.CommittedDiffs {
						diffs = append(diffs, exactDiffReceiptMap(diff))
					}
				}
			case map[string]any:
				preparation, _ := plan["preparation"].(map[string]any)
				switch committed := preparation["committed_diffs"].(type) {
				case []any:
					diffs = committed
				case []workspacecore.ExactDiff:
					for _, diff := range committed {
						diffs = append(diffs, exactDiffReceiptMap(diff))
					}
				}
			}
		}
		for _, raw := range diffs {
			diff, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := fmt.Sprint(diff["path"])
			beforeSHA := fmt.Sprint(diff["before_sha256"])
			afterSHA := fmt.Sprint(diff["after_sha256"])
			identity := fmt.Sprintf("%d\x00%d\x00%s\x00%s\x00%s\x00%s", left, right, path, beforeSHA, afterSHA, fmt.Sprint(diff["patch"]))
			if seen[identity] {
				continue
			}
			seen[identity] = true
			recorded = append(recorded, recordedRevisionDiff{
				from: left, to: right, diff: diff, path: path,
				beforeSHA: beforeSHA, afterSHA: afterSHA,
			})
		}
	}
	return recorded
}
