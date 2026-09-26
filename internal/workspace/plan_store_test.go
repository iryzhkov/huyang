package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func bulkyPlan(ws *Workspace, id string, state PlanState, updatedAt time.Time) PlanRecord {
	handle := RangeHandle{Path: "note.txt", ByteStart: 0, ByteEnd: 3, Revision: "rev_x", ExpectedSHA256: "abc"}
	plan := PlanRecord{
		PlanID: id, WorkspaceID: ws.Identity().ID, State: state, PlanRevision: 2, BaseStateSeq: 1,
		Operations: []PlanOperation{{
			OpID: "op", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &handle}, Content: "payload " + id,
		}},
		Preview: &PlanPreview{PreviewRevision: "preview_" + id, PlanRevision: 2, Outcome: "ok", Diffs: []ExactDiff{{Path: "note.txt"}}},
		Preparation: &PlanPreparation{
			PreparedRevision: "prep_" + id, CanonicalRevision: "wsrev_9", CanonicalFromRevision: "wsrev_8", JournalID: id,
			CommittedDiffs: []ExactDiff{{Path: "note.txt", Before: []byte("old"), After: []byte("new")}},
			Verification:   []VerificationStage{{Stage: "diagnostics", Output: "lots of output"}},
		},
		Conflict:  &PlanConflictReason{Code: CodeCommitPreconditionChanged, Message: "kept"},
		Events:    []PlanEvent{{Action: "create", PlanRevision: 1, Outcome: "ok", At: updatedAt}},
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}
	return plan
}

func TestLegacyPlanFileIsMigratedPrunedAndCompacted(t *testing.T) {
	// The open plan below is older than the idle limit. This test is about
	// terminal retention leaving a non-terminal plan alone, so idle expiry,
	// which would make it terminal first, is off.
	t.Setenv(PlanIdleTTLVariable, "off")
	root, stateDir := t.TempDir(), t.TempDir()
	ws := openCommitWorkspace(t, root, stateDir)
	now := time.Now().UTC()
	var plans []PlanRecord
	recent := make([]string, 0, planRetainCount+50)
	for i := 0; i < planRetainCount+50; i++ {
		id := fmt.Sprintf("plan_recent_%03d", i)
		recent = append(recent, id)
		plans = append(plans, bulkyPlan(ws, id, PlanCommitted, now.Add(-2*time.Hour-time.Duration(i)*time.Second)))
	}
	var expired []string
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("plan_expired_%d", i)
		expired = append(expired, id)
		plans = append(plans, bulkyPlan(ws, id, PlanDiscarded, now.Add(-planRetainAge-time.Hour)))
	}
	plans = append(plans, bulkyPlan(ws, "plan_open", PlanOpen, now.Add(-planRetainAge-time.Hour)))
	plans = append(plans, bulkyPlan(ws, "plan_held", PlanCommitted, now.Add(-planRetainAge-time.Hour)))
	held := CommitJournal{
		Version: commitJournalVersion, WorkspaceID: ws.Identity().ID, PlanID: "plan_held", PlanRevision: 2,
		PreparedRevision: "prep_plan_held", State: CommitJournalApplying, CreatedAt: now,
	}
	if err := ws.writeCommitJournal(ws.commitJournalPath("plan_held"), &held); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(persistedPlans{Version: planStateVersion, Plans: plans})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ws.legacyPlanStatePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ws.legacyPlanStatePath(), content, 0o600); err != nil {
		t.Fatal(err)
	}

	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	if _, err := os.Stat(ws.legacyPlanStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy plan file survived migration: %v", err)
	}
	for _, id := range append(expired, recent[planRetainCount:]...) {
		if _, err := reopened.InspectPlan(id, 0); err == nil {
			t.Fatalf("plan %s survived retention", id)
		}
		if _, err := os.Stat(reopened.planRecordPath(id)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("record for dropped plan %s survived: %v", id, err)
		}
	}
	for _, id := range recent[:planRetainCount] {
		plan, err := reopened.InspectPlan(id, 2)
		if err != nil {
			t.Fatalf("retained plan %s: %v", id, err)
		}
		if !plan.Compacted || plan.Preview != nil || plan.Operations[0].Content != "" || plan.Operations[0].Target != nil ||
			plan.Preparation == nil || plan.Preparation.CommittedDiffs != nil || plan.Preparation.Verification != nil {
			t.Fatalf("plan %s was not compacted: %#v", id, plan)
		}
		if plan.State != PlanCommitted || plan.PlanRevision != 2 || plan.Conflict == nil ||
			plan.Preparation.CanonicalRevision != "wsrev_9" || plan.Preparation.CanonicalFromRevision != "wsrev_8" ||
			plan.Preparation.JournalID != id || plan.Preparation.PreparedRevision != "prep_"+id {
			t.Fatalf("compaction lost identity or receipt: %#v", plan)
		}
		if _, err := reopened.EditPlan(id, 2, PlanEdit{Mode: "remove", OpIDs: []string{"op"}}); ErrorCode(err) != CodePlanStateInvalid {
			t.Fatalf("compacted plan was editable: %v", err)
		}
	}
	open, err := reopened.InspectPlan("plan_open", 2)
	if err != nil || open.Compacted || open.Preview == nil || open.Operations[0].Content != "payload plan_open" {
		t.Fatalf("non-terminal plan was pruned or compacted: %#v, %v", open, err)
	}
	if heldPlan, err := reopened.InspectPlan("plan_held", 2); err != nil || heldPlan.State != PlanCommitted {
		t.Fatalf("plan with an incomplete journal was dropped: %#v, %v", heldPlan, err)
	}
	entries, err := os.ReadDir(reopened.planStateDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := planRetainCount + 2; len(entries) != want {
		t.Fatalf("plan records on disk = %d, want %d", len(entries), want)
	}
	record, err := os.ReadFile(reopened.planRecordPath("plan_open"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedPlan
	if err := json.Unmarshal(record, &persisted); err != nil || persisted.Version != planRecordVersion || persisted.Plan.PlanID != "plan_open" {
		t.Fatalf("per-plan record = %#v, %v", persisted, err)
	}
	// A second open finds only per-plan records and changes nothing.
	again := reopenCommitWorkspace(t, reopened, root, stateDir)
	if plan, err := again.InspectPlan(recent[0], 2); err != nil || !plan.Compacted {
		t.Fatalf("second open lost the compacted record: %#v, %v", plan, err)
	}
}

func TestPlanMutationRewritesOnlyItsOwnRecord(t *testing.T) {
	ws, _, plan, _ := prepareFixture(t)
	other, err := ws.CreatePlan(plan.Operations)
	if err != nil {
		t.Fatal(err)
	}
	otherPath := ws.planRecordPath(other.PlanID)
	before, err := os.Stat(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.PreviewPlan(plan.PlanID, plan.PlanRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.DiscardPlan(plan.PlanID, plan.PlanRevision); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(otherPath)
	if err != nil || after.ModTime() != before.ModTime() || after.Size() != before.Size() {
		t.Fatalf("mutating one plan rewrote another record: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.stateDir, "plans", string(ws.Identity().ID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("monolithic plan file was written: %v", err)
	}
}
