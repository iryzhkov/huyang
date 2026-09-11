package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const compensationPrefix = "undo_"

// Journal retention. Completed journals (committed or rolled back) are receipts: a
// committed journal is what CompensatePlan undoes from, and a rolled-back journal records
// that recovery finished. They are garbage-collected on workspace open and after each
// successful commit once they are older than commitJournalRetention, and the newest
// commitJournalRetainCount are kept beyond that only until the cap is exceeded. Prepared,
// applying and recovery-required journals are never collected.
const (
	commitJournalRetention   = 7 * 24 * time.Hour
	commitJournalRetainCount = 64
)

type CompensationResult struct {
	TransactionID     string             `json:"transaction_id"`
	CompensatesPlanID string             `json:"compensates_plan_id"`
	State             CommitJournalState `json:"state"`
	CanonicalRevision string             `json:"canonical_revision,omitempty"`
	JournalID         string             `json:"journal_id"`
}

func (w *Workspace) persistCommitJournal(path string, journal *CommitJournal, phase string) error {
	if err := w.fireCommitFault("before_"+phase, ""); err != nil {
		return err
	}
	if err := w.writeCommitJournal(path, journal); err != nil {
		return err
	}
	return w.fireCommitFault("after_"+phase, "")
}

func (w *Workspace) recoverCommitJournals() error {
	if w.stateDir == "" {
		return nil
	}
	directory := filepath.Join(w.stateDir, "commit-journals", string(w.Identity().ID))
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read commit journals: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		journal, err := loadCommitJournalFile(path)
		if err != nil {
			return Codedf(CodeCommitRecoveryRequired, "load %s: %w", entry.Name(), err)
		}
		if journal.WorkspaceID != w.Identity().ID {
			return Codedf(CodeCommitRecoveryRequired, "journal %s belongs to workspace %s", entry.Name(), journal.WorkspaceID)
		}
		switch journal.State {
		case CommitJournalCommitted:
			// The committed journal is written before the plan record claims COMMITTED, so a
			// plan still marked COMMITTING or RECOVERY_REQUIRED here finished its canonical
			// writes and only lost the receipt transition.
			if err := w.markPlanCommitted(journal); err != nil {
				return err
			}
		case CommitJournalRolledBack:
			if err := w.markPlanRecovered(journal.PlanID, journal.PlanRevision); err != nil {
				return err
			}
		case CommitJournalPrepared:
			// A prepared journal never reached applying, so no canonical byte was written and
			// there is nothing to restore; the journal is closed without touching any file.
			journal.State = CommitJournalRolledBack
			if journal.LastError == "" {
				journal.LastError = "commit interrupted before any canonical write"
			}
			if err := w.persistCommitJournal(path, &journal, "recovered_journal"); err != nil {
				return w.recoveryConflict(path, &journal, fmt.Errorf("persist unstarted journal: %w", err))
			}
			if err := w.markPlanRecovered(journal.PlanID, journal.PlanRevision); err != nil {
				return err
			}
		case CommitJournalApplying, CommitJournalRecoveryRequired:
			if err := w.recoverCommitJournal(path, &journal); err != nil {
				return err
			}
		default:
			return Codedf(CodeCommitRecoveryRequired, "journal %s has unknown state %q", entry.Name(), journal.State)
		}
	}
	w.collectCommitJournals()
	return nil
}

// collectCommitJournals removes completed journals beyond the retention window or the
// count cap. It is best effort: a journal that cannot be read or removed is left alone, and
// no journal in prepared, applying or recovery-required state is ever touched.
func (w *Workspace) collectCommitJournals() {
	if w.stateDir == "" {
		return
	}
	directory := filepath.Join(w.stateDir, "commit-journals", string(w.Identity().ID))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	type completed struct {
		path      string
		updatedAt time.Time
	}
	var finished []completed
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		journal, err := loadCommitJournalFile(path)
		if err != nil || journal.WorkspaceID != w.Identity().ID {
			continue
		}
		if journal.State != CommitJournalCommitted && journal.State != CommitJournalRolledBack {
			continue
		}
		finished = append(finished, completed{path: path, updatedAt: journal.UpdatedAt})
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].updatedAt.After(finished[j].updatedAt) })
	cutoff := time.Now().UTC().Add(-commitJournalRetention)
	for index, journal := range finished {
		if index < commitJournalRetainCount && !journal.updatedAt.Before(cutoff) {
			continue
		}
		_ = os.Remove(journal.path)
	}
}

