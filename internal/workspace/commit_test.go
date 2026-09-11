package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func openCommitWorkspace(t *testing.T, root, stateDir string) *Workspace {
	t.Helper()
	ws, err := Open(OpenOptions{Kind: KindProject, Root: root, ProviderEpoch: 1, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func revisionAt(t *testing.T, ws *Workspace, path string) RevisionID {
	t.Helper()
	snapshot, err := ws.Refresh(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Revision
}

func fullRangeOperation(t *testing.T, ws *Workspace, opID, path, replacement string) PlanOperation {
	t.Helper()
	read, err := ws.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := rangeHandle(read, 0, len(read.Content), 32)
	if err != nil {
		t.Fatal(err)
	}
	return PlanOperation{
		OpID: opID, Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &handle}, Content: replacement,
	}
}

func prepareCommitPlan(t *testing.T, ws *Workspace, operations []PlanOperation) (PlanRecord, *fakePlanStager) {
	t.Helper()
	plan, err := ws.CreatePlan(operations)
	if err != nil {
		t.Fatal(err)
	}
	previewed, err := ws.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	stager := &fakePlanStager{epoch: ws.Identity().Epoch, buffers: make(map[string][]byte)}
	for _, diff := range previewed.Preview.Diffs {
		if diff.Before != nil {
			stager.buffers[diff.Path] = append([]byte(nil), diff.Before...)
		}
	}
	prepared, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, stager
}

type durablePreparedStager struct {
	*fakePlanStager
	request PlanStageRequest
}

func (s *durablePreparedStager) PreparedRequest() (PlanStageRequest, VerificationResult, bool) {
	return s.request, VerificationResult{}, true
}

func TestJournaledCommitAcceptsDurablePreparationAcrossProviderEpochChange(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, provider := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "replace", "note.txt", "after\n")})
	request, err := ws.planStageRequest(prepared)
	if err != nil {
		t.Fatal(err)
	}
	provider.epoch++
	stager := &durablePreparedStager{fakePlanStager: provider, request: request}
	committed, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != PlanCommitted {
		t.Fatalf("committed state = %s", committed.State)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "after\n" {
		t.Fatalf("canonical bytes = %q, %v", got, err)
	}
}

func TestJournaledCommitCreatesReplacesMovesDeletesModesAndSymlinks(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	script := filepath.Join(root, "script.sh")
	moveSource := filepath.Join(root, "move.txt")
	deletePath := filepath.Join(root, "delete.txt")
	linkSource := filepath.Join(root, "shortcut")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moveSource, []byte("move me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deletePath, []byte("delete me\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("move.txt", linkSource); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	createPath := filepath.Join(root, "created.txt")
	moveDestination := filepath.Join(root, "moved.txt")
	linkDestination := filepath.Join(root, "shortcut-moved")
	operations := []PlanOperation{
		fullRangeOperation(t, ws, "replace-script", script, "#!/bin/sh\necho new\n"),
		{OpID: "create", Kind: OperationCreateFile, Path: createPath, Revision: revisionAt(t, ws, createPath), Content: "created\n"},
		{OpID: "move", Kind: OperationMoveFile, From: moveSource, To: moveDestination, Revision: revisionAt(t, ws, moveSource), DestinationRevision: revisionAt(t, ws, moveDestination)},
		{OpID: "delete", Kind: OperationDeleteFile, Path: deletePath, Revision: revisionAt(t, ws, deletePath)},
		{OpID: "move-link", Kind: OperationMoveFile, From: linkSource, To: linkDestination, Revision: revisionAt(t, ws, linkSource), DestinationRevision: revisionAt(t, ws, linkDestination)},
	}
	prepared, stager := prepareCommitPlan(t, ws, operations)
	startSeq := ws.Identity().StateSeq
	committed, err := ws.CommitPlan(
		context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager,
	)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != PlanCommitted || committed.Preparation == nil || !committed.Preparation.CanonicalChanged {
		t.Fatalf("commit = %#v", committed)
	}
	if committed.Preparation.CanonicalRevision == "" ||
		committed.Preparation.CanonicalFromRevision != fmt.Sprintf("wsrev_%d", startSeq) ||
		committed.Preparation.JournalID != committed.PlanID {
		t.Fatalf("commit metadata = %#v", committed.Preparation)
	}
	if len(committed.Preparation.CommittedDiffs) != 7 {
		t.Fatalf("committed diffs = %d, want 7: %#v", len(committed.Preparation.CommittedDiffs), committed.Preparation.CommittedDiffs)
	}
	foundScript := false
	for _, diff := range committed.Preparation.CommittedDiffs {
		if diff.Path != "script.sh" {
			continue
		}
		foundScript = true
		if !bytes.Equal(diff.Before, []byte("#!/bin/sh\necho old\n")) ||
			!bytes.Equal(diff.After, []byte("#!/bin/sh\necho new\n")) || diff.Patch == "" {
			t.Fatalf("script committed diff = %#v", diff)
		}
	}
	if !foundScript {
		t.Fatal("script committed diff was not recorded")
	}
	if ws.Identity().StateSeq != startSeq+1 || stager.commits != 1 {
		t.Fatalf("state/provider resync = seq %d commits %d", ws.Identity().StateSeq, stager.commits)
	}
	assertFile := func(path, want string, mode os.FileMode) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", path, got, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, %v", path, info.Mode().Perm(), err)
		}
	}
	assertFile(script, "#!/bin/sh\necho new\n", 0o755)
	assertFile(createPath, "created\n", 0o644)
	assertFile(moveDestination, "move me\n", 0o600)
	if _, err := os.Lstat(moveSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move source remains: %v", err)
	}
	if _, err := os.Lstat(deletePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete target remains: %v", err)
	}
	target, err := os.Readlink(linkDestination)
	if err != nil || target != "move.txt" {
		t.Fatalf("moved symlink = %q, %v", target, err)
	}
	if _, err := os.Lstat(linkSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink source remains: %v", err)
	}
	journal, err := ws.loadCommitJournal(committed.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.State != CommitJournalCommitted || len(journal.Entries) != 7 {
		t.Fatalf("journal = %#v", journal)
	}
	for _, entry := range journal.Entries {
		if entry.Progress != CommitPathApplied {
			t.Fatalf("entry did not record progress: %#v", entry)
		}
	}
}

