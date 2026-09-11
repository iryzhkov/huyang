package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	planStateVersion  = 1
	planRecordVersion = 2
)

type OperationKind string

const (
	OperationReplaceSymbol   OperationKind = "replace_symbol"
	OperationDeleteSymbol    OperationKind = "delete_symbol"
	OperationInsertBefore    OperationKind = "insert_before"
	OperationInsertAfter     OperationKind = "insert_after"
	OperationReplaceRange    OperationKind = "replace_range"
	OperationCreateFile      OperationKind = "create_file"
	OperationMoveFile        OperationKind = "move_file"
	OperationDeleteFile      OperationKind = "delete_file"
	OperationRenameSymbol    OperationKind = "rename_symbol"
	OperationMoveSymbols     OperationKind = "move_symbols"
	OperationReplaceMatches  OperationKind = "replace_matches"
	OperationApplyCodeAction OperationKind = "apply_code_action"
)

type PlanState string

const (
	PlanOpen             PlanState = "OPEN"
	PlanPreviewed        PlanState = "PREVIEWED"
	PlanPreparing        PlanState = "PREPARING"
	PlanFailed           PlanState = "FAILED"
	PlanProvisional      PlanState = "PROVISIONAL"
	PlanReady            PlanState = "READY"
	PlanCommitting       PlanState = "COMMITTING"
	PlanCommitted        PlanState = "COMMITTED"
	PlanRecoveryRequired PlanState = "RECOVERY_REQUIRED"
	PlanRollingBack      PlanState = "ROLLING_BACK"
	PlanRolledBack       PlanState = "ROLLED_BACK"
	PlanDiscarded        PlanState = "DISCARDED"
	PlanConflicted       PlanState = "CONFLICTED"
	PlanExpired          PlanState = "EXPIRED"
)

// planTransitions is the plan state machine from the implementation plan. Every durable
// state change goes through transitionPlan, which refuses an edge that is not listed here.
// Two edges extend the documented table: COMMITTING -> CONFLICTED and COMMITTING -> FAILED
// both describe a commit aborted before any canonical byte was written, so the plan is
// re-preparable rather than in need of recovery. Startup reconciliation in loadPlans and
// recoverCommitJournals uses the same table to reconcile the persisted state of a plan whose
// process died mid-transition.
var planTransitions = map[PlanState][]PlanState{
	PlanOpen:             {PlanOpen, PlanPreviewed, PlanPreparing, PlanDiscarded, PlanExpired},
	PlanPreviewed:        {PlanOpen, PlanPreviewed, PlanPreparing, PlanDiscarded, PlanExpired},
	PlanPreparing:        {PlanConflicted, PlanFailed, PlanProvisional, PlanReady},
	PlanConflicted:       {PlanOpen, PlanPreparing, PlanRollingBack, PlanDiscarded},
	PlanFailed:           {PlanOpen, PlanPreparing, PlanRollingBack, PlanDiscarded},
	PlanProvisional:      {PlanCommitting, PlanRollingBack, PlanFailed},
	PlanReady:            {PlanCommitting, PlanRollingBack, PlanFailed},
	PlanCommitting:       {PlanCommitted, PlanRecoveryRequired, PlanConflicted, PlanFailed},
	PlanRollingBack:      {PlanRolledBack, PlanFailed},
	PlanRecoveryRequired: {PlanRolledBack, PlanCommitted},
	PlanCommitted:        {},
	PlanRolledBack:       {},
	PlanDiscarded:        {},
	PlanExpired:          {},
}

