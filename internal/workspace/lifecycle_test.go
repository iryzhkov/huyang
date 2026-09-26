package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stagerBuffersFor seeds a fake provider with the exact preimages a previewed plan expects.
func stagerBuffersFor(ws *Workspace, plan PlanRecord) *fakePlanStager {
	stager := &fakePlanStager{epoch: ws.Identity().Epoch, buffers: make(map[string][]byte)}
	for _, diff := range plan.Preview.Diffs {
		if diff.Before != nil {
			stager.buffers[diff.Path] = append([]byte(nil), diff.Before...)
		}
	}
	return stager
}

func reopenCommitWorkspace(t *testing.T, ws *Workspace, root, stateDir string) *Workspace {
	t.Helper()
	reopened, err := Open(OpenOptions{
		Kind: KindProject, Root: root, ProviderEpoch: ws.Identity().Epoch, StateDir: stateDir,
		Identity: ws.Identity().ID, StateSeq: ws.Identity().StateSeq,
	})
	if err != nil {
		t.Fatalf("reopen root: %v", err)
	}
	return reopened
}

func TestCommitPreconditionFailureOnToolDeltaFileConflictsWithoutPoisoningRoot(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	note := filepath.Join(root, "note.txt")
	generated := filepath.Join(root, "generated.txt")
	if err := os.WriteFile(note, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	plan, err := ws.CreatePlan([]PlanOperation{fullRangeOperation(t, ws, "replace", note, "after\n")})
	if err != nil {
		t.Fatal(err)
	}
	previewed, err := ws.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	request, err := ws.planStageRequest(previewed)
	if err != nil {
		t.Fatal(err)
	}
	// The sandbox pipeline declared an extra generated file the preview never saw.
	request.Files = append(request.Files, PlanStageFile{
		Path: "generated.txt", After: []byte("generated\n"), AfterExists: true,
		BeforeDisk: DiskSnapshot{Kind: ObjectMissing},
		AfterDisk:  DiskSnapshot{Kind: ObjectRegularText, Size: int64(len("generated\n")), Mode: 0o644},
	})
	provider := stagerBuffersFor(ws, previewed)
	stager := &durablePreparedStager{fakePlanStager: provider, request: request}
	prepared, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generated, []byte("third-party\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	record, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if ErrorCode(err) != CodeCommitPreconditionChanged {
		t.Fatalf("commit error = %v, want %s", err, CodeCommitPreconditionChanged)
	}
	if record.State != PlanConflicted || record.Conflict == nil || record.Conflict.Code != CodeCommitPreconditionChanged {
		t.Fatalf("conflicted plan = %#v", record)
	}
	for path, want := range map[string]string{note: "before\n", generated: "third-party\n"} {
		if got, readErr := os.ReadFile(path); readErr != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, readErr, want)
		}
	}
	journal, err := ws.loadCommitJournal(prepared.PlanID)
	if err != nil || journal.State != CommitJournalRolledBack || journal.LastError == "" {
		t.Fatalf("refused commit journal = %#v, %v", journal, err)
	}
	if ws.holdsLease(prepared.PlanID) || provider.rollbacks != 1 || provider.commits != 0 {
		t.Fatalf("refusal leaked the preparation: lease=%t rollbacks=%d commits=%d", ws.holdsLease(prepared.PlanID), provider.rollbacks, provider.commits)
	}
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	restored, err := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || restored.State != PlanConflicted {
		t.Fatalf("restored plan = %#v, %v", restored, err)
	}
	if got, readErr := os.ReadFile(generated); readErr != nil || string(got) != "third-party\n" {
		t.Fatalf("startup touched the third-party file: %q, %v", got, readErr)
	}
	if _, err := reopened.DiscardPlan(prepared.PlanID, prepared.PlanRevision); err != nil {
		t.Fatalf("conflicted plan could not be discarded: %v", err)
	}
}

