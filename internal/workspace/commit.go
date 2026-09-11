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

// snapshotMatch selects how much of a recorded DiskSnapshot a preimage comparison trusts.
// The two callers legitimately differ: content is the revision (S20b decision 6), and the
// disk metadata is only a change detector whose reliability depends on who recorded it.
type snapshotMatch int

const (
	// matchExactSnapshot compares the whole snapshot, including device, inode and mtime.
	// CommitPlan and CommitPreparedTransaction need it: their preimages were captured by
	// this process moments earlier, so any metadata drift is a third-party write that must
	// refuse the commit.
	matchExactSnapshot snapshotMatch = iota
	// matchJournalImage compares kind, mode, size and symlink target, then the bytes.
	// Startup recovery and CompensatePlan need it: a journal read back after a crash, or a
	// committed postimage undone later, describes an object whose inode and mtime may have
	// changed through an atomic save or a checkout of identical content.
	matchJournalImage
)

func stateMatchesImage(path string, expected DiskSnapshot, content []byte, match snapshotMatch) (bool, error) {
	current, currentBytes, err := inspectPath(path)
	if err != nil {
		return false, err
	}
	if match == matchExactSnapshot {
		return equalDisk(current, expected) && bytes.Equal(currentBytes, content), nil
	}
	if current.Kind != expected.Kind || current.Mode != expected.Mode ||
		current.Size != expected.Size || current.SymlinkTarget != expected.SymlinkTarget {
		return false, nil
	}
	return bytes.Equal(currentBytes, content), nil
}

// stateMatchesSnapshot is the strict comparator; see matchExactSnapshot.
func stateMatchesSnapshot(path string, expected DiskSnapshot, content []byte) (bool, error) {
	return stateMatchesImage(path, expected, content, matchExactSnapshot)
}

// stateMatchesJournalImage is the lenient comparator; see matchJournalImage.
func stateMatchesJournalImage(path string, expected DiskSnapshot, content []byte) (bool, error) {
	return stateMatchesImage(path, expected, content, matchJournalImage)
}

// newCommitJournal returns a prepared journal whose entries mirror the staged write set.
func (w *Workspace) newCommitJournal(planID string, planRevision uint64, preparedRevision string, files []PlanStageFile) CommitJournal {
	now := time.Now().UTC()
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: w.Identity().ID, PlanID: planID,
		PlanRevision: planRevision, PreparedRevision: preparedRevision, State: CommitJournalPrepared,
		CreatedAt: now, UpdatedAt: now,
	}
	for _, file := range files {
		journal.Entries = append(journal.Entries, CommitJournalEntry{
			Path: file.Path, Preimage: append([]byte(nil), file.Before...), Postimage: append([]byte(nil), file.After...),
			Before: file.BeforeDisk, After: file.AfterDisk, Progress: CommitPathPending,
		})
	}
	return journal
}

// applyJournalEntries makes every journal entry durable in commit order. Each path is
// rechecked immediately before its replacement so a third-party write that landed after
// the whole-set validation is refused rather than overwritten, and progress is persisted
// after each write. The returned written flag reports whether any canonical write was
// attempted, which is what separates a refusal (nothing written, the journal rolls back)
// from a failure that needs recovery.
func (w *Workspace) applyJournalEntries(ctx context.Context, journalPath string, journal *CommitJournal, recheck func(absolute string, entry CommitJournalEntry) error) (written bool, err error) {
	for _, index := range commitEntryOrder(journal.Entries) {
		entry := &journal.Entries[index]
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if err := w.fireCommitFault("before_apply", entry.Path); err != nil {
			return written, err
		}
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return written, err
		}
		if err := recheck(absolute, *entry); err != nil {
			return written, err
		}
		written = true
		if err := applyCommitEntry(absolute, *entry); err != nil {
			return written, err
		}
		if err := w.fireCommitFault("after_apply", entry.Path); err != nil {
			return written, err
		}
		entry.Progress = CommitPathApplied
		if err := w.persistCommitJournal(journalPath, journal, "progress_journal"); err != nil {
			return written, err
		}
	}
	return written, nil
}

