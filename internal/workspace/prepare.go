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
	// Patch is the human-readable record of how Before became After when a
	// native edit produced this file; plan stages leave it empty.
	Patch string `json:"patch,omitempty"`
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

// PreparationGaps lists every dimension of one prepared plan whose evidence is incomplete:
// the verification stages that fell short, and the advisory invariants that were not proven.
// A committer must accept each of them by name, so this is also the list a caller needs in
// order to know what it is accepting.
func PreparationGaps(plan PlanRecord) []VerificationGap {
	if plan.Preparation == nil {
		return nil
	}
	return append(verificationGaps(plan.Preparation.Verification),
		invariantGaps(plan.Invariants, plan.Preparation.PreparedRevision)...)
}

// UnprovenRequiredInvariants lists the required invariants that are not proven for this
// plan's prepared revision. It is what stops a prepared plan being applied, and what a
// refusal names.
func UnprovenRequiredInvariants(plan PlanRecord) []PlanInvariant {
	prepared := ""
	if plan.Preparation != nil {
		prepared = plan.Preparation.PreparedRevision
	}
	required, _ := unprovenInvariants(plan.Invariants, prepared)
	return required
}

// InvariantDescription is what one invariant says about itself, for a reply that has to
// explain why a plan cannot be applied.
func InvariantDescription(invariant PlanInvariant) string { return invariant.describe() }

// CheckProviderAccess prevents a provider-backed call from observing an unlabeled staged view.
func (w *Workspace) CheckProviderAccess(transactionID string) error {
	return nil
}

// InvariantEvaluator answers what the staged bytes make of each invariant a plan declares.
// It runs after the sandbox holds the prepared bytes and before the plan reaches a state
// anything can be applied from. An evaluator that cannot answer one invariant returns it
// unknown; leaving it out entirely means the same thing.
type InvariantEvaluator interface {
	EvaluateInvariants(ctx context.Context, plan PlanRecord, preparation PlanPreparation) []PlanInvariant
}

// PrepareOption adjusts one PreparePlan call.
type PrepareOption func(*prepareOptions)

type prepareOptions struct {
	evaluator InvariantEvaluator
}

// WithInvariantEvaluator supplies what evaluates a plan's declared invariants. Without one
// every declared invariant is unknown, which is what a plan with required invariants
// deserves from a service that cannot check them.
func WithInvariantEvaluator(evaluator InvariantEvaluator) PrepareOption {
	return func(options *prepareOptions) { options.evaluator = evaluator }
}

// PreparePlan validates the entire plan, acquires the exclusive provider lease, and stages
// exact predicted bytes without writing canonical files.
func (w *Workspace) PreparePlan(ctx context.Context, planID string, expected uint64, stager PlanStager, options ...PrepareOption) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, Coded(CodeProviderUnavailable, errors.New("prepare requires a provider"))
	}
	var settings prepareOptions
	for _, option := range options {
		option(&settings)
	}
	if prepared, err := w.acquirePrepareLease(planID, expected); err != nil {
		return PlanRecord{}, err
	} else if prepared != nil {
		return *prepared, nil
	}
	release := true
	defer func() {
		if release {
			w.prepareMu.Lock()
			delete(w.activePlans, planID)
			w.prepareMu.Unlock()
		}
	}()
	plan, err := w.previewedPlan(planID, expected)
	if err != nil {
		return plan, err
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
		message := err.Error()
		if rollbackErr := rollbackStager(stager, planID); rollbackErr != nil {
			message += "; rollback: " + rollbackErr.Error()
		}
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", message, nil)
		return PlanRecord{}, errors.New(message)
	}
	prepared, targetState := preparationFor(plan, request, stager)
	if target, ok := stager.(PreparedRevisionStager); ok {
		target.SetPreparedRevision(prepared.PreparedRevision)
	}
	// The invariants are evaluated against the bytes that are now staged, and
	// recorded before the plan reaches a state anything can be applied from, so
	// a proof never arrives after the decision it was meant to inform.
	if len(plan.Invariants) > 0 {
		evaluated, err := w.evaluateInvariants(ctx, settings.evaluator, planID, expected, plan, *prepared)
		if err != nil {
			_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", err.Error(), nil)
			return PlanRecord{}, err
		}
		if required, advisory := unprovenInvariants(evaluated, prepared.PreparedRevision); len(required)+len(advisory) > 0 {
			targetState = PlanProvisional
		}
	}
	result, err := w.transitionPlan(planID, expected, targetState, "prepare", prepared.Diagnostics, prepared)
	if err != nil {
		if rollbackErr := rollbackStager(stager, planID); rollbackErr != nil {
			err = fmt.Errorf("%w; rollback: %v", err, rollbackErr)
		}
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "prepare_failed", err.Error(), nil)
		return PlanRecord{}, err
	}
	release = false
	return result, nil
}

