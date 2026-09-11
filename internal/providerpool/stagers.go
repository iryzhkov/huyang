package providerpool

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// VerificationAttachWaitMS is how long a sandbox provider is given
// to attach language servers before diagnostics are collected; it stays
// well inside one tool call.
const VerificationAttachWaitMS = 1500

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
	return Call(ctx, s.workspace, s.provider, CallSpec{
		RequestID: fmt.Sprintf("huyang-plan-%d", s.requests.Add(1)), TransactionID: planID, Timeout: DefaultCallTimeout,
	}, operation, arguments)
}

// SandboxStager prepares one plan in an isolated sandbox. Two locks keep
// bookkeeping separate from the long-running work: opMu serialises Stage,
// Verify, Commit and Rollback, each of which may materialise a sandbox, spawn
// a provider and run the pipeline; stateMu guards the small state below and
// is only ever held briefly, so lookups such as PreparedRequest or Epoch never
// wait behind a running operation.
type SandboxStager struct {
	opMu             sync.Mutex
	stateMu          sync.Mutex
	pool             *Pool
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

func (s *SandboxStager) Epoch() uint64 {
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

func (s *SandboxStager) PreparationMetadata() (string, string, string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.sandbox == nil {
		return "", s.baseRevision, "canonical"
	}
	return s.sandbox.Backend, s.baseRevision, "canonical"
}

func (s *SandboxStager) PreparedRequest() (workspacecore.PlanStageRequest, workspacecore.VerificationResult, bool) {
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

func (s *SandboxStager) SetPreparedRevision(revision string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.preparedRevision = revision
	s.verification.Revision = revision
}

func (s *SandboxStager) Verify(ctx context.Context, request workspacecore.VerificationRequest) (workspacecore.VerificationResult, error) {
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
		report, evidenceErr := RecordDiagnostics(verifyCtx, s.workspace, s.provider, files, revision, s.planID)
		diagnosticReport = &report
		return DiagnosticVerificationStage(revision, report), evidenceErr
	}
	result, err := workspacecore.RunVerificationPipeline(ctx, sandbox, policy, request, prepared.Files)
	if err == nil && diagnosticReport != nil {
		if report, evidenceErr := CorroborateWithProjectCheck(s.workspace, request.Revision, s.planID, result.Stages, *diagnosticReport); evidenceErr == nil {
			ReplaceDiagnosticVerificationStage(&result, request.Revision, report)
		}
	}
	if len(result.Stages) > 0 {
		s.stateMu.Lock()
		s.verification.Stages = append(s.verification.Stages, result.Stages...)
		s.stateMu.Unlock()
	}
	return result, err
}

// Stage prepares the plan in three phases: the operations are written into
// a fresh sandbox through a disposable provider transaction, the
// configured formatter and pipeline transform the staged files, and a
// second provider records diagnostics for the transformed bytes.
func (s *SandboxStager) Stage(ctx context.Context, request workspacecore.PlanStageRequest) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if current, _, done := s.snapshot(); done {
		return workspacecore.Coded(workspacecore.CodePlanStateInvalid, errors.New("sandbox preparation is already closed"))
	} else if current != nil {
		return workspacecore.Coded(workspacecore.CodePlanStateInvalid, errors.New("sandbox preparation is already staged"))
	}
	sandbox, err := s.stageIntoSandbox(ctx, request)
	if err != nil {
		return err
	}
	verification, err := s.transformStaged(ctx, sandbox, &request)
	if err != nil {
		return err
	}
	verification, err = s.recordStagedDiagnostics(ctx, sandbox, request.Files, verification)
	if err != nil {
		return err
	}
	s.stateMu.Lock()
	s.prepared, s.verification = request, verification
	s.stateMu.Unlock()
	return nil
}

// stageIntoSandbox materialises the sandbox, stages the operations through
// a provider transaction, writes the durable sandbox copy and commits the
// provider's disposable transaction so its buffers are marked synchronized.
// Any failure removes the sandbox and leaves the stager empty.
func (s *SandboxStager) stageIntoSandbox(ctx context.Context, request workspacecore.PlanStageRequest) (*workspacecore.Sandbox, error) {
	sandbox, err := workspacecore.MaterializeSandbox(
		ctx, s.workspace.Identity().Root, s.sandboxBase, s.workspace.Identity().ID,
		s.planID, s.planRevision, s.baseRevision, workspacecore.DefaultSandboxLimits(),
	)
	if err != nil {
		return nil, err
	}
	s.setResources(sandbox, nil, nil)
	backend, err := s.pool.Open(sandbox.Tree, false)
	if err != nil {
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return nil, err
	}
	stager := &providerPlanStager{workspace: s.workspace, provider: backend, epoch: backend.Descriptor().Epoch}
	s.setResources(sandbox, backend, stager)
	_, _ = Call(ctx, s.workspace, backend, CallSpec{
		RequestID: fmt.Sprintf("workspace_support_%s", s.planID), TransactionID: s.planID, Timeout: DefaultCallTimeout,
	}, "workspace_support", map[string]any{"root": sandbox.Tree})
	abandon := func(err error) (*workspacecore.Sandbox, error) {
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return nil, err
	}
	if err := stager.Stage(ctx, request); err != nil {
		return abandon(err)
	}
	if err := sandbox.ApplyPrepared(request); err != nil {
		_ = stager.Rollback(context.Background(), request.PlanID)
		return abandon(err)
	}
	// The provider staged identical bytes in modified buffers while the
	// coordinator wrote the durable sandbox copy. Commit only the disposable
	// provider transaction now so its buffers are marked synchronized before
	// diagnostics; the sandbox remains the rollback boundary for the plan.
	if err := stager.Commit(ctx, request.PlanID); err != nil {
		return abandon(err)
	}
	s.setResources(sandbox, backend, nil)
	if err := backend.Close(context.Background()); err != nil {
		_ = sandbox.Cleanup()
		s.setResources(nil, nil, nil)
		return nil, err
	}
	s.setResources(sandbox, nil, nil)
	return sandbox, nil
}

// transformStaged runs the formatter and pipeline over the staged files and
// folds every path a tool wrote into the request, so the prepared set is
// exactly what will be committed.
func (s *SandboxStager) transformStaged(ctx context.Context, sandbox *workspacecore.Sandbox, request *workspacecore.PlanStageRequest) (workspacecore.VerificationResult, error) {
	policy, err := workspacecore.LoadPipelinePolicyForTrustedRoot(sandbox.Tree, s.workspace.Identity().Root, "")
	if err != nil {
		return workspacecore.VerificationResult{}, err
	}
	verification, err := workspacecore.RunVerificationPipeline(ctx, sandbox, policy, workspacecore.VerificationRequest{
		Stages: []string{"format_gate", "parser", "check", "tests"}, Revision: s.baseRevision,
		Transform: request.RequiresFormatter, ApplyConfiguredTransform: true,
	}, request.Files)
	if err != nil {
		return verification, err
	}
	if len(verification.ToolDelta) == 0 {
		return verification, nil
	}
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
			return verification, fileErr
		}
		request.Files = append(request.Files, file)
		known[delta.Path] = true
	}
	return verification, nil
}