// sealCommittedJournal advances the canonical revision for the applied write set and
// persists the committed journal, the durable receipt every caller records before it
// claims success.
func (w *Workspace) sealCommittedJournal(journalPath string, journal *CommitJournal) (Identity, error) {
	identity := w.recordCanonicalCommit(journal.Entries)
	journal.State = CommitJournalCommitted
	journal.CanonicalRevision = fmt.Sprintf("wsrev_%d", identity.StateSeq)
	return identity, w.persistCommitJournal(journalPath, journal, "committed_journal")
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
	return writeTempAndRename(path, content, mode&commitModeMask, replace)
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
	if err := w.validatePreparedWrites(files); err != nil {
		return Identity{}, err
	}
	journal := w.newCommitJournal(transactionID, 1, "legacy:"+operationID, files)
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
	var err error
	if written, err = w.applyJournalEntries(ctx, journalPath, &journal, recheckCommitEntry); err != nil {
		return failure(err)
	}
	if finalize != nil {
		if err := finalize(ctx); err != nil {
			return failure(err)
		}
	}
	identity, err := w.sealCommittedJournal(journalPath, &journal)
	if err != nil {
		return failure(err)
	}
	w.collectCommitJournals()
	return identity, nil
}

// validatePreparedWrites refuses a legacy write set with an empty or duplicate path, a path
// outside the workspace, or a preimage the canonical tree no longer matches.
func (w *Workspace) validatePreparedWrites(files []PlanStageFile) error {
	seen := map[string]bool{}
	for _, file := range files {
		if file.Path == "" || seen[file.Path] {
			return fmt.Errorf("invalid prepared write path %q", file.Path)
		}
		seen[file.Path] = true
		absolute, err := w.confinedPath(file.Path)
		if err != nil {
			return err
		}
		matches, err := stateMatchesSnapshot(absolute, file.BeforeDisk, file.Before)
		if err != nil {
			return err
		}
		if !matches {
			return Codedf(CodeCommitPreconditionChanged, "%s changed before commit", file.Path)
		}
	}
	return nil
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
	run, err := w.admitCommit(ctx, plan, expected, preparedRevision, stager, settings)
	if err != nil {
		return PlanRecord{}, err
	}
	// The commit is admitted. From here every exit releases the provider lease, and every
	// failure rolls the stager back unless the provider view was already committed.
	defer run.finish()
	if err := run.stageWriteSet(); err != nil {
		return run.refuse(err)
	}
	if err := run.revalidate(); err != nil {
		return run.abort(err)
	}
	run.written, err = w.applyJournalEntries(ctx, run.journalPath, &run.journal, recheckCommitEntry)
	if err != nil {
		return run.fail(err)
	}
	return run.complete()
}

// commitRun carries one admitted CommitPlan call through its phases: stage the write set,
// revalidate, apply and complete. The identity fields are fixed at admission; journal,
// written and stagerCommitted record progress for the failure paths and for finish.
type commitRun struct {
	w               *Workspace
	ctx             context.Context
	stager          PlanStager
	plan            PlanRecord
	expected        uint64
	preparation     PlanPreparation
	preparedRev     string
	request         PlanStageRequest
	journalPath     string
	journal         CommitJournal
	written         bool
	stagerCommitted bool
	finished        bool
}

