package bridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

type providerPlanStager struct {
	workspaceID workspacecore.ID
	provider    provider.Provider
	epoch       uint64
	requests    atomic.Uint64
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
	deadline, _ := ctx.Deadline()
	return s.provider.Call(ctx, provider.Request{
		Context: provider.RequestContext{
			RequestID: fmt.Sprintf("huyang-plan-%d", s.requests.Add(1)), WorkspaceID: string(s.workspaceID),
			Epoch: s.provider.Descriptor().Epoch, TransactionID: planID, Deadline: deadline,
			Cancellation: s.provider.Descriptor().Cancellation,
		},
		Operation: operation, Arguments: arguments,
	})
}

type sandboxPlanStager struct {
	mu               sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stager != nil {
		return s.stager.Epoch()
	}
	return s.workspace.Identity().Epoch
}

func (s *sandboxPlanStager) PreparationMetadata() (string, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox == nil {
		return "", s.baseRevision, "canonical"
	}
	return s.sandbox.Backend, s.baseRevision, "canonical"
}

func (s *sandboxPlanStager) PreparedRequest() (workspacecore.PlanStageRequest, workspacecore.VerificationResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preparedRevision = revision
	s.verification.Revision = revision
}

func (s *sandboxPlanStager) Verify(ctx context.Context, request workspacecore.VerificationRequest) (workspacecore.VerificationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sandbox == nil || s.done {
		return workspacecore.VerificationResult{}, errors.New("prepared sandbox is not available")
	}
	policy, err := workspacecore.LoadPipelinePolicy(s.workspace.Identity().Root, "")
	if err != nil {
		return workspacecore.VerificationResult{}, err
	}
	request.DiagnosticVerifier = func(verifyCtx context.Context, revision string, files []workspacecore.PlanStageFile) (workspacecore.VerificationStage, error) {
		report, evidenceErr := recordProviderDiagnostics(verifyCtx, s.workspace, s.provider, files, revision, s.planID)
		return diagnosticVerificationStage(revision, report), evidenceErr
	}
	result, err := workspacecore.RunVerificationPipeline(ctx, s.sandbox, policy, request, s.prepared.Files)
	if len(result.Stages) > 0 {
		s.verification.Stages = append(s.verification.Stages, result.Stages...)
	}
	return result, err
}

func (s *sandboxPlanStager) Stage(ctx context.Context, request workspacecore.PlanStageRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return errors.New("sandbox preparation is already closed")
	}
	if s.sandbox != nil {
		return errors.New("sandbox preparation is already staged")
	}
	sandbox, err := workspacecore.MaterializeSandbox(
		ctx, s.workspace.Identity().Root, s.sandboxBase, s.workspace.Identity().ID,
		s.planID, s.planRevision, s.baseRevision, workspacecore.DefaultSandboxLimits(),
	)
	if err != nil {
		return err
	}
	s.sandbox = sandbox
	backend, err := referenceProviders.Open(providerOpenConfig{
		Root: sandbox.Tree, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"),
		RuntimePath: shippedRuntimePath(), Debug: false,
	})
	if err != nil {
		_ = sandbox.Cleanup()
		s.sandbox = nil
		return err
	}
	s.provider = backend
	s.stager = &providerPlanStager{
		workspaceID: s.workspace.Identity().ID, provider: backend, epoch: backend.Descriptor().Epoch,
	}
	if err := s.stager.Stage(ctx, request); err != nil {
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.provider, s.stager, s.sandbox = nil, nil, nil
		return err
	}
	if err := sandbox.ApplyPrepared(request); err != nil {
		_ = s.stager.Rollback(context.Background(), request.PlanID)
		_ = backend.Close(context.Background())
		_ = sandbox.Cleanup()
		s.provider, s.stager, s.sandbox = nil, nil, nil
		return err
	}
	policy, err := workspacecore.LoadPipelinePolicy(s.workspace.Identity().Root, "")
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
		if err := backend.Close(context.Background()); err != nil {
			return err
		}
		replacement, err := referenceProviders.Open(providerOpenConfig{
			Root: sandbox.Tree, InitFile: os.Getenv("AGENT99_HEADLESS_INIT"),
			RuntimePath: shippedRuntimePath(), Debug: false,
		})
		if err != nil {
			return err
		}
		s.provider = replacement
		s.stager = &providerPlanStager{
			workspaceID: s.workspace.Identity().ID, provider: replacement, epoch: replacement.Descriptor().Epoch,
		}
	}
	diagnosticReport, err := recordProviderDiagnostics(ctx, s.workspace, s.provider, request.Files, s.baseRevision, s.planID)
	if err != nil {
		return err
	}
	diagnosticReport, err = corroborateDiagnosticsWithProjectCheck(s.workspace, s.baseRevision, s.planID, verification.Stages, diagnosticReport)
	if err != nil {
		return err
	}
	verification.Stages = append(verification.Stages, diagnosticVerificationStage(s.baseRevision, diagnosticReport))
	s.prepared, s.verification = request, verification
	return nil
}

func (s *sandboxPlanStager) Commit(ctx context.Context, planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil
	}
	var result error
	if s.stager != nil {
		result = s.stager.Commit(ctx, planID)
	}
	if s.provider != nil {
		if err := s.provider.Close(context.Background()); result == nil {
			result = err
		}
	}
	if s.sandbox != nil {
		if err := s.sandbox.Cleanup(); result == nil {
			result = err
		}
	}
	s.done = result == nil
	return result
}

func (s *sandboxPlanStager) Rollback(ctx context.Context, planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil
	}
	var result error
	if s.stager != nil {
		result = s.stager.Rollback(ctx, planID)
	}
	if s.provider != nil {
		if err := s.provider.Close(context.Background()); result == nil {
			result = err
		}
	}
	if s.sandbox != nil {
		if err := s.sandbox.Cleanup(); result == nil {
			result = err
		}
	}
	s.done = result == nil
	return result
}

func (d *directWorkspaces) planStager(workspace *workspacecore.Workspace, planID string, planRevision uint64, create bool) (workspacecore.PlanStager, error) {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	if existing := d.sandboxStagers[planID]; existing != nil {
		existing.mu.Lock()
		reusable := !existing.done && existing.planRevision == planRevision
		existing.mu.Unlock()
		if reusable {
			return existing, nil
		}
		_ = existing.Rollback(context.Background(), planID)
		delete(d.sandboxStagers, planID)
	}
	if !create {
		return nil, errors.New("provider_unavailable: prepared sandbox is not available")
	}
	identity := workspace.Identity()
	stager := &sandboxPlanStager{
		workspace: workspace, sandboxBase: filepath.Join(d.stateDir, "sandboxes"),
		planID: planID, planRevision: planRevision, baseRevision: fmt.Sprintf("wsrev_%d", identity.StateSeq),
	}
	d.sandboxStagers[planID] = stager
	return stager, nil
}

func (d *directWorkspaces) closeProviders() {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	for id, backend := range d.providers {
		_ = backend.Close(context.Background())
		delete(d.providers, id)
		delete(d.stagers, id)
	}
	for planID, stager := range d.sandboxStagers {
		_ = stager.Rollback(context.Background(), planID)
		delete(d.sandboxStagers, planID)
	}
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
			recorded = append(recorded, recordedRevisionDiff{
				from: left, to: right, diff: diff, path: fmt.Sprint(diff["path"]),
				beforeSHA: fmt.Sprint(diff["before_sha256"]), afterSHA: fmt.Sprint(diff["after_sha256"]),
			})
		}
	}
	return recorded
}