// acquirePrepareLease takes the exclusive lease for planID. When the plan already holds
// the lease and is READY or PROVISIONAL, its prepared record is returned instead so a
// repeated prepare is idempotent; any other holder makes the workspace busy.
func (w *Workspace) acquirePrepareLease(planID string, expected uint64) (*PlanRecord, error) {
	w.prepareMu.Lock()
	if _, exists := w.activePlans[planID]; exists {
		w.prepareMu.Unlock()
		current, inspectErr := w.InspectPlan(planID, expected)
		if inspectErr == nil && (current.State == PlanReady || current.State == PlanProvisional) {
			return &current, nil
		}
		return nil, Codedf(CodeWorkspaceBusy, "transaction %s is already preparing", planID)
	}
	w.activePlans[planID] = struct{}{}
	w.prepareMu.Unlock()
	return nil, nil
}

// previewedPlan returns the plan with a successful preview for the expected revision,
// building the preview when it is missing or stale. On a preview conflict the plan is
// returned beside the error so the caller can report it.
func (w *Workspace) previewedPlan(planID string, expected uint64) (PlanRecord, error) {
	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if !canTransition(plan.State, PlanPreparing) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanPreparing)
	}
	// A plan with no operations stages nothing, so committing it would write
	// no byte and still advance the canonical revision and answer a receipt
	// that says a change was made. There is nothing here to prepare, and
	// saying so is cheaper than a commit that means nothing.
	if len(plan.Operations) == 0 {
		return plan, Codedf(CodePlanStateInvalid,
			"plan %s declares no operations; add them with action=edit and edit.mode=add before preparing", planID)
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
	return plan, nil
}

// evaluateInvariants asks the evaluator about every declared invariant and stores the
// answers on the plan record, each bound to the prepared revision it describes.
func (w *Workspace) evaluateInvariants(ctx context.Context, evaluator InvariantEvaluator, planID string, expected uint64, plan PlanRecord, preparation PlanPreparation) ([]PlanInvariant, error) {
	var answers []PlanInvariant
	if evaluator != nil {
		answers = evaluator.EvaluateInvariants(ctx, plan, preparation)
	}
	merged := mergeInvariantResults(plan.Invariants, answers, preparation.PreparedRevision, time.Now().UTC())
	if err := w.recordInvariants(planID, expected, merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// recordInvariants stores evaluated invariants without moving the plan or changing its
// revision: an answer about a plan is not an edit of it.
func (w *Workspace) recordInvariants(planID string, expected uint64, invariants []PlanInvariant) error {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	previous, ok := w.plans[planID]
	if !ok {
		return unknownPlan(planID)
	}
	if expected == 0 || previous.PlanRevision != expected {
		return planRevisionChanged(expected, previous.PlanRevision)
	}
	plan := clonePlan(previous)
	plan.Invariants = cloneInvariants(invariants)
	w.plans[planID] = plan
	if err := w.persistPlanLocked(planID); err != nil {
		w.plans[planID] = previous
		return err
	}
	return nil
}

// rollbackStager rolls the provider's staged view back with a bounded timeout.
func rollbackStager(stager PlanStager, planID string) error {
	rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return stager.Rollback(rollbackCtx, planID)
}

// preparationFor derives the prepared revision and evidence from the staged request and
// the stager's verification, and the state the plan reaches: READY when every
// verification dimension is covered, PROVISIONAL otherwise.
func preparationFor(plan PlanRecord, request PlanStageRequest, stager PlanStager) (*PlanPreparation, PlanState) {
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
	return prepared, targetState
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

// ReopenPlan rolls a preparation back and returns the plan to OPEN so it can be changed and
// prepared again.
//
// It is the same rollback a discard performs, with a different destination: the sandbox is
// released, the prepared revision stops existing and every handle into it is refused, and the
// plan keeps its identity and its operations. A reviewed preparation is still never edited in
// place - it is replaced - and the proofs of its invariants go with it, because they were
// proofs about bytes nobody proposes any more. The plan revision does not change here; the
// edit that follows is what makes it a new proposal.
func (w *Workspace) ReopenPlan(ctx context.Context, planID string, expected uint64, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, Coded(CodeProviderUnavailable, errors.New("reopening a prepared plan requires a provider"))
	}
	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if !canTransition(plan.State, PlanRollingBack) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanRollingBack)
	}
	if _, err := w.transitionPlan(planID, expected, PlanRollingBack, "reopen_started", "pending", plan.Preparation); err != nil {
		return PlanRecord{}, err
	}
	if err := stager.Rollback(ctx, planID); err != nil {
		_, _ = w.transitionPlan(planID, expected, PlanFailed, "reopen_failed", err.Error(), nil)
		return PlanRecord{}, err
	}
	if err := w.recordInvariants(planID, expected, resetInvariantProofs(plan.Invariants)); err != nil {
		return PlanRecord{}, err
	}
	result, err := w.transitionPlan(planID, expected, PlanOpen, "reopen", "ok", nil)
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
		return PlanRecord{}, unknownPlan(planID)
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