// admitCommit checks the plan, prepared revision, provider epoch and provisional
// acceptance. It succeeds only for a plan this call may commit; nothing is written.
func (w *Workspace) admitCommit(ctx context.Context, plan PlanRecord, expected uint64, preparedRevision string, stager PlanStager, settings commitOptions) (*commitRun, error) {
	if (plan.State != PlanReady && plan.State != PlanProvisional) || plan.Preparation == nil {
		return nil, Codedf(CodePlanStateInvalid, "plan %s is %s; only a READY plan commits", plan.PlanID, plan.State)
	}
	if preparedRevision == "" || plan.Preparation.PreparedRevision != preparedRevision {
		return nil, Coded(CodePreparedRevisionChanged, nil)
	}
	durablePreparation := false
	if source, ok := stager.(PreparedPlanStager); ok {
		_, _, durablePreparation = source.PreparedRequest()
	}
	if plan.Preparation.ProviderEpoch != stager.Epoch() && !durablePreparation {
		return nil, Coded(CodeWorkspaceEpochChanged, nil)
	}
	preparation := *plan.Preparation
	if plan.State == PlanProvisional {
		if err := acceptProvisionalGaps(plan.PlanID, &preparation, settings); err != nil {
			return nil, err
		}
	}
	return &commitRun{w: w, ctx: ctx, stager: stager, plan: plan, expected: expected, preparation: preparation, preparedRev: preparedRevision}, nil
}

// acceptProvisionalGaps requires every missing verification dimension to be accepted
// explicitly and records both the gaps and the acceptance on the preparation.
func acceptProvisionalGaps(planID string, preparation *PlanPreparation, settings commitOptions) error {
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
		return Codedf(CodeProvisionalNotAccepted,
			"plan %s is PROVISIONAL; accept the incomplete evidence explicitly for: %s", planID, strings.Join(missing, "; "))
	}
	preparation.MissingCoverage = append([]VerificationGap(nil), gaps...)
	preparation.ProvisionalAccepted = nil
	for _, gap := range gaps {
		preparation.ProvisionalAccepted = append(preparation.ProvisionalAccepted, gap.Dimension)
	}
	return nil
}

// finish releases the provider lease and rolls the stager back unless the provider view
// was already committed. It runs once, on every exit after admission.
func (run *commitRun) finish() {
	if run.finished {
		return
	}
	run.finished = true
	if !run.stagerCommitted {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = run.stager.Rollback(rollbackCtx, run.plan.PlanID)
		cancel()
	}
	run.w.prepareMu.Lock()
	delete(run.w.activePlans, run.plan.PlanID)
	run.w.prepareMu.Unlock()
}

// stageWriteSet confirms the preview is still current, reconstructs the exact write set
// (preferring the durable prepared request when the stager has one) and persists the
// prepared journal. A failure here happened before any canonical write.
func (run *commitRun) stageWriteSet() error {
	w, plan := run.w, run.plan
	freshPreview := w.buildPreview(plan)
	if freshPreview.Outcome != "ok" || freshPreview.PreviewRevision != plan.Preview.PreviewRevision {
		return Coded(CodeCommitPreconditionChanged, errors.New("plan preview is stale"))
	}
	request, err := w.planStageRequest(plan)
	if err != nil {
		return err
	}
	if source, ok := run.stager.(PreparedPlanStager); ok {
		if prepared, _, available := source.PreparedRequest(); available {
			request = prepared
		}
	}
	run.request = request
	run.journal = w.newCommitJournal(plan.PlanID, run.expected, run.preparedRev, request.Files)
	run.journalPath = w.commitJournalPath(plan.PlanID)
	if run.journalPath == "" {
		return errors.New("commit journal state directory is required")
	}
	return w.persistCommitJournal(run.journalPath, &run.journal, "prepare_journal")
}

// revalidate moves the plan to COMMITTING, rechecks every preimage against the canonical
// tree and marks the journal applying. The journal exists but nothing canonical is written.
func (run *commitRun) revalidate() error {
	w, plan := run.w, run.plan
	if _, err := w.transitionPlan(plan.PlanID, run.expected, PlanCommitting, "commit_started", "pending", &run.preparation); err != nil {
		return err
	}
	for _, entry := range run.journal.Entries {
		absolute, err := w.confinedPath(entry.Path)
		if err != nil {
			return err
		}
		if err := recheckCommitEntry(absolute, entry); err != nil {
			return err
		}
	}
	run.journal.State = CommitJournalApplying
	return w.persistCommitJournal(run.journalPath, &run.journal, "applying_journal")
}