func loadCommitJournalFile(path string) (CommitJournal, error) {
	var journal CommitJournal
	content, err := os.ReadFile(path)
	if err != nil {
		return journal, err
	}
	if err := jsonUnmarshalCommitJournal(content, &journal); err != nil {
		return journal, err
	}
	if journal.Version != commitJournalVersion {
		return journal, fmt.Errorf("unsupported commit journal version %d", journal.Version)
	}
	return journal, nil
}

func jsonUnmarshalCommitJournal(content []byte, journal *CommitJournal) error {
	// Kept beside recovery so corrupt startup state fails closed before a root opens.
	return json.Unmarshal(content, journal)
}

func (w *Workspace) validateRecoveryJournal(journal CommitJournal) error {
	seen := make(map[string]struct{}, len(journal.Entries))
	for _, entry := range journal.Entries {
		if entry.Path == "" {
			return errors.New("journal contains an empty path")
		}
		if _, ok := seen[entry.Path]; ok {
			return fmt.Errorf("journal contains duplicate path %s", entry.Path)
		}
		seen[entry.Path] = struct{}{}
		if _, err := w.confinedPath(entry.Path); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) recoverCommitJournal(path string, journal *CommitJournal) error {
	if err := w.validateRecoveryJournal(*journal); err != nil {
		return w.recoveryConflict(path, journal, err)
	}
	order := commitEntryOrder(journal.Entries)
	for position := len(order) - 1; position >= 0; position-- {
		entry := &journal.Entries[order[position]]
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return w.recoveryConflict(path, journal, err)
		}
		matchesPreimage, err := stateMatchesJournalImage(absolute, entry.Before, entry.Preimage)
		if err != nil {
			return w.recoveryConflict(path, journal, err)
		}
		if !matchesPreimage {
			matchesPostimage, matchErr := stateMatchesJournalImage(absolute, entry.After, entry.Postimage)
			if matchErr != nil {
				return w.recoveryConflict(path, journal, matchErr)
			}
			if !matchesPostimage {
				return w.recoveryConflict(path, journal, fmt.Errorf("%s matches neither journal preimage nor postimage", entry.Path))
			}
			if err := w.fireCommitFault("before_recovery_apply", entry.Path); err != nil {
				return w.recoveryConflict(path, journal, err)
			}
			// Recheck after the recovery hook and immediately before the durable replacement.
			matchesPostimage, matchErr = stateMatchesJournalImage(absolute, entry.After, entry.Postimage)
			if matchErr != nil || !matchesPostimage {
				if matchErr == nil {
					matchErr = fmt.Errorf("%s changed during recovery", entry.Path)
				}
				return w.recoveryConflict(path, journal, matchErr)
			}
			inverse := *entry
			inverse.After = entry.Before
			inverse.Postimage = append([]byte(nil), entry.Preimage...)
			if err := applyCommitEntry(absolute, inverse); err != nil {
				return w.recoveryConflict(path, journal, err)
			}
			if err := w.fireCommitFault("after_recovery_apply", entry.Path); err != nil {
				return w.recoveryConflict(path, journal, err)
			}
			matchesPreimage, err = stateMatchesJournalImage(absolute, entry.Before, entry.Preimage)
			if err != nil || !matchesPreimage {
				if err == nil {
					err = fmt.Errorf("%s did not reach its exact preimage", entry.Path)
				}
				return w.recoveryConflict(path, journal, err)
			}
		}
		entry.Progress = CommitPathRestored
		if err := w.persistCommitJournal(path, journal, "recovery_progress_journal"); err != nil {
			return w.recoveryConflict(path, journal, fmt.Errorf("persist progress for %s: %w", entry.Path, err))
		}
	}
	journal.State = CommitJournalRolledBack
	journal.LastError = ""
	if err := w.persistCommitJournal(path, journal, "recovered_journal"); err != nil {
		return w.recoveryConflict(path, journal, fmt.Errorf("persist completed recovery: %w", err))
	}
	return w.markPlanRecovered(journal.PlanID, journal.PlanRevision)
}

func (w *Workspace) recoveryConflict(path string, journal *CommitJournal, cause error) error {
	journal.State = CommitJournalRecoveryRequired
	journal.LastError = cause.Error()
	if err := w.writeCommitJournal(path, journal); err != nil {
		cause = fmt.Errorf("%w; persist recovery conflict: %v", cause, err)
	}
	return Coded(CodeCommitRecoveryRequired, cause)
}

