package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// PlanStageFile is the exact provider-buffer transition predicted by a plan.
type PlanStageFile struct {
	Path         string `json:"path"`
	Before       []byte `json:"before"`
	After        []byte `json:"after"`
	BeforeExists bool         `json:"before_exists"`
	AfterExists  bool         `json:"after_exists"`
	BeforeDisk   DiskSnapshot `json:"before_disk"`
	AfterDisk    DiskSnapshot `json:"after_disk"`
}

// PlanStageRequest is the complete, already validated batch applied under a workspace lease.
type PlanStageRequest struct {
	PlanID       string          `json:"plan_id"`
	PlanRevision uint64          `json:"plan_revision"`
	Files        []PlanStageFile `json:"files"`
}

// PlanStager owns provider-local unsaved buffers. Stage and Rollback must be atomic from
// the perspective of callers: an error leaves no staged view behind.
type PlanStager interface {
	Stage(context.Context, PlanStageRequest) error
	Commit(context.Context, string) error
	Rollback(context.Context, string) error
	Epoch() uint64
}

// PlanPreparation records the provider-backed result and eventual canonical apply receipt.
type PlanPreparation struct {
	PreparedRevision    string   `json:"prepared_revision"`
	ProviderEpoch       uint64   `json:"provider_epoch"`
	AffectedFiles       []string `json:"affected_files"`
	CanonicalChanged    bool     `json:"canonical_changed"`
	CanonicalRevision   string   `json:"canonical_revision,omitempty"`
	JournalID           string   `json:"journal_id,omitempty"`
	Diagnostics         string   `json:"diagnostics"`
	DiskChecks          string   `json:"disk_checks"`
	IntermediateReports bool     `json:"intermediate_reports"`
}

// CheckProviderAccess prevents a provider-backed call from observing an unlabeled staged view.
func (w *Workspace) CheckProviderAccess(transactionID string) error {
	w.prepareMu.Lock()
	defer w.prepareMu.Unlock()
	if w.activePlan != "" && w.activePlan != transactionID {
		return fmt.Errorf("workspace_busy: transaction %s holds the provider lease", w.activePlan)
	}
	return nil
}

// PreparePlan validates the entire plan, acquires the exclusive provider lease, and stages
// exact predicted bytes without writing canonical files.
func (w *Workspace) PreparePlan(ctx context.Context, planID string, expected uint64, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, errors.New("provider_unavailable: prepare requires a provider")
	}
	w.prepareMu.Lock()
	if w.activePlan != "" {
		owner := w.activePlan
		w.prepareMu.Unlock()
		if owner == planID {
			current, inspectErr := w.InspectPlan(planID, expected)
			if inspectErr == nil && (current.State == PlanReady || current.State == PlanProvisional) {
				return current, nil
			}
		}
		return PlanRecord{}, fmt.Errorf("workspace_busy: transaction %s holds the provider lease", owner)
	}
	w.activePlan = planID
	w.prepareMu.Unlock()

	release := true
	defer func() {
		if release {
			w.prepareMu.Lock()
			if w.activePlan == planID {
				w.activePlan = ""
			}
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
	prepared := &PlanPreparation{
		PreparedRevision: "prep_" + hashBytes(fmt.Appendf(nil, "%s:%d", plan.Preview.PreviewRevision, stager.Epoch())),
		ProviderEpoch:    stager.Epoch(), AffectedFiles: append([]string(nil), plan.Preview.AffectedFiles...),
		CanonicalChanged: false, Diagnostics: "suppressed", DiskChecks: "unavailable_buffer_backed_prepare",
		IntermediateReports: false,
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
	if w.activePlan == planID {
		w.activePlan = ""
	}
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