// recordStagedDiagnostics opens a provider over the transformed sandbox,
// records the diagnostics of the staged files, corroborates them with a
// passing project check, and appends the diagnostics stage.
func (s *SandboxStager) recordStagedDiagnostics(ctx context.Context, sandbox *workspacecore.Sandbox, files []workspacecore.PlanStageFile, verification workspacecore.VerificationResult) (workspacecore.VerificationResult, error) {
	replacement, err := s.pool.Open(sandbox.Tree, false)
	if err != nil {
		return verification, err
	}
	s.setResources(sandbox, replacement, nil)
	_, _ = Call(ctx, s.workspace, replacement, CallSpec{
		RequestID: fmt.Sprintf("workspace_support_%s", s.planID), TransactionID: s.planID, Timeout: DefaultCallTimeout,
	}, "workspace_support", map[string]any{"root": sandbox.Tree, "attach_wait_ms": VerificationAttachWaitMS})
	diagnosticReport, err := RecordDiagnostics(ctx, s.workspace, replacement, files, s.baseRevision, s.planID)
	if err != nil {
		return verification, err
	}
	diagnosticReport, err = CorroborateWithProjectCheck(s.workspace, s.baseRevision, s.planID, verification.Stages, diagnosticReport)
	if err != nil {
		return verification, err
	}
	verification.Stages = append(verification.Stages, DiagnosticVerificationStage(s.baseRevision, diagnosticReport))
	return verification, nil
}

