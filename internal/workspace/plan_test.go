package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPlanPreviewPersistsDeterministicallyWithoutMutation(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	path := filepath.Join(root, "note.txt")
	original := []byte("alpha beta gamma\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	first, err := opened.NewRange("note.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := opened.NewRange("note.txt", 11, 16)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := opened.CreatePlan([]PlanOperation{
		{OpID: "early", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &first}, Content: "A"},
		{OpID: "late", Kind: OperationDeleteSymbol, Target: &PlanTarget{FileRange: &second}, Content: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Operations[1].Content != "" {
		t.Fatalf("delete_symbol content was not normalized: %q", plan.Operations[1].Content)
	}
	plan, err = opened.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != PlanPreviewed || plan.Preview == nil || plan.Preview.Outcome != "ok" {
		t.Fatalf("unexpected previewed plan: %#v", plan)
	}
	if got, want := plan.Preview.NormalizedOrder, []string{"late", "early"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized order = %v, want %v", got, want)
	}
	if len(plan.Preview.Diffs) != 1 || !bytes.Equal(plan.Preview.Diffs[0].After, []byte("A beta \n")) {
		t.Fatalf("unexpected preview diff: %#v", plan.Preview.Diffs)
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("preview mutated canonical file: %q, %v", current, err)
	}

	identity := opened.Identity()
	reopened, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: stateDir, Identity: identity.ID,
		ProviderEpoch: identity.Epoch, StateSeq: identity.StateSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Preview, plan.Preview) {
		t.Fatalf("restored preview differs\n got: %#v\nwant: %#v", restored.Preview, plan.Preview)
	}
	repreviewed, err := reopened.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repreviewed.Preview, plan.Preview) {
		t.Fatalf("repreview differs\n got: %#v\nwant: %#v", repreviewed.Preview, plan.Preview)
	}
	if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, original) {
		t.Fatalf("restart preview mutated canonical file: %q, %v", current, err)
	}
}

func TestPlanPreviewReportsEveryStaleOperationWithoutPartialApplication(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	aPath := filepath.Join(root, "a.txt")
	bPath := filepath.Join(root, "b.txt")
	if err := os.WriteFile(aPath, []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	a, err := opened.NewRange("a.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := opened.NewRange("b.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := opened.CreatePlan([]PlanOperation{
		{OpID: "a", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &a}, Content: "A"},
		{OpID: "b", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &b}, Content: "B"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aPath, []byte("external a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bPath, []byte("external b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err = opened.PreviewPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Preview.Outcome != "conflict" || len(plan.Preview.Conflicts) != 2 {
		t.Fatalf("preview did not report complete conflict vector: %#v", plan.Preview)
	}
	if len(plan.Preview.Diffs) != 0 || plan.Preview.CanonicalChanged {
		t.Fatalf("conflicted preview exposed partial application: %#v", plan.Preview)
	}
	if current, _ := os.ReadFile(aPath); !bytes.Equal(current, []byte("external a\n")) {
		t.Fatalf("a.txt was modified: %q", current)
	}
	if current, _ := os.ReadFile(bPath); !bytes.Equal(current, []byte("external b\n")) {
		t.Fatalf("b.txt was modified: %q", current)
	}
}

func TestPlanLifecycleEditsAndDiscardAreDurable(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("one two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(OpenOptions{Kind: KindProject, Root: root, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	one, _ := opened.NewRange("note.txt", 0, 3)
	two, _ := opened.NewRange("note.txt", 4, 7)
	plan, err := opened.CreatePlan([]PlanOperation{{
		OpID: "one", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &one}, Content: "1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = opened.EditPlan(plan.PlanID, 1, PlanEdit{Mode: "add", Operations: []PlanOperation{{
		OpID: "two", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &two}, Content: "2",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PlanRevision != 2 || len(plan.Operations) != 2 {
		t.Fatalf("unexpected edited plan: %#v", plan)
	}
	plan, err = opened.EditPlan(plan.PlanID, 2, PlanEdit{Mode: "reorder", OpIDs: []string{"two", "one"}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err = opened.DiscardPlan(plan.PlanID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != PlanDiscarded {
		t.Fatalf("state = %s", plan.State)
	}
	identity := opened.Identity()
	reopened, err := Open(OpenOptions{
		Kind: KindProject, Root: root, StateDir: stateDir, Identity: identity.ID,
		ProviderEpoch: identity.Epoch, StateSeq: identity.StateSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != PlanDiscarded || restored.PlanRevision != 3 {
		t.Fatalf("unexpected restored plan: %#v", restored)
	}
	var actions []string
	for _, event := range restored.Events {
		actions = append(actions, event.Action)
	}
	if want := []string{"create", "edit", "edit", "discard", "inspect"}; !reflect.DeepEqual(actions, want) {
		t.Fatalf("durable lifecycle actions = %v, want %v", actions, want)
	}
	if _, err := reopened.PreviewPlan(plan.PlanID, plan.PlanRevision); err == nil {
		t.Fatal("discarded plan was previewable")
	}
}

func TestPlanRejectsDuplicateOperationsAndDependencyCycles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opened, err := Open(OpenOptions{Kind: KindProject, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := opened.NewRange("note.txt", 0, 1)
	base := PlanOperation{OpID: "same", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &target}}
	if _, err := opened.CreatePlan([]PlanOperation{base, base}); err == nil {
		t.Fatal("duplicate op_id accepted")
	}
	left := base
	left.OpID, left.DependsOn = "left", []string{"right"}
	right := base
	right.OpID, right.DependsOn = "right", []string{"left"}
	if _, err := opened.CreatePlan([]PlanOperation{left, right}); err == nil {
		t.Fatal("dependency cycle accepted")
	}
}
