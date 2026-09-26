package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// preparedExpiryFixture is a durable workspace with one prepared plan.
func preparedExpiryFixture(t *testing.T) (*Workspace, string, string, PlanRecord, *fakePlanStager) {
	t.Helper()
	t.Setenv(PlanIdleTTLVariable, "")
	root, stateDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "note.txt")
	writeFile(t, path, "old\n")
	ws := openCommitWorkspace(t, root, stateDir)
	prepared, stager := prepareCommitPlan(t, ws, []PlanOperation{fullRangeOperation(t, ws, "a", path, "new\n")})
	if prepared.State != PlanReady {
		t.Fatalf("prepared state = %s", prepared.State)
	}
	return ws, root, stateDir, prepared, stager
}

// ageRecord moves every timestamp of one plan back, as if it had been idle for age.
func ageRecord(t *testing.T, ws *Workspace, planID string, age time.Duration) {
	t.Helper()
	ws.plansMu.Lock()
	defer ws.plansMu.Unlock()
	plan := ws.plans[planID]
	for i := range plan.Events {
		plan.Events[i].At = plan.Events[i].At.Add(-age)
	}
	plan.CreatedAt, plan.UpdatedAt = plan.CreatedAt.Add(-age), plan.UpdatedAt.Add(-age)
	ws.plans[planID] = plan
	if err := ws.persistPlanLocked(planID); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedPlanExpiresOnlyAfterItsIdleLimit(t *testing.T) {
	ws, _, _, prepared, stager := preparedExpiryFixture(t)
	now := prepared.UpdatedAt
	if expired, err := ws.ExpireIdlePlans(now.Add(DefaultPlanIdleTTL), DefaultPlanIdleTTL, nil); err != nil || len(expired) != 0 {
		t.Fatalf("expired at the limit: %v, %v", expired, err)
	}
	// Use the stager reported keeps the plan alive past its record's age.
	used := map[string]time.Time{prepared.PlanID: now.Add(30 * time.Minute)}
	if expired, err := ws.ExpireIdlePlans(now.Add(DefaultPlanIdleTTL+time.Minute), DefaultPlanIdleTTL, used); err != nil || len(expired) != 0 {
		t.Fatalf("expired a plan used recently: %v, %v", expired, err)
	}
	expired, err := ws.ExpireIdlePlans(now.Add(DefaultPlanIdleTTL+time.Minute), DefaultPlanIdleTTL, nil)
	if err != nil || len(expired) != 1 || expired[0].PlanID != prepared.PlanID || expired[0].State != PlanExpired {
		t.Fatalf("expired = %#v, %v", expired, err)
	}
	ws.prepareMu.Lock()
	_, leased := ws.activePlans[prepared.PlanID]
	ws.prepareMu.Unlock()
	if leased {
		t.Fatal("expired plan kept its prepare lease")
	}
	_, err = ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision, prepared.Preparation.PreparedRevision, stager)
	if ErrorCode(err) != CodePlanStateInvalid || !strings.Contains(err.Error(), "prepare it again") {
		t.Fatalf("apply of an expired plan = %v", err)
	}
	// The refusal names the way back, and it works: the plan kept its operations.
	again, err := ws.PreparePlan(context.Background(), prepared.PlanID, prepared.PlanRevision, stagerBuffersFor(ws, prepared))
	if err != nil || again.State != PlanReady {
		t.Fatalf("re-prepare after expiry = %#v, %v", again, err)
	}
}

func TestUnpreparedPlanExpiresAndZeroLimitExpiresNothing(t *testing.T) {
	t.Setenv(PlanIdleTTLVariable, "")
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	writeFile(t, path, "old\n")
	ws := openCommitWorkspace(t, root, t.TempDir())
	plan, err := ws.CreatePlan([]PlanOperation{fullRangeOperation(t, ws, "a", path, "new\n")})
	if err != nil {
		t.Fatal(err)
	}
	later := plan.UpdatedAt.Add(2 * DefaultPlanIdleTTL)
	if expired, err := ws.ExpireIdlePlans(later, 0, nil); err != nil || len(expired) != 0 {
		t.Fatalf("a zero limit expired %v, %v", expired, err)
	}
	if expired, err := ws.ExpireIdlePlans(later, DefaultPlanIdleTTL, nil); err != nil || len(expired) != 1 {
		t.Fatalf("open plan not expired: %v, %v", expired, err)
	}
	if discarded, err := ws.DiscardPlan(plan.PlanID, plan.PlanRevision); err != nil || discarded.State != PlanDiscarded {
		t.Fatalf("discard of an expired plan = %#v, %v", discarded, err)
	}
}

func TestPlanIdleTTLReadsTheEnvironment(t *testing.T) {
	for value, want := range map[string]time.Duration{
		"": DefaultPlanIdleTTL, "off": 0, "0": 0, "90m": 90 * time.Minute, "soon": DefaultPlanIdleTTL, "-1h": DefaultPlanIdleTTL,
	} {
		t.Setenv(PlanIdleTTLVariable, value)
		if got := PlanIdleTTL(); got != want {
			t.Fatalf("%s=%q gives %s, want %s", PlanIdleTTLVariable, value, got, want)
		}
	}
}

// A restart discards the sandbox but keeps the plan recoverable. Its idle time
// must still count from the last real use: the restart itself is not a use.
func TestPreparedPlanExpiryCountsAcrossRestart(t *testing.T) {
	ws, root, stateDir, prepared, _ := preparedExpiryFixture(t)
	ageRecord(t, ws, prepared.PlanID, 50*time.Minute)
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	restored, err := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || restored.State != PlanFailed {
		t.Fatalf("restored = %#v, %v", restored, err)
	}
	if _, ok := reopened.RecoverablePreparedPlan(prepared.Preparation.PreparedRevision); !ok {
		t.Fatal("a plan inside its limit is not recoverable after restart")
	}
	now := time.Now().UTC()
	if expired, _ := reopened.ExpireIdlePlans(now.Add(5*time.Minute), DefaultPlanIdleTTL, nil); len(expired) != 0 {
		t.Fatal("expired before the limit")
	}
	if expired, _ := reopened.ExpireIdlePlans(now.Add(11*time.Minute), DefaultPlanIdleTTL, nil); len(expired) != 1 {
		t.Fatal("the restart gave the plan a fresh idle limit")
	}
	if _, ok := reopened.RecoverablePreparedPlan(prepared.Preparation.PreparedRevision); ok {
		t.Fatal("an expired plan is still recoverable")
	}
	// The expiry is durable.
	again := reopenCommitWorkspace(t, reopened, root, stateDir)
	if plan, err := again.InspectPlan(prepared.PlanID, prepared.PlanRevision); err != nil || plan.State != PlanExpired {
		t.Fatalf("after a second restart = %#v, %v", plan, err)
	}
}

func TestPlanIdleBeforeRestartExpiresOnLoad(t *testing.T) {
	ws, root, stateDir, prepared, _ := preparedExpiryFixture(t)
	ageRecord(t, ws, prepared.PlanID, 2*DefaultPlanIdleTTL)
	reopened := reopenCommitWorkspace(t, ws, root, stateDir)
	plan, err := reopened.InspectPlan(prepared.PlanID, prepared.PlanRevision)
	if err != nil || plan.State != PlanExpired {
		t.Fatalf("stale prepared plan after restart = %#v, %v", plan, err)
	}
	if _, ok := reopened.RecoverablePreparedPlan(prepared.PlanID); ok {
		t.Fatal("apply could recreate a preparation past its limit")
	}
}
