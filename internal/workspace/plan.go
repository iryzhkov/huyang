package workspace

import (
	"bytes"
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
	planStateVersion = 1
	// planRecordVersion 3 added declared invariants. Version 2 records are
	// still read: they carry no invariants, and a plan with no invariants has
	// no requirements, so an older record can never gain one by being loaded.
	planRecordVersion       = 3
	planRecordVersionLegacy = 2
)

type OperationKind string

const (
	OperationReplaceSymbol  OperationKind = "replace_symbol"
	OperationDeleteSymbol   OperationKind = "delete_symbol"
	OperationInsertBefore   OperationKind = "insert_before"
	OperationInsertAfter    OperationKind = "insert_after"
	OperationReplaceRange   OperationKind = "replace_range"
	OperationCreateFile     OperationKind = "create_file"
	OperationMoveFile       OperationKind = "move_file"
	OperationCopyFile       OperationKind = "copy_file"
	OperationDeleteFile     OperationKind = "delete_file"
	OperationReplaceMatches OperationKind = "replace_matches"
)

// A plan holds only operations it can execute against exact bytes. The kinds
// whose edit a language server owns - rename_symbol, apply_code_action,
// inline_symbol, safe_delete_symbol - are request vocabulary: the handlers
// ask the server what it would change and store the answer as the ranges
// below, with DerivedFrom naming where they came from. A durable plan
// therefore never holds a promise, and preview, prepare, apply and recovery
// all work on the same exact bytes.

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
	PlanOpen:       {PlanOpen, PlanPreviewed, PlanPreparing, PlanDiscarded, PlanExpired},
	PlanPreviewed:  {PlanOpen, PlanPreviewed, PlanPreparing, PlanDiscarded, PlanExpired},
	PlanPreparing:  {PlanConflicted, PlanFailed, PlanProvisional, PlanReady},
	PlanConflicted: {PlanOpen, PlanPreparing, PlanRollingBack, PlanDiscarded},
	PlanFailed:     {PlanOpen, PlanPreparing, PlanRollingBack, PlanDiscarded},
	// READY and PROVISIONAL reach CONFLICTED because a commit can be refused
	// before it is admitted - the canonical bytes moved under the preparation -
	// and that is a conflict, not a failure of the plan. Without the edge the
	// refusal was correct but carried an internal "cannot move from PROVISIONAL
	// to CONFLICTED" behind it, and the plan stayed in a state that claimed it
	// was still applicable.
	PlanProvisional: {PlanCommitting, PlanRollingBack, PlanFailed, PlanConflicted},
	PlanReady:       {PlanCommitting, PlanRollingBack, PlanFailed, PlanConflicted},
	PlanCommitting:  {PlanCommitted, PlanRecoveryRequired, PlanConflicted, PlanFailed},
	// ROLLING_BACK -> OPEN is how a prepared plan is revised: the preparation is
	// released and the plan returns to being an intent, rather than ending as a
	// rolled-back plan its author has to recreate by hand.
	PlanRollingBack:      {PlanRolledBack, PlanFailed, PlanOpen},
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
	// ExpectedSHA256 pins the source of a copy_file, which may live outside
	// the workspace and so has no revision; it is bound at normalization
	// and rechecked at preview and apply.
	ExpectedSHA256 string   `json:"expected_sha256,omitempty"`
	DependsOn      []string `json:"depends_on,omitempty"`
	Indentation    string   `json:"indentation,omitempty"`
	// DerivedFrom names the requested operation a language server expanded
	// into this one, so a plan of twelve ranges still says "this is the
	// rename you asked for".
	DerivedFrom string `json:"derived_from,omitempty"`
}

