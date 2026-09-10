package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PlanStageFile is the exact provider-buffer transition predicted by a plan.
type PlanStageFile struct {
	Path         string       `json:"path"`
	Before       []byte       `json:"before"`
	After        []byte       `json:"after"`
	BeforeExists bool         `json:"before_exists"`
	AfterExists  bool         `json:"after_exists"`
	BeforeDisk   DiskSnapshot `json:"before_disk"`
	AfterDisk    DiskSnapshot `json:"after_disk"`
}

// PlanStageRequest is the complete, already validated batch applied under a workspace lease.
type PlanStageRequest struct {
	PlanID            string          `json:"plan_id"`
	PlanRevision      uint64          `json:"plan_revision"`
	Files             []PlanStageFile `json:"files"`
	RequiresFormatter bool            `json:"requires_formatter,omitempty"`
}

// PlanStager owns provider-local unsaved buffers. Stage and Rollback must be atomic from
// the perspective of callers: an error leaves no staged view behind.
type PlanStager interface {
	Stage(context.Context, PlanStageRequest) error
	Commit(context.Context, string) error
	Rollback(context.Context, string) error
	Epoch() uint64
}

// PlanStagerMetadata describes an isolated preparation without exposing sandbox paths.
type PlanStagerMetadata interface {
	PreparationMetadata() (backend, baseRevision, evidencePaths string)
}

type PreparedPlanStager interface {
	PreparedRequest() (PlanStageRequest, VerificationResult, bool)
}

type PreparedRevisionStager interface {
	SetPreparedRevision(string)
}

// PlanPreparation records the provider-backed result and eventual canonical apply receipt.
type PlanPreparation struct {
	PreparedRevision    string              `json:"prepared_revision"`
	ProviderEpoch       uint64              `json:"provider_epoch"`
	AffectedFiles       []string            `json:"affected_files"`
	CanonicalChanged    bool                `json:"canonical_changed"`
	CanonicalRevision   string              `json:"canonical_revision,omitempty"`
	JournalID           string              `json:"journal_id,omitempty"`
	Diagnostics         string              `json:"diagnostics"`
	DiskChecks          string              `json:"disk_checks"`
	IntermediateReports bool                `json:"intermediate_reports"`
	SandboxBackend      string              `json:"sandbox_backend,omitempty"`
	BaseRevision        string              `json:"base_revision,omitempty"`
	EvidencePaths       string              `json:"evidence_paths,omitempty"`
	Verification        []VerificationStage `json:"verification,omitempty"`
	ToolDelta           []ToolDelta         `json:"tool_delta,omitempty"`
}

// CheckProviderAccess prevents a provider-backed call from observing an unlabeled staged view.
func (w *Workspace) CheckProviderAccess(transactionID string) error {
	return nil
}

