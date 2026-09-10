package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const crashWorkspaceID ID = "ws_0123456789abcdef0123456789abcdef"

func journalFixture(t *testing.T, root, stateDir string, state CommitJournalState, canonicalPostimage bool) (*Workspace, string) {
	t.Helper()
	path := filepath.Join(root, "value.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	before, preimage, err := inspectPath(path)
	if err != nil {
		t.Fatal(err)
	}
	after := before
	after.Device, after.Inode, after.MTimeNS = 0, 0, 0
	after.Size = int64(len("new\n"))
	journal := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: crashWorkspaceID,
		PlanID: "plan_fixture", PlanRevision: 1, PreparedRevision: "prep_fixture",
		State: state, CreatedAt: time.Now().UTC(),
		Entries: []CommitJournalEntry{{
			Path: path, Preimage: preimage, Postimage: []byte("new\n"),
			Before: before, After: after, Progress: CommitPathPending,
		}},
	}
	if canonicalPostimage {
		if err := applyCommitEntry(path, journal.Entries[0]); err != nil {
			t.Fatal(err)
		}
	}
	journalPath := ws.commitJournalPath(journal.PlanID)
	if err := ws.writeCommitJournal(journalPath, &journal); err != nil {
		t.Fatal(err)
	}
	return ws, journalPath
}

