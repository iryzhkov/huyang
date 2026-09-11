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
	"strings"
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
			return PlanStageRequest{}, Codedf(CodeCommitPreconditionChanged, "%s content no longer matches preview", diff.Path)
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, content, 0o600)
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

// applyCommitEntry makes one journal entry durable at path. A postimage that replaces an
// existing object goes through a same-directory temporary file and rename; a postimage for
// a path the journal recorded as missing is created without replacement, so a file a third
// party created inside the commit window is refused rather than clobbered.
//
// Parent directories are created here but are not journaled. Rollback deliberately leaves
// them in place: an empty directory carries no content of its own, and removing it could
// take away a directory another writer created in the same window.
func applyCommitEntry(path string, entry CommitJournalEntry) error {
	if entry.After.Kind != ObjectMissing {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	replace := entry.Before.Kind != ObjectMissing
	switch entry.After.Kind {
	case ObjectMissing:
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	case ObjectRegularText, ObjectBinary:
		if err := commitWrite(path, entry.Postimage, fs.FileMode(entry.After.Mode), replace); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	case ObjectSymlink:
		if err := commitSymlink(path, entry.After.SymlinkTarget, replace); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	default:
		return fmt.Errorf("unsupported commit postimage kind %s for %s", entry.After.Kind, entry.Path)
	}
}

// commitModeMask keeps every bit a journal mode may legitimately carry on disk. The plain
// permission mask would silently drop setuid, setgid and sticky bits, and recovery would
// then refuse the restored file for not matching its exact preimage.
const commitModeMask = fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky

func clobberRefusal(path string) error {
	return Codedf(CodeCommitPreconditionChanged, "%s was created by another writer during the commit window", path)
}

func commitWrite(path string, content []byte, mode fs.FileMode, replace bool) error {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".huyang-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	remove := true
	defer func() {
		_ = temp.Close()
		if remove {
			_ = os.Remove(name)
		}
	}()
	if _, err := temp.Write(content); err != nil {
		return err
	}
	if err := temp.Chmod(mode & commitModeMask); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if replace {
		if err := os.Rename(name, path); err != nil {
			return err
		}
	} else if err := renameNoReplace(name, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return clobberRefusal(path)
		}
		return err
	}
	remove = false
	return nil
}

func commitSymlink(path, target string, replace bool) error {
	if replace {
		return atomicSymlink(path, target)
	}
	// Symlink creation is itself atomic and refuses an existing name.
	if err := os.Symlink(target, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return clobberRefusal(path)
		}
		return err
	}
	return nil
}

// linkNoReplace is the portable no-replace rename: a hard link fails with EEXIST when the
// destination already exists, and the source is unlinked only after the link succeeded.
func linkNoReplace(oldPath, newPath string) error {
	if err := os.Link(oldPath, newPath); err != nil {
		return err
	}
	return os.Remove(oldPath)
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

// markCommitRecovery records a commit that failed after at least one canonical write. The
// journal and the plan both become recovery-required so startup recovery restores the
// preimages; the caller is responsible for releasing the lease.
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

// recordCanonicalCommit advances the workspace state once for a completed canonical write
// set, drops cached documents, and folds the written paths into the primed inventory so the
// next PrimeDocuments does not mistake Huyang's own commit for an external change.
func (w *Workspace) recordCanonicalCommit(entries []CommitJournalEntry) Identity {
	type inventoryChange struct {
		absolute string
		exists   bool
	}
	changes := make([]inventoryChange, 0, len(entries))
	for _, entry := range entries {
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			continue
		}
		changes = append(changes, inventoryChange{absolute: absolute, exists: entry.After.Kind != ObjectMissing})
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.identity.StateSeq++
	w.documents = make(map[string]cachedDocument)
	if w.pathsPrimed {
		for _, change := range changes {
			if change.exists {
				w.knownPaths[change.absolute] = struct{}{}
			} else {
				delete(w.knownPaths, change.absolute)
			}
		}
	}
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
			return Identity{}, Codedf(CodeCommitPreconditionChanged, "%s changed before commit", file.Path)
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
	written := false
	failure := func(cause error) (Identity, error) {
		if !written {
			journal.State = CommitJournalRolledBack
			journal.LastError = cause.Error()
			_ = w.persistCommitJournal(journalPath, &journal, "rolled_back_journal")
			return Identity{}, cause
		}
		journal.State = CommitJournalRecoveryRequired
		journal.LastError = cause.Error()
		_ = w.persistCommitJournal(journalPath, &journal, "recovery_required_journal")
		return Identity{}, Coded(CodeCommitRecoveryRequired, cause)
	}
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return failure(err)
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return failure(err)
		}
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return failure(err)
		}
		if err := recheckCommitEntry(absolute, *entry); err != nil {
			return failure(err)
		}
		written = true
		if err := applyCommitEntry(absolute, *entry); err != nil {
			return failure(err)
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return failure(err)
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(journalPath, &journal, "progress_journal"); err != nil {
			return failure(err)
		}
	}
	if finalize != nil {
		if err := finalize(ctx); err != nil {
			return failure(err)
		}
	}
	identity := w.recordCanonicalCommit(journal.Entries)
	journal.State = CommitJournalCommitted
	journal.CanonicalRevision = fmt.Sprintf("wsrev_%d", identity.StateSeq)
	if err := w.persistCommitJournal(journalPath, &journal, "committed_journal"); err != nil {
		return failure(err)
	}
	w.collectCommitJournals()
	return identity, nil
}

