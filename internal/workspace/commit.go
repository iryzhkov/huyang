package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const commitJournalVersion = 1

type CommitJournalState string

const (
	CommitJournalPrepared         CommitJournalState = "prepared"
	CommitJournalApplying         CommitJournalState = "applying"
	CommitJournalCommitted        CommitJournalState = "committed"
	CommitJournalRecoveryRequired CommitJournalState = "recovery_required"
	CommitJournalRolledBack       CommitJournalState = "rolled_back"
)

type CommitPathProgress string

const (
	CommitPathPending  CommitPathProgress = "pending"
	CommitPathApplied  CommitPathProgress = "applied"
	CommitPathRestored CommitPathProgress = "restored"
)

type CommitJournalEntry struct {
	Path      string             `json:"path"`
	Preimage  []byte             `json:"preimage"`
	Postimage []byte             `json:"postimage"`
	Before    DiskSnapshot       `json:"before"`
	After     DiskSnapshot       `json:"after"`
	Progress  CommitPathProgress `json:"progress"`
}

type CommitJournal struct {
	Version           int                  `json:"version"`
	WorkspaceID       ID                   `json:"workspace_id"`
	PlanID            string               `json:"plan_id"`
	PlanRevision      uint64               `json:"plan_revision"`
	PreparedRevision  string               `json:"prepared_revision"`
	CompensatesPlanID string               `json:"compensates_plan_id,omitempty"`
	CanonicalRevision string               `json:"canonical_revision,omitempty"`
	State             CommitJournalState   `json:"state"`
	Entries           []CommitJournalEntry `json:"entries"`
	LastError         string               `json:"last_error,omitempty"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

// planStageRequest reconstructs the exact prepared write set and its filesystem metadata.
func (w *Workspace) planStageRequest(plan PlanRecord) (PlanStageRequest, error) {
	if plan.Preview == nil || plan.Preview.Outcome != "ok" {
		return PlanStageRequest{}, errors.New("plan has no successful preview")
	}
	request := PlanStageRequest{PlanID: plan.PlanID, PlanRevision: plan.PlanRevision}
	byPath := make(map[string]int, len(plan.Preview.Diffs))
	for _, diff := range plan.Preview.Diffs {
		absolute, err := w.confinedPath(diff.Path)
		if err != nil {
			return PlanStageRequest{}, err
		}
		beforeDisk, beforeBytes, err := inspectPath(absolute)
		if err != nil {
			return PlanStageRequest{}, err
		}
		if !bytes.Equal(beforeBytes, diff.Before) {
			return PlanStageRequest{}, fmt.Errorf("commit_precondition_changed: %s content no longer matches preview", diff.Path)
		}
		afterDisk := beforeDisk
		afterDisk.Device, afterDisk.Inode, afterDisk.MTimeNS = 0, 0, 0
		afterDisk.Size = int64(len(diff.After))
		if afterDisk.Kind == ObjectMissing {
			afterDisk = DiskSnapshot{Kind: ObjectRegularText, Size: int64(len(diff.After)), Mode: uint32(0o644)}
		}
		if afterDisk.Kind == ObjectSymlink {
			afterDisk.SymlinkTarget = string(diff.After)
		}
		byPath[diff.Path] = len(request.Files)
		request.Files = append(request.Files, PlanStageFile{
			Path: diff.Path, Before: append([]byte(nil), beforeBytes...), After: append([]byte(nil), diff.After...),
			BeforeExists: beforeDisk.Kind != ObjectMissing, AfterExists: true,
			BeforeDisk: beforeDisk, AfterDisk: afterDisk,
		})
	}
	fileFor := func(path string) *PlanStageFile {
		index, ok := byPath[path]
		if !ok {
			return nil
		}
		return &request.Files[index]
	}
	for _, operation := range plan.Operations {
		if operation.Indentation == "formatter" {
			request.RequiresFormatter = true
		}
		switch operation.Kind {
		case OperationCreateFile:
			if file := fileFor(operation.Path); file != nil {
				file.AfterExists = true
				file.AfterDisk = DiskSnapshot{Kind: ObjectRegularText, Size: int64(len(file.After)), Mode: uint32(0o644)}
			}
		case OperationDeleteFile:
			if file := fileFor(operation.Path); file != nil {
				file.AfterExists = false
				file.AfterDisk = DiskSnapshot{Kind: ObjectMissing}
			}
		case OperationMoveFile:
			source, destination := fileFor(operation.From), fileFor(operation.To)
			if source != nil {
				source.AfterExists = false
				source.AfterDisk = DiskSnapshot{Kind: ObjectMissing}
			}
			if destination != nil && source != nil {
				destination.AfterExists = true
				destination.AfterDisk = source.BeforeDisk
				destination.AfterDisk.Device, destination.AfterDisk.Inode, destination.AfterDisk.MTimeNS = 0, 0, 0
				destination.AfterDisk.Size = int64(len(destination.After))
				if destination.AfterDisk.Kind == ObjectSymlink {
					destination.AfterDisk.SymlinkTarget = string(destination.After)
				}
			}
		}
	}
	sort.Slice(request.Files, func(i, j int) bool { return request.Files[i].Path < request.Files[j].Path })
	return request, nil
}

func (w *Workspace) commitJournalPath(planID string) string {
	if w.stateDir == "" {
		return ""
	}
	return filepath.Join(w.stateDir, "commit-journals", string(w.Identity().ID), planID+".json")
}

func (w *Workspace) writeCommitJournal(path string, journal *CommitJournal) error {
	journal.UpdatedAt = time.Now().UTC()
	content, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode commit journal: %w", err)
	}
	content = append(content, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".journal-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func (w *Workspace) loadCommitJournal(planID string) (CommitJournal, error) {
	return loadCommitJournalFile(w.commitJournalPath(planID))
}

func (w *Workspace) fireCommitFault(point, path string) error {
	if w.commitFault == nil {
		return nil
	}
	return w.commitFault(point, path)
}

func commitEntryOrder(entries []CommitJournalEntry) []int {
	order := make([]int, len(entries))
	for i := range entries {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		left, right := entries[order[i]], entries[order[j]]
		leftDelete, rightDelete := left.After.Kind == ObjectMissing, right.After.Kind == ObjectMissing
		if leftDelete != rightDelete {
			return !leftDelete
		}
		return left.Path < right.Path
	})
	return order
}

func stateMatchesSnapshot(path string, expected DiskSnapshot, content []byte) (bool, error) {
	current, currentBytes, err := inspectPath(path)
	if err != nil {
		return false, err
	}
	return equalDisk(current, expected) && bytes.Equal(currentBytes, content), nil
}

func applyCommitEntry(path string, entry CommitJournalEntry) error {
	if entry.After.Kind != ObjectMissing {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	switch entry.After.Kind {
	case ObjectMissing:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	case ObjectRegularText, ObjectBinary:
		if err := atomicWrite(path, entry.Postimage, fs.FileMode(entry.After.Mode)); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	case ObjectSymlink:
		if err := atomicSymlink(path, entry.After.SymlinkTarget); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	default:
		return fmt.Errorf("unsupported commit postimage kind %s for %s", entry.After.Kind, entry.Path)
	}
}

func atomicSymlink(path, target string) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".huyang-link-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		return err
	}
	if err := os.Symlink(target, name); err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(name)
		}
	}()
	if err := os.Rename(name, path); err != nil {
		return err
	}
	remove = false
	return nil
}

func (w *Workspace) markCommitRecovery(plan PlanRecord, journalPath string, journal *CommitJournal, cause error) (PlanRecord, error) {
	journal.State = CommitJournalRecoveryRequired
	journal.LastError = cause.Error()
	if err := w.writeCommitJournal(journalPath, journal); err != nil {
		cause = fmt.Errorf("%w; persist recovery journal: %v", cause, err)
	}
	record, transitionErr := w.transitionPlan(
		plan.PlanID, plan.PlanRevision, PlanRecoveryRequired, "commit_failed", cause.Error(), plan.Preparation,
	)
	if transitionErr != nil {
		return record, fmt.Errorf("%w; persist recovery state: %v", cause, transitionErr)
	}
	return record, cause
}

func (w *Workspace) recordCanonicalCommit() Identity {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.identity.StateSeq++
	w.documents = make(map[string]cachedDocument)
	return w.identity
}

func (w *Workspace) CommitPreparedTransaction(ctx context.Context, transactionID, operationID string, files []PlanStageFile, finalize func(context.Context) error) (Identity, error) {
	if transactionID == "" || operationID == "" {
		return Identity{}, errors.New("legacy transaction requires transaction and operation IDs")
	}
	if len(files) == 0 {
		return w.Identity(), nil
	}
	now := time.Now().UTC()
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: w.Identity().ID, PlanID: transactionID,
		PlanRevision: 1, PreparedRevision: "legacy:" + operationID, State: CommitJournalPrepared,
		CreatedAt: now, UpdatedAt: now,
	}
	seen := map[string]bool{}
	for _, file := range files {
		if file.Path == "" || seen[file.Path] {
			return Identity{}, fmt.Errorf("invalid prepared write path %q", file.Path)
		}
		seen[file.Path] = true
		absolute, err := w.confinedPath(file.Path)
		if err != nil {
			return Identity{}, err
		}
		matches, err := stateMatchesSnapshot(absolute, file.BeforeDisk, file.Before)
		if err != nil {
			return Identity{}, err
		}
		if !matches {
			return Identity{}, fmt.Errorf("commit_precondition_changed: %s changed before commit", file.Path)
		}
		journal.Entries = append(journal.Entries, CommitJournalEntry{
			Path: file.Path, Preimage: append([]byte(nil), file.Before...), Postimage: append([]byte(nil), file.After...),
			Before: file.BeforeDisk, After: file.AfterDisk, Progress: CommitPathPending,
		})
	}
	journalPath := w.commitJournalPath(transactionID)
	if journalPath == "" {
		return Identity{}, errors.New("commit journal state directory is required")
	}
	if err := w.persistCommitJournal(journalPath, &journal, "prepare_journal"); err != nil {
		return Identity{}, err
	}
	journal.State = CommitJournalApplying
	if err := w.persistCommitJournal(journalPath, &journal, "applying_journal"); err != nil {
		return Identity{}, err
	}
	recoveryFailure := func(cause error) (Identity, error) {
		journal.State = CommitJournalRecoveryRequired
		_ = w.persistCommitJournal(journalPath, &journal, "recovery_required_journal")
		return Identity{}, fmt.Errorf("commit_recovery_required: %w", cause)
	}
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return recoveryFailure(err)
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return recoveryFailure(err)
		}
		absolute, err := w.confinedPath(entry.Path)
		if err == nil {
			err = applyCommitEntry(absolute, *entry)
		}
		if err != nil {
			return recoveryFailure(err)
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return recoveryFailure(err)
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(journalPath, &journal, "progress_journal"); err != nil {
			return recoveryFailure(err)
		}
	}
	if finalize != nil {
		if err := finalize(ctx); err != nil {
			return recoveryFailure(err)
		}
	}
	identity := w.recordCanonicalCommit()
	journal.State = CommitJournalCommitted
	journal.CanonicalRevision = fmt.Sprintf("wsrev_%d", identity.StateSeq)
	if err := w.persistCommitJournal(journalPath, &journal, "committed_journal"); err != nil {
		return recoveryFailure(err)
	}
	return identity, nil
}

// CommitPlan applies one READY or explicitly accepted PROVISIONAL prepared revision through a durable per-path write-ahead
// journal. The journal is fsynced before canonical mutation; incomplete applications remain
// recorded for S13 startup recovery.
func (w *Workspace) CommitPlan(ctx context.Context, planID string, expected uint64, preparedRevision string, stager PlanStager) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, errors.New("provider_unavailable: commit requires a provider")
	}
	if err := w.CheckProviderAccess(planID); err != nil {
		return PlanRecord{}, err
	}
	plan, err := w.InspectPlan(planID, expected)
	if err != nil {
		return PlanRecord{}, err
	}
	if plan.State == PlanCommitted && plan.Preparation != nil && plan.Preparation.PreparedRevision == preparedRevision {
		return plan, nil
	}
	if (plan.State != PlanReady && plan.State != PlanProvisional) || plan.Preparation == nil {
		return PlanRecord{}, errors.New("plan is not READY or PROVISIONAL")
	}
	if preparedRevision == "" || plan.Preparation.PreparedRevision != preparedRevision {
		return PlanRecord{}, errors.New("prepared_revision_changed")
	}
	durablePreparation := false
	if source, ok := stager.(PreparedPlanStager); ok {
		_, _, durablePreparation = source.PreparedRequest()
	}
	if plan.Preparation.ProviderEpoch != stager.Epoch() && !durablePreparation {
		return PlanRecord{}, errors.New("workspace_epoch_changed")
	}
	freshPreview := w.buildPreview(plan)
	if freshPreview.Outcome != "ok" || freshPreview.PreviewRevision != plan.Preview.PreviewRevision {
		return PlanRecord{}, errors.New("commit_precondition_changed: plan preview is stale")
	}
	request, err := w.planStageRequest(plan)
	if err != nil {
		return PlanRecord{}, err
	}
	if source, ok := stager.(PreparedPlanStager); ok {
		if prepared, _, available := source.PreparedRequest(); available {
			request = prepared
		}
	}
	now := time.Now().UTC()
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: w.Identity().ID, PlanID: planID,
		PlanRevision: expected, PreparedRevision: preparedRevision, State: CommitJournalPrepared,
		CreatedAt: now, UpdatedAt: now,
	}
	for _, file := range request.Files {
		journal.Entries = append(journal.Entries, CommitJournalEntry{
			Path: file.Path, Preimage: append([]byte(nil), file.Before...), Postimage: append([]byte(nil), file.After...),
			Before: file.BeforeDisk, After: file.AfterDisk, Progress: CommitPathPending,
		})
	}
	journalPath := w.commitJournalPath(planID)
	if journalPath == "" {
		return PlanRecord{}, errors.New("commit journal state directory is required")
	}
	if err := w.persistCommitJournal(journalPath, &journal, "prepare_journal"); err != nil {
		return PlanRecord{}, err
	}
	if _, err := w.transitionPlan(planID, expected, PlanCommitting, "commit_started", "pending", plan.Preparation); err != nil {
		return w.markCommitRecovery(plan, journalPath, &journal, err)
	}
	for _, entry := range journal.Entries {
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
		matches, err := stateMatchesSnapshot(absolute, entry.Before, entry.Preimage)
		if err != nil || !matches {
			if err == nil {
				err = fmt.Errorf("commit_precondition_changed: %s changed before commit", entry.Path)
			}
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
	}
	journal.State = CommitJournalApplying
	if err := w.persistCommitJournal(journalPath, &journal, "applying_journal"); err != nil {
		return w.markCommitRecovery(plan, journalPath, &journal, err)
	}
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
		absolute, err := w.confinedPath(entry.Path)
		if err == nil {
			err = applyCommitEntry(absolute, *entry)
		}
		if err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(journalPath, &journal, "progress_journal"); err != nil {
			return w.markCommitRecovery(plan, journalPath, &journal, err)
		}
	}
	if err := stager.Commit(ctx, planID); err != nil {
		return w.markCommitRecovery(plan, journalPath, &journal, fmt.Errorf("provider resync: %w", err))
	}
	canonicalFromRevision := fmt.Sprintf("wsrev_%d", w.Identity().StateSeq)
	identity := w.recordCanonicalCommit()
	preparation := *plan.Preparation
	preparation.CanonicalFromRevision = canonicalFromRevision
	preparation.CanonicalChanged = true
	preparation.CanonicalRevision = fmt.Sprintf("wsrev_%d", identity.StateSeq)
	preparation.JournalID = planID
	preparation.CommittedDiffs = make([]ExactDiff, 0, len(request.Files))
	for _, file := range request.Files {
		before, after := file.Before, file.After
		if !file.BeforeExists {
			before = nil
		}
		if !file.AfterExists {
			after = nil
		}
		preparation.CommittedDiffs = append(preparation.CommittedDiffs, exactDiff(file.Path, before, after, 0, len(before), after))
	}
	result, err := w.transitionPlan(planID, expected, PlanCommitted, "commit", "ok", &preparation)
	if err != nil {
		return w.markCommitRecovery(plan, journalPath, &journal, err)
	}
	journal.State = CommitJournalCommitted
	journal.CanonicalRevision = preparation.CanonicalRevision
	if err := w.persistCommitJournal(journalPath, &journal, "committed_journal"); err != nil {
		return w.markCommitRecovery(result, journalPath, &journal, err)
	}
	w.prepareMu.Lock()
	delete(w.activePlans, planID)
	w.prepareMu.Unlock()
	return result, nil
}