func TestCommitFailureAfterWriteReleasesLeaseAndRollsBackStager(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "a", path, "new\n")})
	ws.commitFault = func(point, _ string) error {
		if point == "after_apply" {
			return errors.New("injected write failure")
		}
		return nil
	}
	record, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if err == nil || record.State != PlanRecoveryRequired {
		t.Fatalf("commit failure = %#v, %v", record, err)
	}
	if ws.holdsLease(prepared.PlanID) {
		t.Fatal("failed commit retained the provider lease")
	}
	if stager.rollbacks != 1 || stager.commits != 0 {
		t.Fatalf("stager was not rolled back exactly once: rollbacks=%d commits=%d", stager.rollbacks, stager.commits)
	}
	if _, err := ws.DiscardPlan(prepared.PlanID, prepared.PlanRevision); ErrorCode(err) != CodePlanStateInvalid {
		t.Fatalf("RECOVERY_REQUIRED plan was discardable: %v", err)
	}
	current, err := ws.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || current.State != PlanRecoveryRequired {
		t.Fatalf("plan after refused discard = %#v, %v", current, err)
	}
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	restored, err := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || restored.State != PlanRolledBack {
		t.Fatalf("recovered plan = %#v, %v", restored, err)
	}
}

func TestDiscardAndTransitionRefuseIllegalEdges(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "a", path, "new\n")})
	if _, err := ws.DiscardPlan(prepared.PlanID, prepared.PlanRevision); ErrorCode(err) != CodePlanStateInvalid {
		t.Fatalf("READY plan bypassed rollback through discard: %v", err)
	}
	if _, err := ws.EditPlan(prepared.PlanID, prepared.PlanRevision, PlanEdit{Mode: "remove", OpIDs: []string{"a"}}); ErrorCode(err) != CodePlanStateInvalid {
		t.Fatalf("READY plan was editable while holding the lease: %v", err)
	}
	committed, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if err != nil || committed.State != PlanCommitted {
		t.Fatalf("commit = %#v, %v", committed, err)
	}
	if _, err := ws.DiscardPlan(prepared.PlanID, prepared.PlanRevision); ErrorCode(err) != CodePlanStateInvalid {
		t.Fatalf("COMMITTED plan was discardable: %v", err)
	}
	current, err := ws.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || current.State != PlanCommitted {
		t.Fatalf("plan after refused discard = %#v, %v", current, err)
	}
	if _, err := ws.transitionPlan(prepared.PlanID, prepared.PlanRevision, PlanOpen, "test", "illegal", nil); ErrorCode(err) != CodePlanStateInvalid {
		t.Fatalf("terminal plan accepted a transition: %v", err)
	}
	// The committed receipt survived the refused discard, so compensation still works.
	undo, err := ws.CompensatePlan(context.Background(), prepared.PlanID, stager)
	if err != nil || undo.State != CommitJournalCommitted {
		t.Fatalf("compensation after refused discard = %#v, %v", undo, err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "old\n" {
		t.Fatalf("compensated bytes = %q, %v", got, readErr)
	}
	for from, to := range map[PlanState]PlanState{
		PlanOpen: PlanCommitted, PlanReady: PlanDiscarded, PlanCommitted: PlanRolledBack,
		PlanRecoveryRequired: PlanDiscarded, PlanDiscarded: PlanOpen, PlanExpired: PlanCommitted,
		PlanCommitting: PlanExpired, PlanPreparing: PlanExpired,
	} {
		if canTransition(from, to) {
			t.Fatalf("state machine allows %s -> %s", from, to)
		}
	}
	for from, to := range map[PlanState]PlanState{
		PlanCommitting: PlanConflicted, PlanConflicted: PlanOpen, PlanFailed: PlanPreparing,
		PlanRecoveryRequired: PlanRolledBack, PlanPreviewed: PlanExpired,
		PlanReady: PlanExpired, PlanExpired: PlanPreparing,
	} {
		if !canTransition(from, to) {
			t.Fatalf("state machine refuses %s -> %s", from, to)
		}
	}
}

func TestEditAfterFailedPrepareReturnsToOpenAndDropsEvidence(t *testing.T) {
	ws, _, plan, stager := prepareFixture(t)
	stager.failAfter = 1
	if _, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err == nil {
		t.Fatal("prepare survived provider failure")
	}
	failed, err := ws.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil || failed.State != PlanFailed || failed.Preview == nil {
		t.Fatalf("failed plan = %#v, %v", failed, err)
	}
	edited, err := ws.EditPlan(plan.PlanID, plan.PlanRevision, PlanEdit{Mode: "remove", OpIDs: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if edited.State != PlanOpen || edited.Preview != nil || edited.Preparation != nil || edited.Conflict != nil ||
		edited.PlanRevision != plan.PlanRevision+1 || len(edited.Operations) != 1 {
		t.Fatalf("edited plan kept stale evidence: %#v", edited)
	}
	stager.failAfter = 0
	prepared, err := ws.PreparePlan(context.Background(), edited.PlanID, edited.PlanRevision, stager)
	if err != nil || prepared.State != PlanReady {
		t.Fatalf("re-prepare after edit = %#v, %v", prepared, err)
	}
}

func TestInspectRecordsNoEventAndEventsAreCapped(t *testing.T) {
	ws, _, plan, _ := prepareFixture(t)
	before, err := ws.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	statePath := ws.planRecordPath(plan.PlanID)
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := ws.InspectPlan(plan.PlanID, plan.PlanRevision); err != nil {
			t.Fatal(err)
		}
	}
	after, err := ws.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Events) != len(before.Events) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("inspect mutated the plan: before %d events, after %d", len(before.Events), len(after.Events))
	}
	if rewritten, statErr := os.Stat(statePath); statErr != nil || rewritten.ModTime() != info.ModTime() || rewritten.Size() != info.Size() {
		t.Fatalf("inspect rewrote the durable plan file: %v", statErr)
	}
	const previews = 90
	for i := 0; i < previews; i++ {
		if _, err := ws.PreviewPlan(plan.PlanID, plan.PlanRevision); err != nil {
			t.Fatal(err)
		}
	}
	capped, err := ws.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Events) != planEventsHead+planEventsTail {
		t.Fatalf("events = %d, want %d", len(capped.Events), planEventsHead+planEventsTail)
	}
	if want := len(before.Events) + previews - (planEventsHead + planEventsTail); capped.DroppedEvents != want {
		t.Fatalf("dropped events = %d, want %d", capped.DroppedEvents, want)
	}
	if capped.Events[0].Action != "create" || capped.Events[len(capped.Events)-1].Action != "preview" {
		t.Fatalf("retention lost the head or tail: first %s, last %s", capped.Events[0].Action, capped.Events[len(capped.Events)-1].Action)
	}
}