// markPlanRecovered reconciles a plan with a rolled-back journal. Only a plan still waiting
// on recovery moves; a plan already reconciled, or one that refused its commit before any
// write, keeps its state so repeated opens do not rewrite it.
func (w *Workspace) markPlanRecovered(planID string, revision uint64) error {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok || plan.PlanRevision != revision {
		return nil
	}
	if plan.State != PlanCommitting && plan.State != PlanRecoveryRequired {
		return nil
	}
	if !canTransition(plan.State, PlanRolledBack) {
		return illegalTransition(planID, plan.State, PlanRolledBack)
	}
	plan.State = PlanRolledBack
	plan.Preparation = nil
	plan.Conflict = nil
	recordPlanEvent(&plan, "startup_recovery", "preimages_restored")
	w.plans[planID] = plan
	return w.finishPlanLocked(planID)
}

// markPlanCommitted reconciles a plan whose committed journal outlived its COMMITTED
// transition, as happens when the process dies between the two durable writes.
func (w *Workspace) markPlanCommitted(journal CommitJournal) error {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[journal.PlanID]
	if !ok || plan.PlanRevision != journal.PlanRevision {
		return nil
	}
	if plan.State != PlanCommitting && plan.State != PlanRecoveryRequired {
		return nil
	}
	if !canTransition(plan.State, PlanCommitted) {
		return illegalTransition(journal.PlanID, plan.State, PlanCommitted)
	}
	preparation := PlanPreparation{}
	if plan.Preparation != nil {
		preparation = *plan.Preparation
	}
	preparation.CanonicalChanged = true
	preparation.CanonicalRevision = journal.CanonicalRevision
	preparation.JournalID = journal.PlanID
	plan.State = PlanCommitted
	plan.Preparation = &preparation
	plan.Conflict = nil
	recordPlanEvent(&plan, "startup_reconcile", "committed_journal_found")
	w.plans[journal.PlanID] = plan
	return w.finishPlanLocked(journal.PlanID)
}

func (w *Workspace) compensationJournalPath(planID string) string {
	return w.commitJournalPath(compensationPrefix + planID)
}

func (w *Workspace) compensationJournal(source CommitJournal) CommitJournal {
	now := time.Now().UTC()
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: source.WorkspaceID,
		PlanID: compensationPrefix + source.PlanID, PlanRevision: source.PlanRevision,
		PreparedRevision: source.PreparedRevision, CompensatesPlanID: source.PlanID,
		State: CommitJournalPrepared, CreatedAt: now, UpdatedAt: now,
	}
	for _, entry := range source.Entries {
		journal.Entries = append(journal.Entries, CommitJournalEntry{
			Path: entry.Path, Preimage: append([]byte(nil), entry.Postimage...),
			Postimage: append([]byte(nil), entry.Preimage...), Before: entry.After,
			After: entry.Before, Progress: CommitPathPending,
		})
	}
	return journal
}

func (w *Workspace) compensationStageRequest(journal CommitJournal) PlanStageRequest {
	request := PlanStageRequest{PlanID: journal.PlanID, PlanRevision: journal.PlanRevision}
	for _, entry := range journal.Entries {
		request.Files = append(request.Files, PlanStageFile{
			Path: entry.Path, Before: append([]byte(nil), entry.Preimage...),
			After:        append([]byte(nil), entry.Postimage...),
			BeforeExists: entry.Before.Kind != ObjectMissing,
			AfterExists:  entry.After.Kind != ObjectMissing,
			BeforeDisk:   entry.Before, AfterDisk: entry.After,
		})
	}
	return request
}