type PlanEdit struct {
	Mode       string          `json:"mode"`
	Operations []PlanOperation `json:"operations,omitempty"`
	OpIDs      []string        `json:"op_ids,omitempty"`
	// Invariants, when present, replaces the plan's declarations wholesale.
	// A pointer rather than a slice so that leaving it out and clearing it
	// are different requests.
	Invariants *[]PlanInvariant `json:"invariants,omitempty"`
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
	PreviewRevision string         `json:"preview_revision"`
	PlanRevision    uint64         `json:"plan_revision"`
	Outcome         string         `json:"outcome"`
	NormalizedOrder []string       `json:"normalized_order"`
	AffectedFiles   []string       `json:"affected_files"`
	Diffs           []ExactDiff    `json:"diffs,omitempty"`
	Conflicts       []PlanConflict `json:"conflicts,omitempty"`
	// Hunks are where each operation's bytes ended up. They are what lets a
	// diagnostic in the prepared revision be traced back to the operation
	// that caused it rather than to the nearest one.
	Hunks            []PlanHunk `json:"hunks,omitempty"`
	CanonicalChanged bool       `json:"canonical_changed"`
}

// PlanHunk is the region one operation wrote, in the image the plan would
// produce. Superseded says a later operation rewrote those bytes, which makes
// the region somebody else's doing and is worth saying rather than hiding.
type PlanHunk struct {
	OpID       string `json:"op_id"`
	Path       string `json:"path"`
	ByteStart  int    `json:"byte_start"`
	ByteEnd    int    `json:"byte_end"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Superseded bool   `json:"superseded,omitempty"`
}

type PlanRecord struct {
	PlanID       string          `json:"plan_id"`
	WorkspaceID  ID              `json:"workspace_id"`
	State        PlanState       `json:"state"`
	PlanRevision uint64          `json:"plan_revision"`
	BaseStateSeq uint64          `json:"base_state_seq"`
	Operations   []PlanOperation `json:"operations"`
	// Invariants are what this plan's result must satisfy. An empty list is
	// the normal case and means the plan asserts nothing.
	Invariants  []PlanInvariant     `json:"invariants,omitempty"`
	Preview     *PlanPreview        `json:"preview,omitempty"`
	Preparation *PlanPreparation    `json:"preparation,omitempty"`
	Conflict    *PlanConflictReason `json:"conflict,omitempty"`
	Events      []PlanEvent         `json:"events"`
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

// loadPlanRecords reads the durable plans of one workspace.
//
// A record this build cannot read is skipped, named in the log and left on
// disk untouched. It used to be a fatal error, and the consequence was out of
// all proportion: one plan file written by a newer build stopped its workspace
// from opening at all, and the service crash-looped because every restart
// failed on the same file. A plan is one proposal; it is never worth an entire
// workspace, let alone the service.
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
			log.Printf("huyang: plan record %s could not be read and is ignored: %v", entry.Name(), err)
			continue
		}
		var record persistedPlan
		if err := json.Unmarshal(content, &record); err != nil {
			log.Printf("huyang: plan record %s could not be decoded and is ignored: %v", entry.Name(), err)
			continue
		}
		if record.Version != planRecordVersion && record.Version != planRecordVersionLegacy {
			log.Printf("huyang: plan record %s is version %d, which this build does not read; it is ignored and left on disk",
				entry.Name(), record.Version)
			continue
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
	return w.CreatePlanWithInvariants(operations, nil)
}

// CreatePlanWithInvariants creates a plan that also declares what its result
// must satisfy. The invariants are stored pending: nothing is proven until a
// prepare evaluates them against staged bytes.
func (w *Workspace) CreatePlanWithInvariants(operations []PlanOperation, invariants []PlanInvariant) (PlanRecord, error) {
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	declared, err := normalizeInvariants(invariants)
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
		BaseStateSeq: w.Identity().StateSeq, Operations: normalized, Invariants: declared,
		CreatedAt: now, UpdatedAt: now,
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

// CommittedPlansBetween lists the applied plans whose canonical revision falls
// inside a range, oldest first. They are the only place the bytes on both
// sides of a past change are still kept: an ordinary edit's receipt holds
// hashes and byte counts, which prove what changed without saying what it
// meant.
func (w *Workspace) CommittedPlansBetween(fromSeq, toSeq uint64) []PlanRecord {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	var found []PlanRecord
	for _, plan := range w.plans {
		if plan.State != PlanCommitted || plan.Preparation == nil {
			continue
		}
		sequence, err := revisionSequence(plan.Preparation.CanonicalRevision)
		if err != nil || sequence <= fromSeq || sequence > toSeq {
			continue
		}
		found = append(found, clonePlan(plan))
	}
	sort.Slice(found, func(i, j int) bool {
		left, _ := revisionSequence(found[i].Preparation.CanonicalRevision)
		right, _ := revisionSequence(found[j].Preparation.CanonicalRevision)
		return left < right
	})
	return found
}

// revisionSequence reads the number out of a wsrev_N token.
func revisionSequence(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil {
		return 0, err
	}
	return sequence, nil
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
	operations, err := applyPlanEdit(cloneOperations(plan.Operations), edit)
	if err != nil {
		return PlanRecord{}, err
	}
	normalized, err := w.normalizeOperations(operations)
	if err != nil {
		return PlanRecord{}, err
	}
	invariants := plan.Invariants
	if edit.Invariants != nil {
		if invariants, err = normalizeInvariants(*edit.Invariants); err != nil {
			return PlanRecord{}, err
		}
	}
	plan.Operations = normalized
	// An edit changes the bytes every answer was about, so the proofs go with
	// them. The declarations stay: what the plan must satisfy did not change
	// unless the caller said so.
	plan.Invariants = resetInvariantProofs(invariants)
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

// applyPlanEdit returns the operation list after one edit mode is applied to a private
// copy of the current operations.
func applyPlanEdit(operations []PlanOperation, edit PlanEdit) ([]PlanOperation, error) {
	switch edit.Mode {
	case "add":
		return append(operations, edit.Operations...), nil
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
			return nil, errors.New("update names an unknown op_id")
		}
		return operations, nil
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
		return filtered, nil
	case "reorder":
		if len(edit.OpIDs) != len(operations) {
			return nil, errors.New("reorder must name every op_id exactly once")
		}
		byID := make(map[string]PlanOperation, len(operations))
		for _, operation := range operations {
			byID[operation.OpID] = operation
		}
		reordered := make([]PlanOperation, 0, len(operations))
		for _, id := range edit.OpIDs {
			operation, found := byID[id]
			if !found {
				return nil, errors.New("reorder contains an unknown or duplicate op_id")
			}
			reordered = append(reordered, operation)
			delete(byID, id)
		}
		return reordered, nil
	case "replace_all":
		return edit.Operations, nil
	default:
		return nil, fmt.Errorf("unknown plan edit mode %q", edit.Mode)
	}
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
		if err := normalizeIndentation(operation); err != nil {
			return nil, err
		}
		normalize, known := operationNormalizers[operation.Kind]
		if !known {
			return nil, fmt.Errorf("%s has unknown operation kind %q", operation.OpID, operation.Kind)
		}
		if err := normalize(w, operation); err != nil {
			return nil, err
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

// operationNormalizers validates and completes one operation per kind. Edit kinds bind
// their target to an exact file range; file kinds bind a missing revision to the current
// document; provider kinds only check that a target is named.
var operationNormalizers = map[OperationKind]func(*Workspace, *PlanOperation) error{
	OperationReplaceSymbol:  (*Workspace).normalizeEditTarget,
	OperationDeleteSymbol:   (*Workspace).normalizeEditTarget,
	OperationInsertBefore:   (*Workspace).normalizeEditTarget,
	OperationInsertAfter:    (*Workspace).normalizeEditTarget,
	OperationReplaceRange:   (*Workspace).normalizeEditTarget,
	OperationCreateFile:     (*Workspace).normalizeCreateFile,
	OperationDeleteFile:     (*Workspace).normalizeDeleteFile,
	OperationMoveFile:       (*Workspace).normalizeMoveFile,
	OperationCopyFile:       (*Workspace).normalizeCopyFile,
	OperationReplaceMatches: (*Workspace).normalizeReplaceMatches,
}

func normalizeIndentation(operation *PlanOperation) error {
	if operation.Indentation == "" {
		operation.Indentation = "exact"
	}
	if operation.Indentation != "exact" && operation.Indentation != "syntax_anchor" && operation.Indentation != "formatter" {
		return fmt.Errorf("%s has invalid indentation mode %q", operation.OpID, operation.Indentation)
	}
	if operation.Indentation == "syntax_anchor" && (operation.Target == nil || operation.Target.Handle == "") {
		return fmt.Errorf("%s syntax_anchor requires one semantic node handle", operation.OpID)
	}
	return nil
}

func (w *Workspace) normalizeEditTarget(operation *PlanOperation) error {
	if operation.Target == nil {
		return fmt.Errorf("%s requires target", operation.OpID)
	}
	if operation.Target.FileRange == nil && operation.Target.Handle != "" {
		handle, err := w.editRangeFromHandle(operation)
		if err != nil {
			return err
		}
		operation.Target.FileRange = &handle
	}
	if operation.Target.FileRange == nil && operation.Target.SymbolLocator != nil {
		handle, err := w.editRangeFromLocator(operation)
		if err != nil {
			return err
		}
		operation.Target.FileRange = &handle
	}
	if operation.Target.FileRange == nil {
		return fmt.Errorf("%s requires a resolvable handle or file_range", operation.OpID)
	}
	if operation.Kind == OperationDeleteSymbol {
		operation.Content = ""
	}
	return nil
}

func (w *Workspace) editRangeFromHandle(operation *PlanOperation) (RangeHandle, error) {
	resolution, err := w.ResolveHandle(operation.Target.Handle)
	if err != nil {
		return RangeHandle{}, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	if resolution.Status == ResolutionConflicted {
		return RangeHandle{}, fmt.Errorf("%s: %s", operation.OpID, resolution.Code)
	}
	if operation.Indentation == "syntax_anchor" && (resolution.Original.Kind == "range" || resolution.Original.Kind == "match") {
		return RangeHandle{}, fmt.Errorf("%s syntax_anchor target is not one parsed node", operation.OpID)
	}
	handle, err := resolution.RangeHandle()
	if err != nil {
		return RangeHandle{}, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	return handle, nil
}

func (w *Workspace) editRangeFromLocator(operation *PlanOperation) (RangeHandle, error) {
	locator := operation.Target.SymbolLocator
	selected, err := w.ResolveSymbolLocator(locator.Path, locator.NamePath)
	if err != nil {
		return RangeHandle{}, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	resolution, err := w.ResolveHandle(selected.Handle)
	if err != nil {
		return RangeHandle{}, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	handle, err := resolution.RangeHandle()
	if err != nil {
		return RangeHandle{}, fmt.Errorf("%s: %w", operation.OpID, err)
	}
	return handle, nil
}

// snapshotRevision binds a file operation that named no revision to the current revision
// of its target, refusing a target whose presence contradicts the operation.
func (w *Workspace) snapshotRevision(opID, label, path string, expectMissing bool) (RevisionID, error) {
	snapshot, err := w.Snapshot(path, ProviderLayer{})
	if err != nil {
		return "", fmt.Errorf("%s: snapshot %s: %w", opID, label, err)
	}
	missing := snapshot.Disk.Kind == ObjectMissing
	if expectMissing && !missing {
		return "", fmt.Errorf("%s: %s already exists", opID, label)
	}
	if !expectMissing && missing {
		return "", fmt.Errorf("%s: %s is missing", opID, label)
	}
	return snapshot.Revision, nil
}

func (w *Workspace) normalizeCreateFile(operation *PlanOperation) error {
	if operation.Path == "" {
		return fmt.Errorf("%s requires path", operation.OpID)
	}
	if operation.Revision != "" {
		return nil
	}
	revision, err := w.snapshotRevision(operation.OpID, "create target", operation.Path, true)
	if err != nil {
		return err
	}
	operation.Revision = revision
	return nil
}

func (w *Workspace) normalizeDeleteFile(operation *PlanOperation) error {
	if operation.Path == "" {
		return fmt.Errorf("%s requires path", operation.OpID)
	}
	if operation.Revision != "" {
		return nil
	}
	revision, err := w.snapshotRevision(operation.OpID, "delete target", operation.Path, false)
	if err != nil {
		return err
	}
	operation.Revision = revision
	return nil
}

func (w *Workspace) normalizeMoveFile(operation *PlanOperation) error {
	if operation.From == "" || operation.To == "" {
		return fmt.Errorf("%s requires from and to", operation.OpID)
	}
	if operation.Revision == "" {
		revision, err := w.snapshotRevision(operation.OpID, "move source", operation.From, false)
		if err != nil {
			return err
		}
		operation.Revision = revision
	}
	if operation.DestinationRevision == "" {
		revision, err := w.snapshotRevision(operation.OpID, "move destination", operation.To, true)
		if err != nil {
			return err
		}
		operation.DestinationRevision = revision
	}
	return nil
}

// normalizeCopyFile binds the copy's source hash (a source outside the
// workspace has no revision) and the missing destination's revision.
func (w *Workspace) normalizeCopyFile(operation *PlanOperation) error {
	if operation.From == "" || operation.To == "" {
		return fmt.Errorf("%s requires from and to", operation.OpID)
	}
	source, err := w.TransferSource(operation.From, operation.ExpectedSHA256)
	if err != nil {
		return fmt.Errorf("%s: %w", operation.OpID, err)
	}
	operation.ExpectedSHA256 = source.SHA256
	if operation.DestinationRevision == "" {
		revision, err := w.snapshotRevision(operation.OpID, "copy destination", operation.To, true)
		if err != nil {
			return err
		}
		operation.DestinationRevision = revision
	}
	return nil
}

func (w *Workspace) normalizeReplaceMatches(operation *PlanOperation) error {
	if operation.Target == nil || operation.Target.Handle == "" {
		return fmt.Errorf("%s requires a result-set handle", operation.OpID)
	}
	if _, err := w.ResolveAllMatches(ResultSetID(operation.Target.Handle)); err != nil {
		return fmt.Errorf("%s: %w", operation.OpID, err)
	}
	return nil
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
			if before, after, ok := implicitEdge(left, right); ok {
				addEdge(before, after)
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

// implicitEdge is the ordering the operations' paths imply between one
// pair: range edits of one file apply from its end toward its beginning; a
// file is created before it is edited and edited before it is deleted or
// moved; a copy reads its source after any edit of it and lands before any
// edit of its destination. It returns the op_ids in apply order.
func implicitEdge(left, right PlanOperation) (string, string, bool) {
	leftPath, leftStart, leftEdit := operationRange(left)
	rightPath, rightStart, rightEdit := operationRange(right)
	switch {
	case leftEdit && rightEdit && leftPath == rightPath && leftStart < rightStart:
		return right.OpID, left.OpID, true
	case left.Kind == OperationCreateFile && left.Path != "" && rightEdit && rightPath == left.Path:
		return left.OpID, right.OpID, true
	case leftEdit && right.Kind == OperationDeleteFile && leftPath == right.Path:
		return left.OpID, right.OpID, true
	case leftEdit && (right.Kind == OperationMoveFile || right.Kind == OperationCopyFile) && leftPath == right.From:
		return left.OpID, right.OpID, true
	case left.Kind == OperationCopyFile && rightEdit && rightPath == left.To:
		return left.OpID, right.OpID, true
	}
	return "", "", false
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

// The preview machinery lives in plan_preview.go.

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