// recheckCommitEntry compares the canonical object with the journal preimage immediately
// before its durable replacement, mirroring the recheck startup recovery performs.
func recheckCommitEntry(absolute string, entry CommitJournalEntry) error {
	matches, err := stateMatchesSnapshot(absolute, entry.Before, entry.Preimage)
	if err != nil {
		return err
	}
	if !matches {
		return Codedf(CodeCommitPreconditionChanged, "%s changed before commit", entry.Path)
	}
	return nil
}

// CommitOption adjusts one CommitPlan call.
type CommitOption func(*commitOptions)

type commitOptions struct {
	acceptProvisional map[string]bool
}

// AcceptProvisional lets CommitPlan commit a PROVISIONAL plan whose evidence is incomplete
// in the named verification dimension (for example "diagnostics"). Every dimension the
// plan lacks must be accepted explicitly; the acceptance and the missing coverage are
// recorded on the committed plan.
func AcceptProvisional(dimension string) CommitOption {
	return func(options *commitOptions) {
		if options.acceptProvisional == nil {
			options.acceptProvisional = make(map[string]bool)
		}
		options.acceptProvisional[dimension] = true
	}
}

// commitAbortsAsConflict reports whether a pre-write commit failure describes a canonical
// tree that no longer matches the prepared plan, which moves the plan to CONFLICTED rather
// than FAILED.
func commitAbortsAsConflict(err error) bool {
	switch ErrorCode(err) {
	case CodeCommitPreconditionChanged, CodeWorkspaceEpochChanged:
		return true
	}
	return false
}