func TestJournaledCommitFailureLeavesExactRecoverableRecord(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	if err := os.WriteFile(a, []byte("old-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("old-b\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{
		fullRangeOperation(t, ws, "a", a, "new-a\n"),
		fullRangeOperation(t, ws, "b", b, "new-b\n"),
	})
	fired := false
	ws.commitFault = func(point, path string) error {
		if point == "after_apply" && !fired {
			fired = true
			return errors.New("injected write failure")
		}
		return nil
	}
	record, err := ws.CommitPlan(
		context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager,
	)
	if err == nil || record.State != PlanRecoveryRequired {
		t.Fatalf("commit failure = %#v, %v", record, err)
	}
	journal, loadErr := ws.loadCommitJournal(prepared.PlanID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if journal.State != CommitJournalRecoveryRequired || journal.LastError == "" || len(journal.Entries) != 2 {
		t.Fatalf("recovery journal = %#v", journal)
	}
	for _, entry := range journal.Entries {
		if len(entry.Preimage) == 0 || len(entry.Postimage) == 0 || entry.Before.Kind != ObjectRegularText || entry.After.Kind != ObjectRegularText {
			t.Fatalf("journal lost exact recovery data: %#v", entry)
		}
	}
	if got, readErr := os.ReadFile(a); readErr != nil || !bytes.Equal(got, []byte("new-a\n")) {
		t.Fatalf("first durable write = %q, %v", got, readErr)
	}
	if got, readErr := os.ReadFile(b); readErr != nil || !bytes.Equal(got, []byte("old-b\n")) {
		t.Fatalf("unreached path changed = %q, %v", got, readErr)
	}
	info, statErr := os.Stat(ws.commitJournalPath(prepared.PlanID))
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, %v", info.Mode().Perm(), statErr)
	}
	reopened, openErr := Open(OpenOptions{
		Kind: KindProject, Root: root, ProviderEpoch: ws.Identity().Epoch, StateDir: stateDir,
		Identity: ws.Identity().ID, StateSeq: ws.Identity().StateSeq,
	})
	if openErr != nil {
		t.Fatal(openErr)
	}
	restored, inspectErr := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if inspectErr != nil || restored.State != PlanRolledBack {
		t.Fatalf("durable recovery state = %#v, %v", restored, inspectErr)
	}
	for path, want := range map[string]string{a: "old-a\n", b: "old-b\n"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("recovered %s = %q, %v", path, got, readErr)
		}
	}
}

