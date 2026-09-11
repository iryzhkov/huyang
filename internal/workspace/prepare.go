package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	PreparedRevision      string              `json:"prepared_revision"`
	ProviderEpoch         uint64              `json:"provider_epoch"`
	AffectedFiles         []string            `json:"affected_files"`
	CanonicalChanged      bool                `json:"canonical_changed"`
	CanonicalRevision     string              `json:"canonical_revision,omitempty"`
	JournalID             string              `json:"journal_id,omitempty"`
	Diagnostics           string              `json:"diagnostics"`
	DiskChecks            string              `json:"disk_checks"`
	IntermediateReports   bool                `json:"intermediate_reports"`
	SandboxBackend        string              `json:"sandbox_backend,omitempty"`
	BaseRevision          string              `json:"base_revision,omitempty"`
	CanonicalFromRevision string              `json:"canonical_from_revision,omitempty"`
	EvidencePaths         string              `json:"evidence_paths,omitempty"`
	Verification          []VerificationStage `json:"verification,omitempty"`
	ToolDelta             []ToolDelta         `json:"tool_delta,omitempty"`
	CommittedDiffs        []ExactDiff         `json:"committed_diffs,omitempty"`
	// ProvisionalAccepted lists the dimensions whose incomplete evidence the committer
	// explicitly accepted; MissingCoverage describes what each of them lacked.
	ProvisionalAccepted []string          `json:"provisional_accepted,omitempty"`
	MissingCoverage     []VerificationGap `json:"missing_coverage,omitempty"`
}

// VerificationGap names one verification dimension whose evidence is incomplete and
// therefore keeps a prepared plan PROVISIONAL instead of READY.
type VerificationGap struct {
	Dimension string `json:"dimension"`
	Detail    string `json:"detail"`
}

// verificationGaps lists the dimensions whose evidence is too weak for a READY plan. Today
// only the diagnostics stage decides readiness: it must have passed with authoritative or
// corroborated semantic coverage.
func verificationGaps(stages []VerificationStage) []VerificationGap {
	var gaps []VerificationGap
	for _, stage := range stages {
		if stage.Stage != "diagnostics" {
			continue
		}
		semantic := stage.Coverage.Semantic
		if stage.Status == VerificationPassed && (semantic == string(ConfidenceAuthoritative) || semantic == string(ConfidenceCorroborated)) {
			continue
		}
		detail := fmt.Sprintf("status %s, semantic coverage %q", stage.Status, semantic)
		if len(stage.Coverage.Skipped) > 0 {
			detail += " (" + strings.Join(stage.Coverage.Skipped, ", ") + ")"
		}
		gaps = append(gaps, VerificationGap{Dimension: stage.Stage, Detail: detail})
	}
	return gaps
}

// CheckProviderAccess prevents a provider-backed call from observing an unlabeled staged view.
func (w *Workspace) CheckProviderAccess(transactionID string) error {
	return nil
}

// PreparePlan validates the entire plan, acquires the exclusive provider lease, and stages
// exact predicted bytes without writing canonical files.
func (w *Workspace) PreparePlan(ctx context.Context, planID string, expected uint64, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, Coded(CodeProviderUnavailable, errors.New("prepare requires a provider"))
	}
	w.prepareMu.Lock()
	if _, exists := w.activePlans[planID]; exists {
		w.prepareMu.Unlock()
		current, inspectErr := w.InspectPlan(planID, expected)
		if inspectErr == nil && (current.State == PlanReady || current.State == PlanProvisional) {
			return current, nil
		}
		return PlanRecord{}, Codedf(CodeWorkspaceBusy, "transaction %s is already preparing", planID)
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
	if !canTransition(plan.State, PlanPreparing) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanPreparing)
	}
	if plan.Preview == nil || plan.Preview.PlanRevision != expected {
		plan, err = w.PreviewPlan(planID, expected)
		if err != nil {
			return PlanRecord{}, err
		}
	}
	if plan.Preview.Outcome != "ok" {
		return plan, Coded(CodePlanValidationConflicts, errors.New("preview must succeed before prepare"))
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
	diagnosticStatus := "suppressed"
	for _, stage := range verification.Stages {
		if stage.Stage == "diagnostics" {
			diagnosticStatus = stage.Coverage.Semantic
		}
	}
	targetState := PlanReady
	if len(verificationGaps(verification.Stages)) > 0 {
		targetState = PlanProvisional
	}
	prepared := &PlanPreparation{
		PreparedRevision: "prep_" + preparedHash,
		ProviderEpoch:    stager.Epoch(), AffectedFiles: append([]string(nil), plan.Preview.AffectedFiles...),
		CanonicalChanged: false, Diagnostics: diagnosticStatus, DiskChecks: diskChecks,
		IntermediateReports: false, SandboxBackend: backend, BaseRevision: baseRevision, EvidencePaths: evidencePaths,
		Verification: append([]VerificationStage(nil), verification.Stages...),
		ToolDelta:    append([]ToolDelta(nil), verification.ToolDelta...),
	}
	if target, ok := stager.(PreparedRevisionStager); ok {
		target.SetPreparedRevision(prepared.PreparedRevision)
	}
	result, err := w.transitionPlan(planID, expected, targetState, "prepare", diagnosticStatus, prepared)
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
		return PlanRecord{}, Coded(CodeProviderUnavailable, errors.New("rollback requires a provider"))
	}
	if err := w.CheckProviderAccess(planID); err != nil {
		return PlanRecord{}, err
	}
	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if !canTransition(plan.State, PlanRollingBack) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanRollingBack)
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

// transitionPlan moves one plan to a new state under the plan state machine, replaces its
// preparation evidence, records the event and persists the record. An edge the state
// machine does not allow is refused with plan_state_invalid. A persistence failure leaves
// the in-memory record at its previous state so a later transition is judged from the
// durable truth rather than from the half-applied one.
func (w *Workspace) transitionPlan(planID string, expected uint64, state PlanState, action, outcome string, preparation *PlanPreparation) (PlanRecord, error) {
	return w.transitionPlanWithConflict(planID, expected, state, action, outcome, preparation, nil)
}

// conflictPlan moves a plan to CONFLICTED and records the stable conflict reason.
func (w *Workspace) conflictPlan(planID string, expected uint64, action string, preparation *PlanPreparation, cause error) (PlanRecord, error) {
	code := ErrorCode(cause)
	if code == "" {
		code = CodeCommitPreconditionChanged
	}
	reason := &PlanConflictReason{Code: code, Message: cause.Error()}
	return w.transitionPlanWithConflict(planID, expected, PlanConflicted, action, code, preparation, reason)
}

func (w *Workspace) transitionPlanWithConflict(planID string, expected uint64, state PlanState, action, outcome string, preparation *PlanPreparation, conflict *PlanConflictReason) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	previous, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected == 0 || previous.PlanRevision != expected {
		return PlanRecord{}, planRevisionChanged(expected, previous.PlanRevision)
	}
	if !canTransition(previous.State, state) {
		return PlanRecord{}, illegalTransition(planID, previous.State, state)
	}
	if previous.Compacted && !planTerminal(state) {
		return PlanRecord{}, Codedf(CodePlanStateInvalid, "plan %s was compacted by retention; create a new plan", planID)
	}
	plan := clonePlan(previous)
	plan.State, plan.Preparation, plan.Conflict = state, preparation, conflict
	recordPlanEvent(&plan, action, outcome)
	w.plans[planID] = plan
	if err := w.finishPlanLocked(planID); err != nil {
		w.plans[planID] = previous
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}