func TestStartupRecoveryHandlesEveryIncompleteJournalState(t *testing.T) {
	for _, state := range []CommitJournalState{
		CommitJournalPrepared,
		CommitJournalApplying,
		CommitJournalRecoveryRequired,
	} {
		t.Run(string(state), func(t *testing.T) {
			root, stateDir := t.TempDir(), t.TempDir()
			_, journalPath := journalFixture(t, root, stateDir, state, state != CommitJournalPrepared)
			reopened, err := Open(OpenOptions{
				Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(root, "value.txt"))
			if err != nil || string(got) != "old\n" {
				t.Fatalf("recovered bytes = %q, %v", got, err)
			}
			journal, err := loadCommitJournalFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			if journal.State != CommitJournalRolledBack || journal.Entries[0].Progress != CommitPathRestored {
				t.Fatalf("recovered journal = %#v", journal)
			}
			if reopened.Identity().ID != crashWorkspaceID {
				t.Fatalf("reopened identity = %s", reopened.Identity().ID)
			}
		})
	}
}

func TestStartupRecoveryPreservesThirdPartyWritesBeforeAndDuringRecovery(t *testing.T) {
	t.Run("before_open", func(t *testing.T) {
		root, stateDir := t.TempDir(), t.TempDir()
		_, journalPath := journalFixture(t, root, stateDir, CommitJournalApplying, true)
		path := filepath.Join(root, "value.txt")
		if err := os.WriteFile(path, []byte("third-party\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(OpenOptions{
			Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
		}); err == nil || !strings.Contains(err.Error(), "commit_recovery_required") {
			t.Fatalf("open conflict = %v", err)
		}
		assertThirdPartyRecoveryConflict(t, path, journalPath)
	})

	t.Run("during_recovery", func(t *testing.T) {
		root, stateDir := t.TempDir(), t.TempDir()
		_, journalPath := journalFixture(t, root, stateDir, CommitJournalApplying, true)
		path := filepath.Join(root, "value.txt")
		fired := false
		_, err := Open(OpenOptions{
			Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
			CommitFault: func(point, faultPath string) error {
				if point == "before_recovery_apply" && faultPath == path && !fired {
					fired = true
					return os.WriteFile(path, []byte("third-party\n"), 0o640)
				}
				return nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "commit_recovery_required") {
			t.Fatalf("open race conflict = %v", err)
		}
		assertThirdPartyRecoveryConflict(t, path, journalPath)
	})
}

func assertThirdPartyRecoveryConflict(t *testing.T, path, journalPath string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "third-party\n" {
		t.Fatalf("third-party bytes = %q, %v", got, err)
	}
	journal, err := loadCommitJournalFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.State != CommitJournalRecoveryRequired || journal.LastError == "" {
		t.Fatalf("conflict journal = %#v", journal)
	}
}

func TestCompensatingTransactionRestoresExactCommittedPreimages(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	script := filepath.Join(root, "script.sh")
	deleted := filepath.Join(root, "deleted.txt")
	linkSource := filepath.Join(root, "shortcut")
	linkDestination := filepath.Join(root, "shortcut-moved")
	created := filepath.Join(root, "created.txt")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deleted, []byte("restore me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("deleted.txt", linkSource); err != nil {
		t.Fatal(err)
	}
	ws := openCommitWorkspace(t, root, stateDir)
	operations := []PlanOperation{
		fullRangeOperation(t, ws, "replace", script, "#!/bin/sh\necho new\n"),
		{OpID: "delete", Kind: OperationDeleteFile, Path: deleted, Revision: revisionAt(t, ws, deleted)},
		{OpID: "create", Kind: OperationCreateFile, Path: created, Revision: revisionAt(t, ws, created), Content: "created\n"},
		{OpID: "move-link", Kind: OperationMoveFile, From: linkSource, To: linkDestination, Revision: revisionAt(t, ws, linkSource), DestinationRevision: revisionAt(t, ws, linkDestination)},
	}
	prepared, stager := prepareCommitPlan(t, ws, operations)
	committed, err := ws.CommitPlan(
		context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager,
	)
	if err != nil {
		t.Fatal(err)
	}
	startSeq := ws.Identity().StateSeq
	result, err := ws.CompensatePlan(context.Background(), committed.PlanID, stager)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CommitJournalCommitted || result.CompensatesPlanID != committed.PlanID ||
		result.CanonicalRevision == "" || ws.Identity().StateSeq != startSeq+1 {
		t.Fatalf("compensation = %#v identity=%#v", result, ws.Identity())
	}
	assertRegular := func(path, content string, mode os.FileMode) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("%s = %q, %v", path, got, err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, %v", path, info.Mode().Perm(), err)
		}
	}
	assertRegular(script, "#!/bin/sh\necho old\n", 0o755)
	assertRegular(deleted, "restore me\n", 0o600)
	if _, err := os.Lstat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file survived compensation: %v", err)
	}
	target, err := os.Readlink(linkSource)
	if err != nil || target != "deleted.txt" {
		t.Fatalf("restored symlink = %q, %v", target, err)
	}
	if _, err := os.Lstat(linkDestination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("moved symlink destination survived: %v", err)
	}
	journal, err := loadCommitJournalFile(ws.compensationJournalPath(committed.PlanID))
	if err != nil {
		t.Fatal(err)
	}
	if journal.CompensatesPlanID != committed.PlanID || journal.State != CommitJournalCommitted {
		t.Fatalf("compensation journal = %#v", journal)
	}
	replayed, err := ws.CompensatePlan(context.Background(), committed.PlanID, stager)
	if err != nil || replayed != result {
		t.Fatalf("idempotent compensation = %#v, %v", replayed, err)
	}
}

func TestCrashRecoveryKillsSeparateServiceProcessAtEveryDurableStep(t *testing.T) {
	steps := []struct {
		point      string
		path       string
		occurrence int
		committed  bool
	}{
		{"before_prepare_journal", "", 1, false},
		{"after_prepare_journal", "", 1, false},
		{"before_applying_journal", "", 1, false},
		{"after_applying_journal", "", 1, false},
		{"before_apply", "a.txt", 1, false},
		{"after_apply", "a.txt", 1, false},
		{"before_progress_journal", "", 1, false},
		{"after_progress_journal", "", 1, false},
		{"before_apply", "b.txt", 1, false},
		{"after_apply", "b.txt", 1, false},
		{"before_progress_journal", "", 2, false},
		{"after_progress_journal", "", 2, false},
		{"before_committed_journal", "", 1, false},
		{"after_committed_journal", "", 1, true},
	}
	for _, step := range steps {
		name := fmt.Sprintf("%s_%s_%d", step.point, step.path, step.occurrence)
		t.Run(name, func(t *testing.T) {
			root, stateDir := t.TempDir(), t.TempDir()
			for _, name := range []string{"a.txt", "b.txt"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("old-"+name+"\n"), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(t.TempDir(), "reached")
			command := exec.Command(os.Args[0], "-test.run", "^TestCrashCommitServiceHelper$")
			command.Env = append(os.Environ(),
				"HUYANG_CRASH_HELPER=1",
				"HUYANG_CRASH_ROOT="+root,
				"HUYANG_CRASH_STATE="+stateDir,
				"HUYANG_CRASH_POINT="+step.point,
				"HUYANG_CRASH_PATH="+step.path,
				"HUYANG_CRASH_OCCURRENCE="+strconv.Itoa(step.occurrence),
				"HUYANG_CRASH_MARKER="+marker,
			)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = command.Process.Kill()
					_ = command.Wait()
					t.Fatalf("helper did not reach failpoint: %s", output.String())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()

			reopened, err := Open(OpenOptions{
				Kind: KindProject, Root: root, StateDir: stateDir,
				Identity: crashWorkspaceID, ProviderEpoch: 1,
			})
			if err != nil {
				t.Fatalf("startup recovery: %v; helper: %s", err, output.String())
			}
			for _, file := range []string{"a.txt", "b.txt"} {
				want := "old-" + file + "\n"
				if step.committed {
					want = "new-" + file + "\n"
				}
				got, err := os.ReadFile(filepath.Join(root, file))
				if err != nil || string(got) != want {
					t.Fatalf("%s = %q, %v; want %q", file, got, err, want)
				}
			}
			if reopened.Identity().ID != crashWorkspaceID {
				t.Fatalf("reopened workspace = %s", reopened.Identity().ID)
			}
		})
	}
}

func TestCrashRecoveryCanItselfResumeAfterEveryDurableStep(t *testing.T) {
	for _, point := range []string{
		"before_recovery_apply",
		"after_recovery_apply",
		"before_recovery_progress_journal",
		"after_recovery_progress_journal",
		"before_recovered_journal",
		"after_recovered_journal",
	} {
		t.Run(point, func(t *testing.T) {
			root, stateDir := t.TempDir(), t.TempDir()
			_, _ = journalFixture(t, root, stateDir, CommitJournalApplying, true)
			marker := filepath.Join(t.TempDir(), "reached")
			command := exec.Command(os.Args[0], "-test.run", "^TestCrashRecoveryServiceHelper$")
			command.Env = append(os.Environ(),
				"HUYANG_RECOVERY_HELPER=1",
				"HUYANG_CRASH_ROOT="+root,
				"HUYANG_CRASH_STATE="+stateDir,
				"HUYANG_CRASH_POINT="+point,
				"HUYANG_CRASH_MARKER="+marker,
			)
			var output bytes.Buffer
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = command.Process.Kill()
					_ = command.Wait()
					t.Fatalf("recovery helper did not reach failpoint: %s", output.String())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			if _, err := Open(OpenOptions{
				Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
			}); err != nil {
				t.Fatalf("resumed startup recovery: %v; helper: %s", err, output.String())
			}
			got, err := os.ReadFile(filepath.Join(root, "value.txt"))
			if err != nil || string(got) != "old\n" {
				t.Fatalf("resumed bytes = %q, %v", got, err)
			}
		})
	}
}

func TestCrashCommitServiceHelper(t *testing.T) {
	if os.Getenv("HUYANG_CRASH_HELPER") != "1" {
		return
	}
	root, stateDir := os.Getenv("HUYANG_CRASH_ROOT"), os.Getenv("HUYANG_CRASH_STATE")
	point, relativePath := os.Getenv("HUYANG_CRASH_POINT"), os.Getenv("HUYANG_CRASH_PATH")
	occurrence, err := strconv.Atoi(os.Getenv("HUYANG_CRASH_OCCURRENCE"))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	ws, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: stateDir, Identity: crashWorkspaceID,
		ProviderEpoch: 1,
		CommitFault: func(candidate, path string) error {
			if candidate != point {
				return nil
			}
			if relativePath != "" && filepath.Base(path) != relativePath {
				return nil
			}
			seen++
			if seen != occurrence {
				return nil
			}
			if err := os.WriteFile(os.Getenv("HUYANG_CRASH_MARKER"), []byte(candidate), 0o600); err != nil {
				return err
			}
			select {}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operations := []PlanOperation{
		fullRangeOperation(t, ws, "a", filepath.Join(root, "a.txt"), "new-a.txt\n"),
		fullRangeOperation(t, ws, "b", filepath.Join(root, "b.txt"), "new-b.txt\n"),
	}
	prepared, stager := prepareCommitPlan(t, ws, operations)
	if _, err := ws.CommitPlan(
		context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager,
	); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("helper completed without reaching %s occurrence %d", point, occurrence)
}

func TestCrashRecoveryServiceHelper(t *testing.T) {
	if os.Getenv("HUYANG_RECOVERY_HELPER") != "1" {
		return
	}
	point := os.Getenv("HUYANG_CRASH_POINT")
	_, err := Open(OpenOptions{
		Kind: KindProject, Root: os.Getenv("HUYANG_CRASH_ROOT"),
		StateDir: os.Getenv("HUYANG_CRASH_STATE"), Identity: crashWorkspaceID,
		CommitFault: func(candidate, _ string) error {
			if candidate != point {
				return nil
			}
			if err := os.WriteFile(os.Getenv("HUYANG_CRASH_MARKER"), []byte(candidate), 0o600); err != nil {
				return err
			}
			select {}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("recovery helper completed without reaching %s", point)
}
