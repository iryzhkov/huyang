package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// evaluatorFunc answers invariants the way a test wants them answered.
type evaluatorFunc func(PlanRecord, PlanPreparation) []PlanInvariant

func (f evaluatorFunc) EvaluateInvariants(_ context.Context, plan PlanRecord, preparation PlanPreparation) []PlanInvariant {
	return f(plan, preparation)
}

func invariantWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	root, stateDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return openCommitWorkspace(t, root, stateDir), root
}

func prepareWithInvariants(t *testing.T, ws *Workspace, invariants []PlanInvariant, answer func(PlanRecord, PlanPreparation) []PlanInvariant) (PlanRecord, *fakePlanStager) {
	t.Helper()
	plan, err := ws.CreatePlanWithInvariants(
		[]PlanOperation{fullRangeOperation(t, ws, "replace", "note.txt", "after\n")}, invariants)
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
	var options []PrepareOption
	if answer != nil {
		options = append(options, WithInvariantEvaluator(evaluatorFunc(answer)))
	}
	prepared, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager, options...)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, stager
}

// A caller can ask for an invariant. It cannot ask for the answer.
func TestADeclaredInvariantIsPendingWhateverTheCallerSaid(t *testing.T) {
	normalized, err := normalizeInvariants([]PlanInvariant{{
		Kind: InvariantTestsPass, Status: InvariantProven, EvaluatedRevision: "prep_whatever",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if normalized[0].Status != InvariantPending || normalized[0].EvaluatedRevision != "" {
		t.Fatalf("a declaration carried its own proof: %#v", normalized[0])
	}
	if normalized[0].Enforcement != InvariantRequired || normalized[0].ID != "inv_1" {
		t.Fatalf("unexpected normalization: %#v", normalized[0])
	}
}

func TestADeclarationThatCannotBeEvaluatedIsRefused(t *testing.T) {
	cases := map[string][]PlanInvariant{
		"unknown kind":    {{ID: "a", Kind: "whatever_i_like"}},
		"no symbol":       {{ID: "a", Kind: InvariantNoReferences}},
		"duplicate id":    {{ID: "a", Kind: InvariantTestsPass}, {ID: "a", Kind: InvariantNoNewDiagnostics}},
		"bad enforcement": {{ID: "a", Kind: InvariantTestsPass, Enforcement: "please"}},
	}
	for name, invariants := range cases {
		if _, err := normalizeInvariants(invariants); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	many := make([]PlanInvariant, planInvariantLimit+1)
	for index := range many {
		many[index] = PlanInvariant{Kind: InvariantTestsPass}
	}
	if _, err := normalizeInvariants(many); err == nil {
		t.Fatal("an unbounded invariant list was accepted")
	}
}

// An unproven required invariant keeps the plan out of READY, and apply says
// so rather than letting accept_provisional past it.
func TestARequiredInvariantThatIsUnknownBlocksApply(t *testing.T) {
	ws, _ := invariantWorkspace(t)
	prepared, stager := prepareWithInvariants(t, ws,
		[]PlanInvariant{{ID: "tests", Kind: InvariantTestsPass}},
		func(_ PlanRecord, _ PlanPreparation) []PlanInvariant {
			return []PlanInvariant{{ID: "tests", Status: InvariantUnknown, Detail: "this preparation ran no tests stage"}}
		})
	if prepared.State != PlanProvisional {
		t.Fatalf("state = %s, want PROVISIONAL", prepared.State)
	}
	if got := UnprovenRequiredInvariants(prepared); len(got) != 1 {
		t.Fatalf("unproven required = %#v", got)
	}
	_, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager, AcceptProvisional("diagnostics"), AcceptProvisional("invariant:tests"))
	if ErrorCode(err) != CodeInvariantNotProven {
		t.Fatalf("commit error = %v, want %s", err, CodeInvariantNotProven)
	}
	if !strings.Contains(err.Error(), "ran no tests stage") {
		t.Fatalf("refusal does not say what is missing: %v", err)
	}
}

// An advisory invariant nobody could answer is still worth saying, and still
// costs an explicit acceptance, but it does not stop the change.
func TestAnAdvisoryInvariantLeavesThePlanApplicable(t *testing.T) {
	ws, root := invariantWorkspace(t)
	prepared, stager := prepareWithInvariants(t, ws,
		[]PlanInvariant{{ID: "tests", Kind: InvariantTestsPass, Enforcement: InvariantAdvisory}},
		func(_ PlanRecord, _ PlanPreparation) []PlanInvariant {
			return []PlanInvariant{{ID: "tests", Status: InvariantUnknown, Detail: "no tests stage"}}
		})
	if prepared.State != PlanProvisional {
		t.Fatalf("state = %s, want PROVISIONAL", prepared.State)
	}
	gaps := PreparationGaps(prepared)
	if len(gaps) != 1 || gaps[0].Dimension != "invariant:tests" {
		t.Fatalf("gaps = %#v", gaps)
	}
	if _, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager); ErrorCode(err) != CodeProvisionalNotAccepted {
		t.Fatalf("unaccepted commit error = %v", err)
	}
	prepared, _ = prepareWithInvariants(t, ws,
		[]PlanInvariant{{ID: "tests", Kind: InvariantTestsPass, Enforcement: InvariantAdvisory}},
		func(_ PlanRecord, _ PlanPreparation) []PlanInvariant {
			return []PlanInvariant{{ID: "tests", Status: InvariantUnknown, Detail: "no tests stage"}}
		})
	stager = stagerBuffersFor(ws, prepared)
	committed, err := ws.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager, AcceptProvisional("invariant:tests"))
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != PlanCommitted {
		t.Fatalf("state = %s", committed.State)
	}
	if content, err := os.ReadFile(filepath.Join(root, "note.txt")); err != nil || string(content) != "after\n" {
		t.Fatalf("canonical bytes = %q, %v", content, err)
	}
}

// A proof is about one prepared revision. Editing the plan makes it a proof
// about bytes nobody proposes any more.
func TestEditingAPlanDropsItsProofs(t *testing.T) {
	ws, _ := invariantWorkspace(t)
	plan, err := ws.CreatePlanWithInvariants(
		[]PlanOperation{fullRangeOperation(t, ws, "replace", "note.txt", "after\n")},
		[]PlanInvariant{{ID: "tests", Kind: InvariantTestsPass}})
	if err != nil {
		t.Fatal(err)
	}
	proven := plan.Invariants
	proven[0].Status, proven[0].EvaluatedRevision = InvariantProven, "prep_earlier"
	if err := ws.recordInvariants(plan.PlanID, plan.PlanRevision, proven); err != nil {
		t.Fatal(err)
	}
	edited, err := ws.EditPlan(plan.PlanID, plan.PlanRevision, PlanEdit{Mode: "remove", OpIDs: []string{"nothing"}})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Invariants[0].Status != InvariantPending || edited.Invariants[0].EvaluatedRevision != "" {
		t.Fatalf("an edit kept a proof: %#v", edited.Invariants[0])
	}
	// The declarations themselves survive: what the plan must satisfy did not
	// change, only what is known about it.
	if len(edited.Invariants) != 1 || edited.Invariants[0].Kind != InvariantTestsPass {
		t.Fatalf("an edit dropped the declaration: %#v", edited.Invariants)
	}
	relaxed, err := ws.EditPlan(edited.PlanID, edited.PlanRevision, PlanEdit{
		Mode: "remove", Invariants: &[]PlanInvariant{{ID: "tests", Kind: InvariantTestsPass, Enforcement: InvariantAdvisory}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if relaxed.Invariants[0].Enforcement != InvariantAdvisory {
		t.Fatalf("an edit could not change what the plan requires: %#v", relaxed.Invariants[0])
	}
}

// A proof that names another preparation of the same plan is not a proof of
// this one.
func TestAProofOfAnotherRevisionIsNotAProof(t *testing.T) {
	invariant := PlanInvariant{
		ID: "tests", Kind: InvariantTestsPass, Enforcement: InvariantRequired,
		Status: InvariantProven, EvaluatedRevision: "prep_older",
	}
	if invariant.provenAt("prep_current") {
		t.Fatal("a proof of another revision was accepted")
	}
	if !invariant.provenAt("prep_older") {
		t.Fatal("a proof of its own revision was refused")
	}
	if invariant.provenAt("") {
		t.Fatal("a proof of no revision at all was accepted")
	}
}

// Reading a plan written before invariants existed must not invent one.
func TestALegacyPlanRecordCarriesNoRequirements(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	ws := openCommitWorkspace(t, root, stateDir)
	identity := ws.Identity()
	legacy := persistedPlan{Version: planRecordVersionLegacy, Plan: PlanRecord{
		PlanID: "plan_legacy", WorkspaceID: identity.ID, State: PlanOpen, PlanRevision: 1,
		Operations: []PlanOperation{}, Events: []PlanEvent{{Action: "create", PlanRevision: 1, Outcome: "ok"}},
	}}
	content, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(stateDir, "plans", string(identity.ID))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plan_legacy.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: stateDir, Identity: identity.ID, ProviderEpoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reopened.InspectPlan("plan_legacy", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Invariants) != 0 || len(UnprovenRequiredInvariants(plan)) != 0 {
		t.Fatalf("a legacy plan gained requirements: %#v", plan.Invariants)
	}
}
