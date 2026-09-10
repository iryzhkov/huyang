package workspace

import (
	"bytes"
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
			return fmt.Errorf("commit_recovery_required: load %s: %w", entry.Name(), err)
		}
		if journal.WorkspaceID != w.Identity().ID {
			return fmt.Errorf("commit_recovery_required: journal %s belongs to workspace %s", entry.Name(), journal.WorkspaceID)
		}
		switch journal.State {
		case CommitJournalCommitted:
			continue
		case CommitJournalRolledBack:
			if err := w.markPlanRecovered(journal.PlanID, journal.PlanRevision); err != nil {
				return err
			}
		case CommitJournalPrepared, CommitJournalApplying, CommitJournalRecoveryRequired:
			if err := w.recoverCommitJournal(path, &journal); err != nil {
				return err
			}
		default:
			return fmt.Errorf("commit_recovery_required: journal %s has unknown state %q", entry.Name(), journal.State)
		}
	}
	return nil
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

func stateMatchesJournalImage(path string, expected DiskSnapshot, content []byte) (bool, error) {
	current, currentBytes, err := inspectPath(path)
	if err != nil {
		return false, err
	}
	if current.Kind != expected.Kind || current.Mode != expected.Mode ||
		current.Size != expected.Size || current.SymlinkTarget != expected.SymlinkTarget {
		return false, nil
	}
	return bytes.Equal(currentBytes, content), nil
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
	return fmt.Errorf("commit_recovery_required: %w", cause)
}

func (w *Workspace) markPlanRecovered(planID string, revision uint64) error {
	w.plansMu.Lock()
	defer w.plansMu.Unlock()
	plan, ok := w.plans[planID]
	if !ok {
		return nil
	}
	plan.State = PlanRolledBack
	plan.Preparation = nil
	plan.UpdatedAt = time.Now().UTC()
	plan.Events = append(plan.Events, PlanEvent{
		Action: "startup_recovery", PlanRevision: revision, Outcome: "preimages_restored", At: plan.UpdatedAt,
	})
	w.plans[planID] = plan
	return w.persistPlansLocked()
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
		return CompensationResult{}, errors.New("provider_unavailable: compensation requires a provider")
	}
	w.plansMu.Lock()
	plan, known := w.plans[planID]
	w.plansMu.Unlock()
	if !known || plan.State != PlanCommitted || plan.Preparation == nil || plan.Preparation.JournalID != planID {
		return CompensationResult{}, errors.New("committed transaction is not available for compensation")
	}
	source, err := w.loadCommitJournal(planID)
	if err != nil {
		return CompensationResult{}, err
	}
	if source.State != CommitJournalCommitted || source.WorkspaceID != w.Identity().ID ||
		source.PlanID != planID || source.PlanRevision != plan.PlanRevision {
		return CompensationResult{}, errors.New("committed transaction journal does not match the plan")
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
	for _, entry := range journal.Entries {
		absolute, pathErr := w.confinedPath(entry.Path)
		if pathErr != nil {
			return result, pathErr
		}
		matches, matchErr := stateMatchesJournalImage(absolute, entry.Before, entry.Preimage)
		if matchErr != nil || !matches {
			if matchErr != nil {
				return result, matchErr
			}
			return result, fmt.Errorf("commit_precondition_changed: %s no longer matches committed postimage", entry.Path)
		}
	}
	w.prepareMu.Lock()
	if _, exists := w.activePlans[transactionID]; exists {
		w.prepareMu.Unlock()
		return result, fmt.Errorf("workspace_busy: transaction %s is already active", transactionID)
	}
	w.activePlans[transactionID] = struct{}{}
	w.prepareMu.Unlock()
	release := true
	defer func() {
		if release {
			w.prepareMu.Lock()
			delete(w.activePlans, transactionID)
			w.prepareMu.Unlock()
		}
	}()
	if err := stager.Stage(ctx, w.compensationStageRequest(journal)); err != nil {
		_ = stager.Rollback(context.Background(), transactionID)
		return result, err
	}
	if err := w.persistCommitJournal(compensationPath, &journal, "prepare_journal"); err != nil {
		_ = stager.Rollback(context.Background(), transactionID)
		return result, err
	}
	release = false
	journal.State = CommitJournalApplying
	if err := w.persistCommitJournal(compensationPath, &journal, "applying_journal"); err != nil {
		return w.compensationRecovery(result, compensationPath, &journal, err)
	}
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return w.compensationRecovery(result, compensationPath, &journal, err)
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return w.compensationRecovery(result, compensationPath, &journal, err)
		}
		absolute, pathErr := w.confinedPath(entry.Path)
		if pathErr == nil {
			pathErr = applyCommitEntry(absolute, *entry)
		}
		if pathErr != nil {
			return w.compensationRecovery(result, compensationPath, &journal, pathErr)
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return w.compensationRecovery(result, compensationPath, &journal, err)
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(compensationPath, &journal, "progress_journal"); err != nil {
			return w.compensationRecovery(result, compensationPath, &journal, err)
		}
	}
	if err := stager.Commit(ctx, transactionID); err != nil {
		return w.compensationRecovery(result, compensationPath, &journal, fmt.Errorf("provider resync: %w", err))
	}
	identity := w.recordCanonicalCommit()
	journal.CanonicalRevision = fmt.Sprintf("wsrev_%d", identity.StateSeq)
	journal.State = CommitJournalCommitted
	if err := w.persistCommitJournal(compensationPath, &journal, "committed_journal"); err != nil {
		return w.compensationRecovery(result, compensationPath, &journal, err)
	}
	result.State = journal.State
	result.CanonicalRevision = journal.CanonicalRevision
	release = true
	return result, nil
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