func writeAgedJournal(t *testing.T, ws *Workspace, planID string, state CommitJournalState, age time.Duration) string {
	t.Helper()
	at := time.Now().UTC().Add(-age)
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: ws.Identity().ID, PlanID: planID, PlanRevision: 1,
		PreparedRevision: "prep_" + planID, State: state, CreatedAt: at, UpdatedAt: at,
	}
	content, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	path := ws.commitJournalPath(planID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommitJournalGarbageCollectionIsBoundedAndSkipsIncompleteJournals(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	ws := openCommitWorkspace(t, root, stateDir)
	stale := commitJournalRetention + time.Hour
	oldCommitted := writeAgedJournal(t, ws, "old_committed", CommitJournalCommitted, stale)
	oldRolledBack := writeAgedJournal(t, ws, "old_rolled_back", CommitJournalRolledBack, stale)
	oldPrepared := writeAgedJournal(t, ws, "old_prepared", CommitJournalPrepared, stale)
	oldRecovery := writeAgedJournal(t, ws, "old_recovery", CommitJournalRecoveryRequired, stale)
	fresh := make([]string, 0, commitJournalRetainCount+3)
	for i := 0; i < commitJournalRetainCount+3; i++ {
		fresh = append(fresh, writeAgedJournal(t, ws, fmt.Sprintf("fresh_%03d", i), CommitJournalCommitted, time.Duration(i)*time.Minute))
	}
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	for _, path := range []string{oldCommitted, oldRolledBack} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale completed journal survived: %s (%v)", path, err)
		}
	}
	prepared, err := loadCommitJournalFile(oldPrepared)
	if err != nil || prepared.State != CommitJournalRolledBack {
		t.Fatalf("prepared journal = %#v, %v; want closed as rolled back and retained", prepared, err)
	}
	recovery, err := loadCommitJournalFile(oldRecovery)
	if err != nil || recovery.State != CommitJournalRolledBack {
		t.Fatalf("recovery-required journal with no entries = %#v, %v", recovery, err)
	}
	// The two journals startup just closed are now the newest completed ones, so the cap
	// keeps them plus the newest fresh journals; the five oldest beyond it are collected.
	kept := commitJournalRetainCount - 2
	for i, path := range fresh {
		_, statErr := os.Stat(path)
		if i < kept && statErr != nil {
			t.Fatalf("fresh journal %d was collected: %v", i, statErr)
		}
		if i >= kept && !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("journal %d beyond the cap survived: %v", i, statErr)
		}
	}
	if reopened.Identity().ID != ws.Identity().ID {
		t.Fatalf("reopened identity = %s", reopened.Identity().ID)
	}
}