// Sandbox returns the materialised sandbox, or nil before Stage or after
// release. It takes only the short state lock.
func (s *SandboxStager) Sandbox() *workspacecore.Sandbox {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.sandbox
}

// snapshot reads the bookkeeping state without waiting on a running operation.
func (s *SandboxStager) snapshot() (*workspacecore.Sandbox, workspacecore.PlanStageRequest, bool) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return s.sandbox, s.prepared, s.done
}

func (s *SandboxStager) setResources(sandbox *workspacecore.Sandbox, backend provider.Provider, stager *providerPlanStager) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.sandbox, s.provider, s.stager = sandbox, backend, stager
}

// reusable reports whether the stager still serves the given plan revision.
func (s *SandboxStager) reusable(planRevision uint64) bool {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	return !s.done && s.planRevision == planRevision
}

// release finishes the provider transaction, closes the provider and removes
// the sandbox. It runs under opMu so it cannot interleave with Stage or Verify.
func (s *SandboxStager) release(ctx context.Context, planID string, commit bool) error {
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

func ReplaceDiagnosticVerificationStage(result *workspacecore.VerificationResult, revision string, report workspacecore.DiagnosticReport) {
	replacement := DiagnosticVerificationStage(revision, report)
	for index := range result.Stages {
		if result.Stages[index].Stage == "diagnostics" {
			result.Stages[index] = replacement
			return
		}
	}
	result.Stages = append(result.Stages, replacement)
}

func (s *SandboxStager) Commit(ctx context.Context, planID string) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.release(ctx, planID, true)
}

func (s *SandboxStager) Rollback(ctx context.Context, planID string) error {
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
// revision matches reference. The map is snapshotted under p.mu and
// inspected after it is released; PreparedRequest only takes the stager's
// short state lock, so a stager inside a long Stage never blocks the lookup.
func (p *Pool) PreparedStager(workspace *workspacecore.Workspace, reference string) *SandboxStager {
	workspaceID := workspace.Identity().ID
	p.mu.Lock()
	candidates := make([]*SandboxStager, 0, len(p.stagers))
	if exact := p.stagers[stagerKey{workspace: workspaceID, planID: reference}]; exact != nil {
		candidates = append(candidates, exact)
	}
	for key, candidate := range p.stagers {
		if key.workspace == workspaceID && key.planID != reference {
			candidates = append(candidates, candidate)
		}
	}
	p.mu.Unlock()
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

func (p *Pool) PlanStager(workspace *workspacecore.Workspace, planID string, planRevision uint64, create bool) (workspacecore.PlanStager, error) {
	identity := workspace.Identity()
	key := stagerKey{workspace: identity.ID, planID: planID}
	p.mu.Lock()
	existing := p.stagers[key]
	p.mu.Unlock()
	if existing != nil {
		if existing.reusable(planRevision) {
			return existing, nil
		}
		if !create {
			// A lookup with the wrong plan revision must not destroy the
			// sandbox another caller prepared; report the mismatch instead.
			return nil, workspacecore.Codedf(workspacecore.CodePlanRevisionChanged, "prepared sandbox belongs to plan revision %d, not %d", existing.planRevision, planRevision)
		}
		// The stale stager is rolled back outside p.mu because the
		// rollback closes a provider and removes a sandbox tree.
		_ = existing.Rollback(context.Background(), planID)
		p.mu.Lock()
		if p.stagers[key] == existing {
			delete(p.stagers, key)
		}
		p.mu.Unlock()
	}
	if !create {
		return nil, workspacecore.Coded(workspacecore.CodeProviderUnavailable, errors.New("prepared sandbox is not available"))
	}
	stager := &SandboxStager{
		pool: p, workspace: workspace, sandboxBase: p.sandboxBase,
		planID: planID, planRevision: planRevision, baseRevision: fmt.Sprintf("wsrev_%d", identity.StateSeq),
	}
	p.mu.Lock()
	if current := p.stagers[key]; current != nil && current.reusable(planRevision) {
		p.mu.Unlock()
		return current, nil
	}
	p.stagers[key] = stager
	p.mu.Unlock()
	return stager, nil
}