// canTransition reports whether the plan state machine allows moving from one state to
// another. Unknown states have no legal transitions.
func canTransition(from, to PlanState) bool {
	for _, allowed := range planTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

func illegalTransition(planID string, from, to PlanState) error {
	return Codedf(CodePlanStateInvalid, "plan %s cannot move from %s to %s", planID, from, to)
}

// Plan event retention. A plan record keeps its first planEventsHead events, which describe
// how it was created and shaped, and its most recent planEventsTail events; anything between
// is dropped and counted in PlanRecord.DroppedEvents. Inspect-only reads record no event at
// all, because they neither change the plan nor justify rewriting its durable file.
const (
	planEventsHead = 8
	planEventsTail = 56
)

// PlanConflictReason records why a plan entered CONFLICTED.
type PlanConflictReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type PlanTarget struct {
	Handle        HandleID           `json:"handle,omitempty"`
	FileRange     *RangeHandle       `json:"file_range,omitempty"`
	SymbolLocator *PlanSymbolLocator `json:"symbol_locator,omitempty"`
}

type PlanSymbolLocator struct {
	Path     string `json:"path"`
	NamePath string `json:"name_path"`
}

type PlanOperation struct {
	OpID                string        `json:"op_id"`
	Kind                OperationKind `json:"kind"`
	Target              *PlanTarget   `json:"target,omitempty"`
	Content             string        `json:"content,omitempty"`
	Path                string        `json:"path,omitempty"`
	From                string        `json:"from,omitempty"`
	To                  string        `json:"to,omitempty"`
	Revision            RevisionID    `json:"revision_id,omitempty"`
	DestinationRevision RevisionID    `json:"destination_revision_id,omitempty"`
	DependsOn           []string      `json:"depends_on,omitempty"`
	Indentation         string        `json:"indentation,omitempty"`
}

type PlanEdit struct {
	Mode       string          `json:"mode"`
	Operations []PlanOperation `json:"operations,omitempty"`
	OpIDs      []string        `json:"op_ids,omitempty"`
}

type PlanConflict struct {
	OpID     string       `json:"op_id"`
	Code     ConflictCode `json:"code"`
	Path     string       `json:"path,omitempty"`
	Expected RevisionID   `json:"expected_revision,omitempty"`
	Current  RevisionID   `json:"current_revision,omitempty"`
	Message  string       `json:"message"`
}

type PlanPreview struct {
	PreviewRevision  string         `json:"preview_revision"`
	PlanRevision     uint64         `json:"plan_revision"`
	Outcome          string         `json:"outcome"`
	NormalizedOrder  []string       `json:"normalized_order"`
	AffectedFiles    []string       `json:"affected_files"`
	Diffs            []ExactDiff    `json:"diffs,omitempty"`
	Conflicts        []PlanConflict `json:"conflicts,omitempty"`
	CanonicalChanged bool           `json:"canonical_changed"`
}

type PlanRecord struct {
	PlanID       string              `json:"plan_id"`
	WorkspaceID  ID                  `json:"workspace_id"`
	State        PlanState           `json:"state"`
	PlanRevision uint64              `json:"plan_revision"`
	BaseStateSeq uint64              `json:"base_state_seq"`
	Operations   []PlanOperation     `json:"operations"`
	Preview      *PlanPreview        `json:"preview,omitempty"`
	Preparation  *PlanPreparation    `json:"preparation,omitempty"`
	Conflict     *PlanConflictReason `json:"conflict,omitempty"`
	Events       []PlanEvent         `json:"events"`
	// DroppedEvents counts events removed from the middle of Events by retention.
	DroppedEvents int `json:"dropped_events,omitempty"`
	// Compacted marks a terminal plan whose bulky payloads retention has stripped.
	Compacted bool      `json:"compacted,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PlanEvent struct {
	Action       string    `json:"action"`
	PlanRevision uint64    `json:"plan_revision"`
	Outcome      string    `json:"outcome"`
	At           time.Time `json:"at"`
}

// recordPlanEvent appends one event, stamps UpdatedAt, and applies event retention.
func recordPlanEvent(plan *PlanRecord, action string, outcome string) {
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Action: action, PlanRevision: plan.PlanRevision, Outcome: outcome, At: plan.UpdatedAt,
	})
	if excess := len(plan.Events) - (planEventsHead + planEventsTail); excess > 0 {
		kept := make([]PlanEvent, 0, planEventsHead+planEventsTail)
		kept = append(kept, plan.Events[:planEventsHead]...)
		kept = append(kept, plan.Events[planEventsHead+excess:]...)
		plan.Events = kept
		plan.DroppedEvents += excess
	}
}

// Plan records are stored one file per plan under <stateDir>/plans/<workspace id>/<plan id>.json
// (planRecordVersion 2) so a mutation rewrites and syncs only the record that changed. The
// previous layout, one monolithic <stateDir>/plans/<workspace id>.json holding every record
// (planStateVersion 1), is still read on open: its records are pruned, written out as
// per-plan files and the legacy file is then removed.
type persistedPlans struct {
	Version int          `json:"version"`
	Plans   []PlanRecord `json:"plans"`
}

type persistedPlan struct {
	Version int        `json:"version"`
	Plan    PlanRecord `json:"plan"`
}

// Terminal plan retention. A plan in a terminal state is kept for planRetainAge, and only
// the newest planRetainCount terminal plans are kept at all; the rest are dropped on open
// and after every terminal transition. A plan whose commit journal is still prepared,
// applying or recovery-required is never dropped, because that journal is what recovery
// reconciles against. Terminal plans older than planCompactAge lose their bulky payloads
// (operation contents and targets, preview, verification evidence, committed diffs) but keep
// their identity, state, revisions, canonical receipt and conflict reason.
const (
	planRetainCount = 200
	planRetainAge   = 30 * 24 * time.Hour
	planCompactAge  = time.Hour
)

func planTerminal(state PlanState) bool {
	switch state {
	case PlanCommitted, PlanRolledBack, PlanDiscarded, PlanExpired, PlanFailed, PlanConflicted:
		return true
	}
	return false
}

// legacyPlanStatePath is the monolithic version 1 file read for migration only.
func (w *Workspace) legacyPlanStatePath() string {
	if w.stateDir == "" {
		return ""
	}
	return filepath.Join(w.stateDir, "plans", string(w.Identity().ID)+".json")
}

func (w *Workspace) planStateDir() string {
	if w.stateDir == "" {
		return ""
	}
	return filepath.Join(w.stateDir, "plans", string(w.Identity().ID))
}

func (w *Workspace) planRecordPath(planID string) string {
	directory := w.planStateDir()
	if directory == "" {
		return ""
	}
	return filepath.Join(directory, planID+".json")
}

func (w *Workspace) loadPlans() error {
	if w.stateDir == "" {
		return nil
	}
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	legacy, err := w.loadLegacyPlans()
	if err != nil {
		return err
	}
	changed := make(map[string]bool, len(legacy))
	for _, plan := range legacy {
		w.plans[plan.PlanID] = plan
		changed[plan.PlanID] = true
	}
	if err := w.loadPlanRecords(); err != nil {
		return err
	}
	for id, plan := range w.plans {
		plan = clonePlan(plan)
		switch plan.State {
		case PlanReady, PlanProvisional:
			plan.State = PlanFailed
			recordPlanEvent(&plan, "provider_restart_restore", "provider_buffers_discarded_reprepare_available")
		case PlanPreparing, PlanRollingBack:
			plan.State = PlanFailed
			plan.Preparation = nil
			recordPlanEvent(&plan, "provider_restart_restore", "incomplete_provider_operation_discarded")
		case PlanCommitting:
			plan.State = PlanRecoveryRequired
			recordPlanEvent(&plan, "commit_restart_detected", "recovery_required")
		default:
			continue
		}
		w.plans[id] = plan
		changed[id] = true
	}
	dropped, compacted, err := w.pruneTerminalPlansLocked(time.Now().UTC())
	if err != nil {
		return err
	}
	if len(legacy) > 0 || dropped > 0 || compacted > 0 {
		log.Printf("huyang: workspace %s plan store: migrated %d legacy records, dropped %d terminal plans, compacted %d",
			w.Identity().ID, len(legacy), dropped, compacted)
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := w.persistPlanLocked(id); err != nil {
			return err
		}
	}
	if len(legacy) > 0 {
		if err := os.Remove(w.legacyPlanStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove migrated plan file: %w", err)
		}
	}
	return nil
}

func (w *Workspace) loadLegacyPlans() ([]PlanRecord, error) {
	content, err := os.ReadFile(w.legacyPlanStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plans: %w", err)
	}
	var state persistedPlans
	if err := json.Unmarshal(content, &state); err != nil {
		return nil, fmt.Errorf("decode plans: %w", err)
	}
	if state.Version != planStateVersion {
		return nil, fmt.Errorf("unsupported plan state version %d", state.Version)
	}
	for _, plan := range state.Plans {
		if plan.WorkspaceID != w.Identity().ID {
			return nil, fmt.Errorf("plan %s belongs to workspace %s", plan.PlanID, plan.WorkspaceID)
		}
	}
	return state.Plans, nil
}

func (w *Workspace) loadPlanRecords() error {
	entries, err := os.ReadDir(w.planStateDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read plan records: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(w.planStateDir(), entry.Name()))
		if err != nil {
			return fmt.Errorf("read plan record %s: %w", entry.Name(), err)
		}
		var record persistedPlan
		if err := json.Unmarshal(content, &record); err != nil {
			return fmt.Errorf("decode plan record %s: %w", entry.Name(), err)
		}
		if record.Version != planRecordVersion {
			return fmt.Errorf("unsupported plan record version %d in %s", record.Version, entry.Name())
		}
		if record.Plan.WorkspaceID != w.Identity().ID {
			return fmt.Errorf("plan %s belongs to workspace %s", record.Plan.PlanID, record.Plan.WorkspaceID)
		}
		if record.Plan.PlanID+".json" != entry.Name() {
			return fmt.Errorf("plan record %s holds plan %s", entry.Name(), record.Plan.PlanID)
		}
		w.plans[record.Plan.PlanID] = record.Plan
	}
	return nil
}

// persistPlanLocked writes the durable record of one plan, or removes the record when the
// plan no longer exists in memory. The caller holds plansMu.
func (w *Workspace) persistPlanLocked(planID string) error {
	path := w.planRecordPath(planID)
	if path == "" {
		return nil
	}
	plan, ok := w.plans[planID]
	if !ok {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	content, err := json.MarshalIndent(persistedPlan{Version: planRecordVersion, Plan: plan}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plan %s: %w", planID, err)
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, content, 0o600)
}

// journalRetainsPlan reports whether the plan's commit journal is still incomplete, in
// which case the plan record must outlive retention so startup recovery can reconcile it.
func (w *Workspace) journalRetainsPlan(planID string) bool {
	journal, err := w.loadCommitJournal(planID)
	if err != nil {
		return false
	}
	return journal.State != CommitJournalCommitted && journal.State != CommitJournalRolledBack
}

// pruneTerminalPlansLocked applies terminal plan retention and compaction. It returns the
// number of plans dropped and compacted; every change is persisted immediately. The caller
// holds plansMu.
func (w *Workspace) pruneTerminalPlansLocked(now time.Time) (dropped, compacted int, err error) {
	if w.stateDir == "" {
		return 0, 0, nil
	}
	terminal := make([]PlanRecord, 0, len(w.plans))
	for _, plan := range w.plans {
		if planTerminal(plan.State) {
			terminal = append(terminal, plan)
		}
	}
	sort.Slice(terminal, func(i, j int) bool {
		if !terminal[i].UpdatedAt.Equal(terminal[j].UpdatedAt) {
			return terminal[i].UpdatedAt.After(terminal[j].UpdatedAt)
		}
		return terminal[i].PlanID < terminal[j].PlanID
	})
	kept := 0
	for _, plan := range terminal {
		expired := now.Sub(plan.UpdatedAt) > planRetainAge
		if (kept >= planRetainCount || expired) && !w.journalRetainsPlan(plan.PlanID) {
			delete(w.plans, plan.PlanID)
			if err := w.persistPlanLocked(plan.PlanID); err != nil {
				return dropped, compacted, err
			}
			dropped++
			continue
		}
		kept++
		if !plan.Compacted && now.Sub(plan.UpdatedAt) > planCompactAge {
			plan = compactPlan(plan)
			w.plans[plan.PlanID] = plan
			if err := w.persistPlanLocked(plan.PlanID); err != nil {
				return dropped, compacted, err
			}
			compacted++
		}
	}
	return dropped, compacted, nil
}

// compactPlan strips the payloads a terminal plan no longer needs. What remains identifies
// the plan, its state and revisions, its canonical receipt and its conflict reason.
func compactPlan(plan PlanRecord) PlanRecord {
	plan = clonePlan(plan)
	for i := range plan.Operations {
		operation := &plan.Operations[i]
		operation.Content = ""
		operation.Target = nil
		operation.DependsOn = nil
	}
	plan.Preview = nil
	if plan.Preparation != nil {
		slim := PlanPreparation{
			PreparedRevision: plan.Preparation.PreparedRevision, ProviderEpoch: plan.Preparation.ProviderEpoch,
			AffectedFiles: plan.Preparation.AffectedFiles, CanonicalChanged: plan.Preparation.CanonicalChanged,
			CanonicalRevision: plan.Preparation.CanonicalRevision, JournalID: plan.Preparation.JournalID,
			Diagnostics: plan.Preparation.Diagnostics, DiskChecks: plan.Preparation.DiskChecks,
			BaseRevision: plan.Preparation.BaseRevision, CanonicalFromRevision: plan.Preparation.CanonicalFromRevision,
			ProvisionalAccepted: plan.Preparation.ProvisionalAccepted,
		}
		plan.Preparation = &slim
	}
	plan.Compacted = true
	return plan
}

// finishPlanLocked persists a plan after a state change and, when the plan reached a
// terminal state, applies retention across the store. The caller holds plansMu.
func (w *Workspace) finishPlanLocked(planID string) error {
	if err := w.persistPlanLocked(planID); err != nil {
		return err
	}
	if plan, ok := w.plans[planID]; ok && planTerminal(plan.State) {
		if _, _, err := w.pruneTerminalPlansLocked(time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func newPlanID() (string, error) {
	value, err := randomOpaque("plan_")
	if err != nil {
		return "", err
	}
	return value, nil
}

func (w *Workspace) CreatePlan(operations []PlanOperation) (PlanRecord, error) {
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	id, err := newPlanID()
	if err != nil {
		return PlanRecord{}, err
	}
	now := time.Now().UTC()
	plan := PlanRecord{
		PlanID: id, WorkspaceID: w.Identity().ID, State: PlanOpen, PlanRevision: 1,
		BaseStateSeq: w.Identity().StateSeq, Operations: normalized, CreatedAt: now, UpdatedAt: now,
		Events: []PlanEvent{{Action: "create", PlanRevision: 1, Outcome: "ok", At: now}},
	}
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	w.plans[id] = plan
	if err := w.persistPlanLocked(id); err != nil {
		delete(w.plans, id)
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) InspectPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected != 0 && plan.PlanRevision != expected {
		return PlanRecord{}, planRevisionChanged(expected, plan.PlanRevision)
	}
	// Inspection is a pure read: it records no event and never rewrites the plan file.
	return clonePlan(plan), nil
}

func planRevisionChanged(expected, current uint64) error {
	return Codedf(CodePlanRevisionChanged, "expected %d, current %d", expected, current)
}

func (w *Workspace) EditPlan(planID string, expected uint64, edit PlanEdit) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	// READY and PROVISIONAL plans hold the provider lease and staged buffers, so they must
	// be rolled back before their operations change; every other editable state carries no
	// provider-side state and returns to OPEN, dropping its preview, preparation evidence and
	// conflict reason.
	if !canTransition(plan.State, PlanOpen) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanOpen)
	}
	if plan.Compacted {
		return PlanRecord{}, Codedf(CodePlanStateInvalid, "plan %s was compacted by retention; create a new plan", planID)
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, planRevisionChanged(expected, plan.PlanRevision)
	}
	operations := cloneOperations(plan.Operations)
	switch edit.Mode {
	case "add":
		operations = append(operations, edit.Operations...)
	case "update":
		updates := make(map[string]PlanOperation, len(edit.Operations))
		for _, operation := range edit.Operations {
			updates[operation.OpID] = operation
		}
		for i := range operations {
			if update, found := updates[operations[i].OpID]; found {
				operations[i] = update
				delete(updates, operations[i].OpID)
			}
		}
		if len(updates) != 0 {
			return PlanRecord{}, errors.New("update names an unknown op_id")
		}
	case "remove":
		remove := make(map[string]bool, len(edit.OpIDs))
		for _, id := range edit.OpIDs {
			remove[id] = true
		}
		filtered := operations[:0]
		for _, operation := range operations {
			if !remove[operation.OpID] {
				filtered = append(filtered, operation)
			}
		}
		operations = filtered
	case "reorder":
		if len(edit.OpIDs) != len(operations) {
			return PlanRecord{}, errors.New("reorder must name every op_id exactly once")
		}
		byID := make(map[string]PlanOperation, len(operations))
		for _, operation := range operations {
			byID[operation.OpID] = operation
		}
		reordered := make([]PlanOperation, 0, len(operations))
		for _, id := range edit.OpIDs {
			operation, found := byID[id]
			if !found {
				return PlanRecord{}, errors.New("reorder contains an unknown or duplicate op_id")
			}
			reordered = append(reordered, operation)
			delete(byID, id)
		}
		operations = reordered
	case "replace_all":
		operations = edit.Operations
	default:
		return PlanRecord{}, fmt.Errorf("unknown plan edit mode %q", edit.Mode)
	}
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	plan.Operations = normalized
	plan.State = PlanOpen
	plan.PlanRevision++
	plan.Preview = nil
	plan.Preparation = nil
	plan.Conflict = nil
	recordPlanEvent(&plan, "edit", "ok")
	w.plans[planID] = plan
	if err := w.persistPlanLocked(planID); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) DiscardPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, planRevisionChanged(expected, plan.PlanRevision)
	}
	// Terminal plans keep their record: a COMMITTED plan is the receipt CompensatePlan
	// undoes from, and a RECOVERY_REQUIRED plan must stay visible until startup recovery
	// resolves it. READY and PROVISIONAL plans go through RollbackPlan so the provider lease
	// and staged buffers are released with them.
	if !canTransition(plan.State, PlanDiscarded) {
		return PlanRecord{}, illegalTransition(planID, plan.State, PlanDiscarded)
	}
	plan.State = PlanDiscarded
	recordPlanEvent(&plan, "discard", "ok")
	w.plans[planID] = plan
	if err := w.finishPlanLocked(planID); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(plan), nil
}

func (w *Workspace) PreviewPlan(planID string, expected uint64) (PlanRecord, error) {
	w.plansMu.Lock()
	plan, ok := w.plans[planID]
	w.plansMu.Unlock()
	if !ok {
		return PlanRecord{}, errors.New("unknown plan")
	}
	if plan.State != PlanOpen && plan.State != PlanPreviewed {
		return PlanRecord{}, errors.New("plan cannot be previewed in its current state")
	}
	if expected == 0 || plan.PlanRevision != expected {
		return PlanRecord{}, planRevisionChanged(expected, plan.PlanRevision)
	}
	preview := w.buildPreview(plan)
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	current, ok := w.plans[planID]
	if !ok || current.PlanRevision != expected ||
		(current.State != PlanOpen && current.State != PlanPreviewed) {
		return PlanRecord{}, errors.New("plan changed while preview was being built")
	}
	current.Preview = &preview
	current.State = PlanPreviewed
	recordPlanEvent(&current, "preview", preview.Outcome)
	w.plans[planID] = current
	if err := w.persistPlanLocked(planID); err != nil {
		return PlanRecord{}, err
	}
	return clonePlan(current), nil
}

func (w *Workspace) normalizeOperations(operations []PlanOperation) ([]PlanOperation, error) {
	result := cloneOperations(operations)
	seen := make(map[string]bool, len(result))
	for i := range result {
		operation := &result[i]
		operation.OpID = strings.TrimSpace(operation.OpID)
		if operation.OpID == "" {
			return nil, errors.New("every operation requires op_id")
		}
		if seen[operation.OpID] {
			return nil, fmt.Errorf("duplicate op_id %q", operation.OpID)
		}
		seen[operation.OpID] = true
		if operation.Indentation == "" {
			operation.Indentation = "exact"
		}
		if operation.Indentation != "exact" && operation.Indentation != "syntax_anchor" && operation.Indentation != "formatter" {
			return nil, fmt.Errorf("%s has invalid indentation mode %q", operation.OpID, operation.Indentation)
		}
		if operation.Indentation == "syntax_anchor" && (operation.Target == nil || operation.Target.Handle == "") {
			return nil, fmt.Errorf("%s syntax_anchor requires one semantic node handle", operation.OpID)
		}
		switch operation.Kind {
		case OperationReplaceSymbol, OperationDeleteSymbol, OperationInsertBefore, OperationInsertAfter, OperationReplaceRange:
			if operation.Target == nil {
				return nil, fmt.Errorf("%s requires target", operation.OpID)
			}
			if operation.Target.FileRange == nil && operation.Target.Handle != "" {
				resolution, err := w.ResolveHandle(operation.Target.Handle)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				if resolution.Status == ResolutionConflicted {
					return nil, fmt.Errorf("%s: %s", operation.OpID, resolution.Code)
				}
				if operation.Indentation == "syntax_anchor" && (resolution.Original.Kind == "range" || resolution.Original.Kind == "match") {
					return nil, fmt.Errorf("%s syntax_anchor target is not one parsed node", operation.OpID)
				}
				handle, err := resolution.RangeHandle()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				operation.Target.FileRange = &handle
			}
			if operation.Target.FileRange == nil && operation.Target.SymbolLocator != nil {
				selected, err := w.ResolveSymbolLocator(operation.Target.SymbolLocator.Path, operation.Target.SymbolLocator.NamePath)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				resolution, err := w.ResolveHandle(selected.Handle)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				handle, err := resolution.RangeHandle()
				if err != nil {
					return nil, fmt.Errorf("%s: %w", operation.OpID, err)
				}
				operation.Target.FileRange = &handle
			}
			if operation.Target.FileRange == nil {
				return nil, fmt.Errorf("%s requires a resolvable handle or file_range", operation.OpID)
			}
			if operation.Kind == OperationDeleteSymbol {
				operation.Content = ""
			}
		case OperationCreateFile:
			if operation.Path == "" {
				return nil, fmt.Errorf("%s requires path", operation.OpID)
			}
			if operation.Revision == "" {
				snapshot, err := w.Snapshot(operation.Path, ProviderLayer{})
				if err != nil {
					return nil, fmt.Errorf("%s: snapshot create target: %w", operation.OpID, err)
				}
				if snapshot.Disk.Kind != ObjectMissing {
					return nil, fmt.Errorf("%s: create target already exists", operation.OpID)
				}
				operation.Revision = snapshot.Revision
			}
		case OperationDeleteFile:
			if operation.Path == "" {
				return nil, fmt.Errorf("%s requires path", operation.OpID)
			}
			if operation.Revision == "" {
				snapshot, err := w.Snapshot(operation.Path, ProviderLayer{})
				if err != nil {
					return nil, fmt.Errorf("%s: snapshot delete target: %w", operation.OpID, err)
				}
				if snapshot.Disk.Kind == ObjectMissing {
					return nil, fmt.Errorf("%s: delete target is missing", operation.OpID)
				}
				operation.Revision = snapshot.Revision
			}
		case OperationMoveFile:
			if operation.From == "" || operation.To == "" {
				return nil, fmt.Errorf("%s requires from and to", operation.OpID)
			}
			if operation.Revision == "" {
				source, err := w.Snapshot(operation.From, ProviderLayer{})
				if err != nil {
					return nil, fmt.Errorf("%s: snapshot move source: %w", operation.OpID, err)
				}
				if source.Disk.Kind == ObjectMissing {
					return nil, fmt.Errorf("%s: move source is missing", operation.OpID)
				}
				operation.Revision = source.Revision
			}
			if operation.DestinationRevision == "" {
				destination, err := w.Snapshot(operation.To, ProviderLayer{})
				if err != nil {
					return nil, fmt.Errorf("%s: snapshot move destination: %w", operation.OpID, err)
				}
				if destination.Disk.Kind != ObjectMissing {
					return nil, fmt.Errorf("%s: move destination already exists", operation.OpID)
				}
				operation.DestinationRevision = destination.Revision
			}
		case OperationRenameSymbol, OperationMoveSymbols, OperationApplyCodeAction:
			if operation.Target == nil || (operation.Target.Handle == "" && operation.Target.FileRange == nil && operation.Target.SymbolLocator == nil) {
				return nil, fmt.Errorf("%s requires target", operation.OpID)
			}
		case OperationReplaceMatches:
			if operation.Target == nil || operation.Target.Handle == "" {
				return nil, fmt.Errorf("%s requires a result-set handle", operation.OpID)
			}
			if _, err := w.ResolveAllMatches(ResultSetID(operation.Target.Handle)); err != nil {
				return nil, fmt.Errorf("%s: %w", operation.OpID, err)
			}
		default:
			return nil, fmt.Errorf("%s has unknown operation kind %q", operation.OpID, operation.Kind)
		}
		for _, dependency := range operation.DependsOn {
			if dependency == operation.OpID {
				return nil, fmt.Errorf("%s cannot depend on itself", operation.OpID)
			}
		}
	}
	for _, operation := range result {
		for _, dependency := range operation.DependsOn {
			if !seen[dependency] {
				return nil, fmt.Errorf("%s depends on unknown op_id %s", operation.OpID, dependency)
			}
		}
	}
	if _, err := orderOperations(result); err != nil {
		return nil, err
	}
	return result, nil
}

func orderOperations(operations []PlanOperation) ([]PlanOperation, error) {
	byID := make(map[string]PlanOperation, len(operations))
	index := make(map[string]int, len(operations))
	indegree := make(map[string]int, len(operations))
	next := make(map[string][]string, len(operations))
	for i, operation := range operations {
		byID[operation.OpID] = operation
		index[operation.OpID] = i
		indegree[operation.OpID] = 0
	}
	addEdge := func(before, after string) {
		for _, existing := range next[before] {
			if existing == after {
				return
			}
		}
		next[before] = append(next[before], after)
		indegree[after]++
	}
	for _, operation := range operations {
		for _, dependency := range operation.DependsOn {
			dependencyOperation := byID[dependency]
			dependencyPath, dependencyStart, dependencyEdit := operationRange(dependencyOperation)
			operationPath, operationStart, operationEdit := operationRange(operation)
			if dependencyEdit && operationEdit && dependencyPath == operationPath && dependencyStart != operationStart {
				// Revision-bound ranges must be applied from the end of a file
				// toward the beginning so earlier edits cannot shift later offsets.
				// A logical dependency between disjoint ranges does not override
				// that deterministic positional ordering.
				continue
			}
			addEdge(dependency, operation.OpID)
		}
	}
	for i, left := range operations {
		for j, right := range operations {
			if i == j {
				continue
			}
			leftPath, leftStart, leftEdit := operationRange(left)
			rightPath, rightStart, rightEdit := operationRange(right)
			if leftEdit && rightEdit && leftPath == rightPath && leftStart < rightStart {
				addEdge(right.OpID, left.OpID)
			}
			if left.Kind == OperationCreateFile && left.Path != "" && rightEdit && rightPath == left.Path {
				addEdge(left.OpID, right.OpID)
			}
			if leftEdit && right.Kind == OperationDeleteFile && leftPath == right.Path {
				addEdge(left.OpID, right.OpID)
			}
			if leftEdit && right.Kind == OperationMoveFile && leftPath == right.From {
				addEdge(left.OpID, right.OpID)
			}
		}
	}
	ready := make([]string, 0, len(operations))
	for id, count := range indegree {
		if count == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
	ordered := make([]PlanOperation, 0, len(operations))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		ordered = append(ordered, byID[id])
		for _, target := range next[id] {
			indegree[target]--
			if indegree[target] == 0 {
				ready = append(ready, target)
				sort.Slice(ready, func(i, j int) bool { return index[ready[i]] < index[ready[j]] })
			}
		}
	}
	if len(ordered) != len(operations) {
		return nil, errors.New("operation dependency cycle")
	}
	return ordered, nil
}

func operationRange(operation PlanOperation) (string, int, bool) {
	if operation.Target == nil || operation.Target.FileRange == nil {
		return "", 0, false
	}
	return operation.Target.FileRange.Path, operation.Target.FileRange.ByteStart, true
}

func syntaxAnchorReplacement(content []byte, start int, replacement []byte) ([]byte, error) {
	if start < 0 || start > len(content) {
		return nil, errors.New("syntax anchor is outside the document")
	}
	lineStart := bytes.LastIndexByte(content[:start], '\n') + 1
	prefix := content[lineStart:start]
	for _, value := range prefix {
		if value != ' ' && value != '\t' {
			return nil, errors.New("syntax anchor does not begin at a parsed node indentation boundary")
		}
	}
	if bytes.Contains(prefix, []byte(" ")) && bytes.Contains(prefix, []byte("\t")) {
		return nil, errors.New("syntax anchor has mixed leading whitespace")
	}
	lines := bytes.Split(replacement, []byte("\n"))
	if len(lines) < 2 {
		return append([]byte(nil), replacement...), nil
	}
	var result []byte
	result = append(result, lines[0]...)
	for _, line := range lines[1:] {
		result = append(result, '\n')
		if len(line) > 0 {
			result = append(result, prefix...)
		}
		result = append(result, line...)
	}
	return result, nil
}

func (w *Workspace) buildPreview(plan PlanRecord) PlanPreview {
	ordered, orderErr := orderOperations(plan.Operations)
	preview := PlanPreview{PlanRevision: plan.PlanRevision, Outcome: "ok", CanonicalChanged: false}
	if orderErr != nil {
		preview.Outcome = "conflict"
		preview.Conflicts = append(preview.Conflicts, PlanConflict{Code: "operation_dependency_cycle", Message: orderErr.Error()})
		return finalizePreview(preview)
	}
	for _, operation := range ordered {
		preview.NormalizedOrder = append(preview.NormalizedOrder, operation.OpID)
	}
	contents := make(map[string][]byte)
	exists := make(map[string]bool)
	before := make(map[string][]byte)
	touched := make(map[string]bool)
	conflictFor := func(operation PlanOperation, path string, expected RevisionID, err error) {
		conflict := PlanConflict{OpID: operation.OpID, Code: ConflictDocumentChanged, Path: path, Expected: expected, Message: err.Error()}
		var typed *Conflict
		if errors.As(err, &typed) {
			conflict.Code, conflict.Current = typed.Code, typed.Current
		}
		preview.Conflicts = append(preview.Conflicts, conflict)
	}
	load := func(path string) ([]byte, bool, error) {
		if content, ok := contents[path]; ok {
			return content, exists[path], nil
		}
		read, err := w.Read(path)
		if err != nil {
			snapshot, snapshotErr := w.Refresh(path, ProviderLayer{})
			if snapshotErr == nil && snapshot.Disk.Kind == ObjectMissing {
				contents[path], before[path], exists[path] = nil, nil, false
				return nil, false, nil
			}
			if snapshotErr == nil && snapshot.Disk.Kind == ObjectBinary {
				absolute := filepath.Join(w.Identity().Root, filepath.FromSlash(path))
				content, binaryErr := os.ReadFile(absolute)
				if binaryErr != nil {
					return nil, false, binaryErr
				}
				if int64(len(content)) != snapshot.Disk.Size || hashBytes(content) != snapshot.ContentSHA256 {
					return nil, false, &Conflict{
						Code:     ConflictDocumentChanged,
						Path:     path,
						Expected: snapshot.Revision,
						Current:  snapshot.Revision,
					}
				}
				contents[path], before[path], exists[path] = content, append([]byte(nil), content...), true
				return content, true, nil
			}
			if snapshotErr == nil && snapshot.Disk.Kind == ObjectSymlink {
				content := []byte(snapshot.Disk.SymlinkTarget)
				contents[path], before[path], exists[path] = content, append([]byte(nil), content...), true
				return content, true, nil
			}
			return nil, false, err
		}
		content := append([]byte(nil), read.Content...)
		contents[path], before[path], exists[path] = content, append([]byte(nil), content...), true
		return content, true, nil
	}
	for _, operation := range ordered {
		switch operation.Kind {
		case OperationReplaceSymbol, OperationDeleteSymbol, OperationInsertBefore, OperationInsertAfter, OperationReplaceRange:
			handle := *operation.Target.FileRange
			current, err := w.ValidateMutation(w.Identity().ID, handle.Path, handle.Revision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, handle.Path, handle.Revision, err)
				continue
			}
			content, present, err := load(handle.Path)
			if err != nil || !present {
				if err == nil {
					err = errors.New("target file is missing")
				}
				conflictFor(operation, handle.Path, handle.Revision, err)
				continue
			}
			if handle.ByteStart < 0 || handle.ByteEnd < handle.ByteStart || handle.ByteEnd > len(content) ||
				hashBytes(content[handle.ByteStart:handle.ByteEnd]) != handle.ExpectedSHA256 {
				conflictFor(operation, handle.Path, handle.Revision, &Conflict{Code: ConflictDocumentChanged, Path: handle.Path, Expected: handle.Revision, Current: current.Revision})
				continue
			}
			start, end := handle.ByteStart, handle.ByteEnd
			switch operation.Kind {
			case OperationInsertBefore:
				end = start
			case OperationInsertAfter:
				start = end
			}
			replacement := []byte(operation.Content)
			if operation.Indentation == "syntax_anchor" {
				replacement, err = syntaxAnchorReplacement(content, start, replacement)
				if err != nil {
					conflictFor(operation, handle.Path, handle.Revision, err)
					continue
				}
			}
			nextContent := make([]byte, 0, len(content)-(end-start)+len(replacement))
			nextContent = append(nextContent, content[:start]...)
			nextContent = append(nextContent, replacement...)
			nextContent = append(nextContent, content[end:]...)
			contents[handle.Path] = nextContent
			touched[handle.Path] = true
		case OperationCreateFile:
			current, err := w.ValidateMutation(w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			if current.Disk.Kind != ObjectMissing {
				conflictFor(operation, operation.Path, operation.Revision, errors.New("create target already exists"))
				continue
			}
			_, _, _ = load(operation.Path)
			contents[operation.Path], exists[operation.Path], touched[operation.Path] = []byte(operation.Content), true, true
		case OperationDeleteFile:
			if _, err := w.ValidateMutation(w.Identity().ID, operation.Path, operation.Revision, ProviderLayer{}); err != nil {
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			if _, present, err := load(operation.Path); err != nil || !present {
				if err == nil {
					err = errors.New("delete target is missing")
				}
				conflictFor(operation, operation.Path, operation.Revision, err)
				continue
			}
			contents[operation.Path], exists[operation.Path], touched[operation.Path] = nil, false, true
		case OperationMoveFile:
			if _, err := w.ValidateMutation(w.Identity().ID, operation.From, operation.Revision, ProviderLayer{}); err != nil {
				conflictFor(operation, operation.From, operation.Revision, err)
				continue
			}
			destination, err := w.ValidateMutation(w.Identity().ID, operation.To, operation.DestinationRevision, ProviderLayer{})
			if err != nil {
				conflictFor(operation, operation.To, operation.DestinationRevision, err)
				continue
			}
			if destination.Disk.Kind != ObjectMissing {
				conflictFor(operation, operation.To, operation.DestinationRevision, errors.New("move destination exists"))
				continue
			}
			source, present, err := load(operation.From)
			if err != nil || !present {
				if err == nil {
					err = errors.New("move source is missing")
				}
				conflictFor(operation, operation.From, operation.Revision, err)
				continue
			}
			_, _, _ = load(operation.To)
			contents[operation.To], exists[operation.To], touched[operation.To] = append([]byte(nil), source...), true, true
			contents[operation.From], exists[operation.From], touched[operation.From] = nil, false, true
		case OperationReplaceMatches:
			hits, err := w.ResolveAllMatches(ResultSetID(operation.Target.Handle))
			if err != nil {
				conflictFor(operation, "", "", err)
				continue
			}
			sort.Slice(hits, func(i, j int) bool {
				if hits[i].Path == hits[j].Path {
					return hits[i].ByteStart > hits[j].ByteStart
				}
				return hits[i].Path < hits[j].Path
			})
			for _, hit := range hits {
				content, present, loadErr := load(hit.Path)
				if loadErr != nil || !present {
					if loadErr == nil {
						loadErr = errors.New("result-set target file is missing")
					}
					conflictFor(operation, hit.Path, hit.Range.Revision, loadErr)
					continue
				}
				if hit.ByteStart < 0 || hit.ByteEnd < hit.ByteStart || hit.ByteEnd > len(content) ||
					hashBytes(content[hit.ByteStart:hit.ByteEnd]) != hit.Range.ExpectedSHA256 {
					conflictFor(operation, hit.Path, hit.Range.Revision, &Conflict{Code: ConflictDocumentChanged, Path: hit.Path, Expected: hit.Range.Revision})
					continue
				}
				nextContent := make([]byte, 0, len(content)-(hit.ByteEnd-hit.ByteStart)+len(operation.Content))
				nextContent = append(nextContent, content[:hit.ByteStart]...)
				nextContent = append(nextContent, operation.Content...)
				nextContent = append(nextContent, content[hit.ByteEnd:]...)
				contents[hit.Path] = nextContent
				touched[hit.Path] = true
			}
		default:
			preview.Conflicts = append(preview.Conflicts, PlanConflict{
				OpID: operation.OpID, Code: "operation_requires_later_stage",
				Message: fmt.Sprintf("%s requires provider or result-set validation unavailable before its named stage", operation.Kind),
			})
		}
	}
	if len(preview.Conflicts) > 0 {
		preview.Outcome = "conflict"
		return finalizePreview(preview)
	}
	for path := range touched {
		preview.AffectedFiles = append(preview.AffectedFiles, path)
		if exists[path] {
			preview.Diffs = append(preview.Diffs, exactDiff(path, before[path], contents[path], 0, len(before[path]), contents[path]))
		} else {
			preview.Diffs = append(preview.Diffs, exactDiff(path, before[path], nil, 0, len(before[path]), nil))
		}
	}
	sort.Strings(preview.AffectedFiles)
	sort.Slice(preview.Diffs, func(i, j int) bool { return preview.Diffs[i].Path < preview.Diffs[j].Path })
	return finalizePreview(preview)
}

func finalizePreview(preview PlanPreview) PlanPreview {
	copy := preview
	copy.PreviewRevision = ""
	encoded, _ := json.Marshal(copy)
	sum := sha256.Sum256(encoded)
	preview.PreviewRevision = "preview_" + hex.EncodeToString(sum[:16])
	return preview
}

func cloneOperations(source []PlanOperation) []PlanOperation {
	encoded, _ := json.Marshal(source)
	var clone []PlanOperation
	_ = json.Unmarshal(encoded, &clone)
	if clone == nil {
		clone = []PlanOperation{}
	}
	return clone
}

func clonePlan(source PlanRecord) PlanRecord {
	encoded, _ := json.Marshal(source)
	var clone PlanRecord
	_ = json.Unmarshal(encoded, &clone)
	return clone
}