func TestCommitRefusesToClobberThirdPartyCreatedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new.txt")
	if err := os.WriteFile(path, []byte("third-party\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := CommitJournalEntry{
		Path: path, Postimage: []byte("mine\n"),
		Before: DiskSnapshot{Kind: ObjectMissing},
		After:  DiskSnapshot{Kind: ObjectRegularText, Size: 5, Mode: 0o644},
	}
	if err := applyCommitEntry(path, entry); ErrorCode(err) != CodeCommitPreconditionChanged {
		t.Fatalf("create over an existing file = %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte("third-party\n")) {
		t.Fatalf("third-party file was clobbered: %q, %v", got, err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink("elsewhere", link); err != nil {
		t.Fatal(err)
	}
	linkEntry := CommitJournalEntry{
		Path: link, Postimage: []byte("mine"),
		Before: DiskSnapshot{Kind: ObjectMissing},
		After:  DiskSnapshot{Kind: ObjectSymlink, SymlinkTarget: "mine"},
	}
	if err := applyCommitEntry(link, linkEntry); ErrorCode(err) != CodeCommitPreconditionChanged {
		t.Fatalf("symlink create over an existing link = %v", err)
	}
	if target, err := os.Readlink(link); err != nil || target != "elsewhere" {
		t.Fatalf("third-party symlink was clobbered: %q, %v", target, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("refused creates left temporary files behind: %d entries, %v", len(entries), err)
	}
}

func TestCommitPreservesSetuidModeSoRecoveryAcceptsIt(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "tool.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755|os.ModeSetuid); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSetuid == 0 {
		t.Skipf("filesystem does not keep setuid on regular files: %v, %v", info, err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "tool", path, "#!/bin/sh\necho new\n")})
	ws.commitFault = func(point, _ string) error {
		if point == "after_apply" {
			return errors.New("injected failure after the setuid write")
		}
		return nil
	}
	if record, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager); err == nil || record.State != PlanRecoveryRequired {
		t.Fatalf("commit = %#v, %v", record, err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSetuid == 0 || info.Mode().Perm() != 0o755 {
		t.Fatalf("commit dropped the setuid bit: %v, %v", info.Mode(), err)
	}
	reopenCommitWorkspace(t, ws, root, stateDir)
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "#!/bin/sh\necho old\n" {
		t.Fatalf("recovered bytes = %q, %v", got, err)
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSetuid == 0 {
		t.Fatalf("recovery dropped the setuid bit: %v, %v", info.Mode(), err)
	}
}

func TestCommittedJournalReconcilesLaggingPlanRecordAtStartup(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "a", path, "new\n")})
	committed, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process that died after the committed journal but before the plan record
	// recorded COMMITTED.
	content, err := os.ReadFile(ws.planRecordPath(prepared.PlanID))
	if err != nil {
		t.Fatal(err)
	}
	var record persistedPlan
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	record.Plan.State = PlanCommitting
	record.Plan.Preparation.CanonicalRevision = ""
	record.Plan.Preparation.JournalID = ""
	content, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.planRecordPath(prepared.PlanID), content, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	restored, err := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || restored.State != PlanCommitted || restored.Preparation == nil ||
		restored.Preparation.JournalID != prepared.PlanID || restored.Preparation.CanonicalRevision != committed.Preparation.CanonicalRevision {
		t.Fatalf("reconciled plan = %#v, %v", restored, err)
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "new\n" {
		t.Fatalf("reconciliation rolled a committed write back: %q, %v", got, readErr)
	}
}

func TestCommitKeepsPrimedInventoryInSync(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	if err := ws.PrimeDocuments(); err != nil {
		t.Fatal(err)
	}
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{
		{OpID: "create", Kind: OperationCreateFile, Path: "created.txt", Content: "created\n"},
		{OpID: "delete", Kind: OperationDeleteFile, Path: "a.txt"},
	})
	if _, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager); err != nil {
		t.Fatal(err)
	}
	sequence := ws.Identity().StateSeq
	if err := ws.PrimeDocuments(); err != nil {
		t.Fatal(err)
	}
	if ws.Identity().StateSeq != sequence {
		t.Fatalf("priming after a Huyang commit advanced the state from %d to %d", sequence, ws.Identity().StateSeq)
	}
}