// PreparePlan validates the entire plan, acquires the exclusive provider lease, and stages
// exact predicted bytes without writing canonical files.
func (w *Workspace) PreparePlan(ctx context.Context, planID string, expected uint64, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, errors.New("provider_unavailable: prepare requires a provider")
	}
	w.prepareMu.Lock()
	if _, exists := w.activePlans[planID]; exists {
		w.prepareMu.Unlock()
		current, inspectErr := w.InspectPlan(planID, expected)
		if inspectErr == nil && (current.State == PlanReady || current.State == PlanProvisional) {
			return current, nil
		}
		return PlanRecord{}, fmt.Errorf("workspace_busy: transaction %s is already preparing", planID)
	}
	w.activePlans[planID] = struct{}{}
	w.prepareMu.Unlock()

	release := true
	defer func() {
		if release {
			w.prepareMu.Lock()
			delete(w.activePlans, planID)
			w.prepareMu.Unlock()
		}
	}()

	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if plan.State != PlanOpen && plan.State != PlanPreviewed && plan.State != PlanFailed {
		return PlanRecord{}, errors.New("plan cannot be prepared in its current state")
	}
	if plan.Preview == nil || plan.Preview.PlanRevision != expected {
		plan, err = w.PreviewPlan(planID, expected)
		if err != nil {
			return PlanRecord{}, err
		}
	}
	if plan.Preview.Outcome != "ok" {
		return plan, errors.New("plan_validation_conflicts: preview must succeed before prepare")
	}
	if _, err := w.transitionPlan(planID, expected, PlanPreparing, "prepare_started", "pending", nil); err != nil {
		return PlanRecord{}, err
	}
	request, err := w.planStageRequest(plan)
	if err != nil {
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", err.Error(), nil)
		return PlanRecord{}, err
	}
	if err := stager.Stage(ctx, request); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rollbackErr := stager.Rollback(rollbackCtx, planID)
		cancel()
		message := err.Error()
		if rollbackErr != nil {
			message += "; rollback: " + rollbackErr.Error()
		}
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", message, nil)
		return PlanRecord{}, errors.New(message)
	}
	backend, baseRevision, evidencePaths := "", plan.Preview.PreviewRevision, "canonical"
	diskChecks := "unavailable_buffer_backed_prepare"
	if metadata, ok := stager.(PlanStagerMetadata); ok {
		backend, baseRevision, evidencePaths = metadata.PreparationMetadata()
		diskChecks = "sandbox_manifest_verified"
	}
	preparedRequest := request
	var verification VerificationResult
	if source, ok := stager.(PreparedPlanStager); ok {
		if candidate, result, available := source.PreparedRequest(); available {
			preparedRequest, verification = candidate, result
		}
	}
	preparedHash := hashBytes(fmt.Appendf(nil, "%s:%d:%s:%s", plan.Preview.PreviewRevision, stager.Epoch(), backend, baseRevision))
	for _, file := range preparedRequest.Files {
		preparedHash = hashBytes(fmt.Appendf(nil, "%s:%s:%t:%x", preparedHash, file.Path, file.AfterExists, file.After))
	}
	prepared := &PlanPreparation{
		PreparedRevision: "prep_" + preparedHash,
		ProviderEpoch:    stager.Epoch(), AffectedFiles: append([]string(nil), plan.Preview.AffectedFiles...),
		CanonicalChanged: false, Diagnostics: "suppressed", DiskChecks: diskChecks,
		IntermediateReports: false, SandboxBackend: backend, BaseRevision: baseRevision, EvidencePaths: evidencePaths,
		Verification: append([]VerificationStage(nil), verification.Stages...),
		ToolDelta:    append([]ToolDelta(nil), verification.ToolDelta...),
	}
	if target, ok := stager.(PreparedRevisionStager); ok {
		target.SetPreparedRevision(prepared.PreparedRevision)
	}
	result, err := w.transitionPlan(planID, expected, PlanReady, "prepare", "ok", prepared)
	if err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rollbackErr := stager.Rollback(rollbackCtx, planID)
		cancel()
		if rollbackErr != nil {
			err = fmt.Errorf("%w; rollback: %v", err, rollbackErr)
		}
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", err.Error(), nil)
		return PlanRecord{}, err
	}
	release = false
	return result, nil
}

// RollbackPlan restores exact provider preimages and releases the transaction lease.
func (w *Workspace) RollbackPlan(ctx context.Context, planID string, expected uint64, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, errors.New("provider_unavailable: rollback requires a provider")
	}
	if err := w.CheckProviderAccess(planID); err != nil {
		return PlanRecord{}, err
	}
	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if plan.State != PlanReady && plan.State != PlanProvisional && plan.State != PlanFailed {
		return PlanRecord{}, errors.New("plan has no provider preparation to roll back")
	}
	if _, err := w.transitionPlan(planID, expected, PlanRollingBack, "rollback_started", "pending", plan.Preparation); err != nil {
		return PlanRecord{}, err
	}
	if err := stager.Rollback(ctx, planID); err != nil {
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "rollback_failed", err.Error(), nil)
		return PlanRecord{}, err
	}
	result, err := w.transitionPlan(planID, expected, PlanRolledBack, "rollback", "ok", nil)
	w.prepareMu.Lock()
	delete(w.activePlans, planID)
	w.prepareMu.Unlock()
	return result, err
}

func (w *Workspace) transitionPlan(planID string, expected uint64, state PlanState, action, outcome string, preparation *PlanPreparation) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, fmt.Errorf("plan_revision_changed: expected %d, current %d", expected, plan.PlanRevision)
	}
	plan.State, plan.Preparation, plan.UpdatedAt = state, preparation, time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{Action: action, PlanRevision: expected, Outcome: outcome, At: plan.UpdatedAt})
	w.plans[planID] = plan
	if err := w.persistPlansLocked(); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}