// complete resyncs the provider, seals the committed journal and records the COMMITTED
// plan. The committed journal is the durable receipt and is persisted before the plan
// record claims COMMITTED, so a crash between the two is reconciled at startup.
func (run *commitRun) complete() (PlanRecord, error) {
	w, plan := run.w, run.plan
	if err := run.stager.Commit(run.ctx, plan.PlanID); err != nil {
		return run.fail(fmt.Errorf("provider resync: %w", err))
	}
	run.stagerCommitted = true
	run.preparation.CanonicalFromRevision = fmt.Sprintf("wsrev_%d", w.Identity().StateSeq)
	run.preparation.CommittedDiffs = committedDiffs(run.request.Files)
	if _, err := w.sealCommittedJournal(run.journalPath, &run.journal); err != nil {
		return run.fail(err)
	}
	run.preparation.CanonicalChanged = true
	run.preparation.CanonicalRevision = run.journal.CanonicalRevision
	run.preparation.JournalID = plan.PlanID
	result, err := w.transitionPlan(plan.PlanID, run.expected, PlanCommitted, "commit", "ok", &run.preparation)
	if err != nil {
		// Every canonical byte and the committed journal are durable; only the plan record
		// lags. Startup reconciliation marks it COMMITTED from the journal.
		record, _ := w.transitionPlan(plan.PlanID, run.expected, PlanRecoveryRequired, "commit_receipt_failed", err.Error(), &run.preparation)
		return record, Codedf(CodeCommitRecoveryRequired, "plan record could not be marked COMMITTED: %w", err)
	}
	w.collectCommitJournals()
	return result, nil
}

// committedDiffs records the exact byte change of every committed file.
func committedDiffs(files []PlanStageFile) []ExactDiff {
	diffs := make([]ExactDiff, 0, len(files))
	for _, file := range files {
		before, after := file.Before, file.After
		if !file.BeforeExists {
			before = nil
		}
		if !file.AfterExists {
			after = nil
		}
		diffs = append(diffs, exactDiff(file.Path, before, after, 0, len(before), after))
	}
	return diffs
}

// refuse records a failure that happened before any canonical byte was written. A
// precondition mismatch is a CONFLICTED plan; any other cause leaves it FAILED. Both are
// re-preparable and neither needs recovery.
func (run *commitRun) refuse(cause error) (PlanRecord, error) {
	w, plan := run.w, run.plan
	var record PlanRecord
	var transitionErr error
	if commitAbortsAsConflict(cause) {
		record, transitionErr = w.conflictPlan(plan.PlanID, run.expected, "commit_refused", &run.preparation, cause)
	} else {
		record, transitionErr = w.transitionPlan(plan.PlanID, run.expected, PlanFailed, "commit_refused", cause.Error(), &run.preparation)
	}
	if transitionErr != nil {
		return record, fmt.Errorf("%w; persist refusal: %v", cause, transitionErr)
	}
	return record, cause
}

// abort handles a failure after the journal exists but before any canonical write: the
// journal is rolled back so startup never reprocesses it, then the plan is refused.
func (run *commitRun) abort(cause error) (PlanRecord, error) {
	run.journal.State = CommitJournalRolledBack
	run.journal.LastError = cause.Error()
	if err := run.w.writeCommitJournal(run.journalPath, &run.journal); err != nil {
		cause = fmt.Errorf("%w; persist rolled-back journal: %v", cause, err)
	}
	return run.refuse(cause)
}

// fail routes a failure by whether a canonical write was attempted: before the first
// write it is an abort, after it the journal and plan need recovery.
func (run *commitRun) fail(cause error) (PlanRecord, error) {
	if !run.written {
		return run.abort(cause)
	}
	return run.w.markCommitRecovery(run.plan, run.journalPath, &run.journal, cause)
}