func TestJournaledCommitRevalidatesEntireWriteSetBeforeMutation(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	a, b := filepath.Join(root, "a.txt"), filepath.Join(root, "b.txt")
	if err := os.WriteFile(a, []byte("old-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("old-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{
		fullRangeOperation(t, ws, "a", a, "new-a\n"),
		fullRangeOperation(t, ws, "b", b, "new-b\n"),
	})
	if err := os.WriteFile(b, []byte("third-party\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CommitPlan(
		context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager,
	); err == nil {
		t.Fatal("stale write set committed")
	}
	if got, err := os.ReadFile(a); err != nil || !bytes.Equal(got, []byte("old-a\n")) {
		t.Fatalf("unrelated path changed before full validation: %q, %v", got, err)
	}
	if _, err := os.Stat(ws.commitJournalPath(prepared.PlanID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal created before stale preview refusal: %v", err)
	}
}

func TestCommitPreparedTransactionJournalsSingleLegacyOperation(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "legacy.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	snapshot, err := ws.Refresh("legacy.txt", ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	startSeq := ws.Identity().StateSeq
	finalized := false
	identity, err := ws.CommitPreparedTransaction(context.Background(), "legacy_fixture", "op_replace_symbol_body", []PlanStageFile{{
		Path: "legacy.txt", Before: []byte("before\n"), After: []byte("after\n"),
		BeforeExists: true, AfterExists: true, BeforeDisk: snapshot.Disk,
		AfterDisk: DiskSnapshot{Kind: ObjectRegularText, Size: 6, Mode: 0o600},
	}}, func(context.Context) error {
		finalized = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finalized || identity.StateSeq != startSeq+1 {
		t.Fatalf("finalized=%v identity=%+v", finalized, identity)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, []byte("after\n")) {
		t.Fatalf("canonical bytes = %q, %v", got, err)
	}
	journal, err := ws.loadCommitJournal("legacy_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if journal.State != CommitJournalCommitted || journal.PreparedRevision != "legacy:op_replace_symbol_body" || len(journal.Entries) != 1 {
		t.Fatalf("journal = %+v", journal)
	}
}

type provisionalCommitStager struct {
	*fakePlanStager
	request PlanStageRequest
}

func (s *provisionalCommitStager) Stage(ctx context.Context, request PlanStageRequest) error {
	s.request = request
	return s.fakePlanStager.Stage(ctx, request)
}

func (s *provisionalCommitStager) PreparedRequest() (PlanStageRequest, VerificationResult, bool) {
	return s.request, VerificationResult{Stages: []VerificationStage{{
		Stage: "diagnostics", Status: VerificationSkipped,
		Coverage: Coverage{Complete: false, Semantic: string(ConfidenceUnavailable), Skipped: []string{"lsp_not_configured"}},
	}}}, true
}

func TestCommitExplicitlyAcceptedProvisionalPreparedRevision(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	operation := fullRangeOperation(t, ws, "replace", path, "after\n")
	plan, err := ws.CreatePlan([]PlanOperation{operation})
	if err != nil {
		t.Fatal(err)
	}
	previewed, err := ws.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	base := &fakePlanStager{epoch: ws.Identity().Epoch, buffers: map[string][]byte{"note.txt": []byte("before\n")}}
	stager := &provisionalCommitStager{fakePlanStager: base}
	prepared, err := ws.PreparePlan(context.Background(), previewed.PlanID, previewed.PlanRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != PlanProvisional {
		t.Fatalf("prepared state = %s, want %s", prepared.State, PlanProvisional)
	}
	committed, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != PlanCommitted {
		t.Fatalf("committed state = %s", committed.State)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "after\n" {
		t.Fatalf("canonical bytes = %q, %v", got, err)
	}
}