// CompensatePlan performs durable undo as a new exact-image transaction. It refuses stale
// committed postimages, stages the inverse view in the provider, and records its own journal.
func (w *Workspace) CompensatePlan(ctx context.Context, planID string, stager PlanStager) (CompensationResult, error) {
	if stager == nil {
		return CompensationResult{}, Coded(CodeProviderUnavailable, errors.New("compensation requires a provider"))
	}
	source, err := w.compensationSource(planID)
	if err != nil {
		return CompensationResult{}, err
	}
	transactionID := compensationPrefix + planID
	result := CompensationResult{
		TransactionID: transactionID, CompensatesPlanID: planID,
		State: CommitJournalPrepared, JournalID: transactionID,
	}
	compensationPath := w.compensationJournalPath(planID)
	if existing, loadErr := loadCommitJournalFile(compensationPath); loadErr == nil && existing.State == CommitJournalCommitted {
		result.State = existing.State
		result.CanonicalRevision = existing.CanonicalRevision
		return result, nil
	}
	journal := w.compensationJournal(source)
	if err := w.validateRecoveryJournal(journal); err != nil {
		return result, err
	}
	if err := w.revalidateCompensation(journal); err != nil {
		return result, err
	}
	w.prepareMu.Lock()
	if _, exists := w.activePlans[transactionID]; exists {
		w.prepareMu.Unlock()
		return result, Codedf(CodeWorkspaceBusy, "transaction %s is already active", transactionID)
	}
	w.activePlans[transactionID] = struct{}{}
	w.prepareMu.Unlock()
	// The lease is released on every exit; the staged provider view is rolled back unless
	// its own commit already succeeded.
	stagerCommitted := false
	defer func() {
		if !stagerCommitted {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = stager.Rollback(rollbackCtx, transactionID)
			cancel()
		}
		w.prepareMu.Lock()
		delete(w.activePlans, transactionID)
		w.prepareMu.Unlock()
	}()
	if err := stager.Stage(ctx, w.compensationStageRequest(journal)); err != nil {
		return result, err
	}
	if err := w.persistCommitJournal(compensationPath, &journal, "prepare_journal"); err != nil {
		return result, err
	}
	written := false
	failure := func(cause error) (CompensationResult, error) {
		return w.compensationFailure(result, compensationPath, &journal, written, cause)
	}
	journal.State = CommitJournalApplying
	if err := w.persistCommitJournal(compensationPath, &journal, "applying_journal"); err != nil {
		return failure(err)
	}
	if written, err = w.applyJournalEntries(ctx, compensationPath, &journal, recheckCompensationEntry); err != nil {
		return failure(err)
	}
	if err := stager.Commit(ctx, transactionID); err != nil {
		return failure(fmt.Errorf("provider resync: %w", err))
	}
	stagerCommitted = true
	if _, err := w.sealCommittedJournal(compensationPath, &journal); err != nil {
		return failure(err)
	}
	result.State = journal.State
	result.CanonicalRevision = journal.CanonicalRevision
	w.collectCommitJournals()
	return result, nil
}

// compensationSource returns the committed journal a compensation undoes, refusing a plan
// that is not COMMITTED or whose journal does not describe that plan.
func (w *Workspace) compensationSource(planID string) (CommitJournal, error) {
	w.plansMu.Lock()
	plan, known := w.plans[planID]
	w.plansMu.Unlock()
	if !known || plan.State != PlanCommitted || plan.Preparation == nil || plan.Preparation.JournalID != planID {
		return CommitJournal{}, errors.New("committed transaction is not available for compensation")
	}
	source, err := w.loadCommitJournal(planID)
	if err != nil {
		return CommitJournal{}, err
	}
	if source.State != CommitJournalCommitted || source.WorkspaceID != w.Identity().ID ||
		source.PlanID != planID || source.PlanRevision != plan.PlanRevision {
		return CommitJournal{}, errors.New("committed transaction journal does not match the plan")
	}
	return source, nil
}

// revalidateCompensation refuses the undo when any canonical object no longer matches the
// committed postimage it would replace.
func (w *Workspace) revalidateCompensation(journal CommitJournal) error {
	for _, entry := range journal.Entries {
		absolute, pathErr := w.confinedPath(entry.Path)
		if pathErr != nil {
			return pathErr
		}
		matches, matchErr := stateMatchesJournalImage(absolute, entry.Before, entry.Preimage)
		if matchErr != nil {
			return matchErr
		}
		if !matches {
			return Codedf(CodeCommitPreconditionChanged, "%s no longer matches committed postimage", entry.Path)
		}
	}
	return nil
}

// recheckCompensationEntry compares the canonical object with the committed postimage the
// compensation undoes, immediately before the durable replacement. Committed postimages
// carry no inode or mtime identity, so the comparison is the lenient journal-image one.
func recheckCompensationEntry(absolute string, entry CommitJournalEntry) error {
	matches, err := stateMatchesJournalImage(absolute, entry.Before, entry.Preimage)
	if err != nil {
		return err
	}
	if !matches {
		return Codedf(CodeCommitPreconditionChanged, "%s changed during compensation", entry.Path)
	}
	return nil
}

// compensationFailure closes a failed compensation: before the first canonical write the
// journal is rolled back and the cause returned as is; after it both need recovery.
func (w *Workspace) compensationFailure(result CompensationResult, path string, journal *CommitJournal, written bool, cause error) (CompensationResult, error) {
	if !written {
		journal.State = CommitJournalRolledBack
		journal.LastError = cause.Error()
		_ = w.writeCommitJournal(path, journal)
		return result, cause
	}
	return w.compensationRecovery(result, path, journal, cause)
}

func (w *Workspace) compensationRecovery(result CompensationResult, path string, journal *CommitJournal, cause error) (CompensationResult, error) {
	journal.State = CommitJournalRecoveryRequired
	journal.LastError = cause.Error()
	if err := w.writeCommitJournal(path, journal); err != nil {
		cause = fmt.Errorf("%w; persist compensation recovery: %v", cause, err)
	}
	result.State = CommitJournalRecoveryRequired
	return result, cause
}