// CommitPlan applies one READY prepared revision, or a PROVISIONAL one whose missing
// evidence the caller accepted with AcceptProvisional, through a durable per-path
// write-ahead journal. The journal is fsynced before canonical mutation. A precondition
// that fails before the first canonical write leaves the workspace untouched, rolls the
// journal back and moves the plan to CONFLICTED; a failure after the first write leaves
// both journal and plan RECOVERY_REQUIRED for startup recovery. On every exit after the
// commit has been admitted, the provider lease is released and the stager is rolled back
// unless its own commit already succeeded.
func (w *Workspace) CommitPlan(ctx context.Context, planID string, expected uint64, preparedRevision string, stager PlanStager, options ...CommitOption) (PlanRecord, error) {
	if stager == nil {
		return PlanRecord{}, Coded(CodeProviderUnavailable, errors.New("commit requires a provider"))
	}
	var settings commitOptions
	for _, option := range options {
		option(&settings)
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
		return PlanRecord{}, Codedf(CodePlanStateInvalid, "plan %s is %s; only a READY plan commits", planID, plan.State)
	}
	if preparedRevision == "" || plan.Preparation.PreparedRevision != preparedRevision {
		return PlanRecord{}, Coded(CodePreparedRevisionChanged, nil)
	}
	durablePreparation := false
	if source, ok := stager.(PreparedPlanStager); ok {
		_, _, durablePreparation = source.PreparedRequest()
	}
	if plan.Preparation.ProviderEpoch != stager.Epoch() && !durablePreparation {
		return PlanRecord{}, Coded(CodeWorkspaceEpochChanged, nil)
	}
	preparation := *plan.Preparation
	if plan.State == PlanProvisional {
		gaps := verificationGaps(preparation.Verification)
		if len(gaps) == 0 {
			gaps = []VerificationGap{{Dimension: "diagnostics", Detail: "semantic coverage " + preparation.Diagnostics}}
		}
		var missing []string
		for _, gap := range gaps {
			if !settings.acceptProvisional[gap.Dimension] {
				missing = append(missing, gap.Dimension+" ("+gap.Detail+")")
			}
		}
		if len(missing) > 0 {
			return PlanRecord{}, Codedf(CodeProvisionalNotAccepted,
				"plan %s is PROVISIONAL; accept the incomplete evidence explicitly for: %s", planID, strings.Join(missing, "; "))
		}
		preparation.MissingCoverage = append([]VerificationGap(nil), gaps...)
		preparation.ProvisionalAccepted = nil
		for _, gap := range gaps {
			preparation.ProvisionalAccepted = append(preparation.ProvisionalAccepted, gap.Dimension)
		}
	}

	// The commit is admitted. From here every exit releases the provider lease, and every
	// failure rolls the stager back unless the provider view was already committed.
	stagerCommitted := false
	finished := false
	finish := func() {
		if finished {
			return
		}
		finished = true
		if !stagerCommitted {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = stager.Rollback(rollbackCtx, planID)
			cancel()
		}
		w.prepareMu.Lock()
		delete(w.activePlans, planID)
		w.prepareMu.Unlock()
	}
	defer finish()

	// refuse records a failure that happened before any canonical byte was written. A
	// precondition mismatch is a CONFLICTED plan; any other cause leaves it FAILED. Both are
	// re-preparable and neither needs recovery.
	refuse := func(cause error) (PlanRecord, error) {
		var record PlanRecord
		var transitionErr error
		if commitAbortsAsConflict(cause) {
			record, transitionErr = w.conflictPlan(planID, expected, "commit_refused", &preparation, cause)
		} else {
			record, transitionErr = w.transitionPlan(planID, expected, PlanFailed, "commit_refused", cause.Error(), &preparation)
		}
		if transitionErr != nil {
			return record, fmt.Errorf("%w; persist refusal: %v", cause, transitionErr)
		}
		return record, cause
	}

	freshPreview := w.buildPreview(plan)
	if freshPreview.Outcome != "ok" || freshPreview.PreviewRevision != plan.Preview.PreviewRevision {
		return refuse(Coded(CodeCommitPreconditionChanged, errors.New("plan preview is stale")))
	}
	request, err := w.planStageRequest(plan)
	if err != nil {
		return refuse(err)
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
		return refuse(errors.New("commit journal state directory is required"))
	}
	if err := w.persistCommitJournal(journalPath, &journal, "prepare_journal"); err != nil {
		return refuse(err)
	}
	// abort handles a failure after the journal exists but before any canonical write: the
	// journal is rolled back so startup never reprocesses it, then the plan is refused.
	abort := func(cause error) (PlanRecord, error) {
		journal.State = CommitJournalRolledBack
		journal.LastError = cause.Error()
		if err := w.writeCommitJournal(journalPath, &journal); err != nil {
			cause = fmt.Errorf("%w; persist rolled-back journal: %v", cause, err)
		}
		return refuse(cause)
	}
	if _, err := w.transitionPlan(planID, expected, PlanCommitting, "commit_started", "pending", &preparation); err != nil {
		return abort(err)
	}
	for _, entry := range journal.Entries {
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return abort(err)
		}
		if err := recheckCommitEntry(absolute, entry); err != nil {
			return abort(err)
		}
	}
	journal.State = CommitJournalApplying
	if err := w.persistCommitJournal(journalPath, &journal, "applying_journal"); err != nil {
		return abort(err)
	}
	written := false
	fail := func(cause error) (PlanRecord, error) {
		if !written {
			return abort(cause)
		}
		return w.markCommitRecovery(plan, journalPath, &journal, cause)
	}
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return fail(err)
		}
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return fail(err)
		}
		// Recheck immediately before the durable replacement so a third-party write that
		// landed after the whole-set validation is refused rather than overwritten.
		if err := recheckCommitEntry(absolute, *entry); err != nil {
			return fail(err)
		}
		written = true
		if err := applyCommitEntry(absolute, *entry); err != nil {
			return fail(err)
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return fail(err)
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(journalPath, &journal, "progress_journal"); err != nil {
			return fail(err)
		}
	}
	if err := stager.Commit(ctx, planID); err != nil {
		return fail(fmt.Errorf("provider resync: %w", err))
	}
	stagerCommitted = true
	canonicalFromRevision := fmt.Sprintf("wsrev_%d", w.Identity().StateSeq)
	identity := w.recordCanonicalCommit(journal.Entries)
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
	// The committed journal is the durable receipt and is persisted before the plan record
	// claims COMMITTED, so a crash between the two is reconciled from the journal at startup.
	journal.State = CommitJournalCommitted
	journal.CanonicalRevision = preparation.CanonicalRevision
	if err := w.persistCommitJournal(journalPath, &journal, "committed_journal"); err != nil {
		return fail(err)
	}
	result, err := w.transitionPlan(planID, expected, PlanCommitted, "commit", "ok", &preparation)
	if err != nil {
		// Every canonical byte and the committed journal are durable; only the plan record
		// lags. Startup reconciliation marks it COMMITTED from the journal.
		record, _ := w.transitionPlan(planID, expected, PlanRecoveryRequired, "commit_receipt_failed", err.Error(), &preparation)
		return record, Codedf(CodeCommitRecoveryRequired, "plan record could not be marked COMMITTED: %w", err)
	}
	w.collectCommitJournals()
	return result, nil
}
