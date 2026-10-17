package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestEditPlanReplaceMatchesExternalChangeDoesNotReenterPlansLock(t *testing.T) {
	w, stateDir := evidenceWorkspace(t)
	path := filepath.Join(w.Identity().Root, "main.go")
	writeFile(t, path, "package main\nvar Red = 1\n")
	searched, err := w.Search(SearchRequest{Query: "Red", Mode: SearchLiteral, Paths: []string{"main.go"}})
	if err != nil {
		t.Fatal(err)
	}
	operation := PlanOperation{OpID: "replace-red", Kind: OperationReplaceMatches,
		Target: &PlanTarget{Handle: HandleID(searched.ResultSet.Handle)}, Content: "Green"}
	plan, err := w.CreatePlan([]PlanOperation{operation})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := w.Snapshot(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	batch := baselineBatch(path)
	batch.Baseline, batch.DocumentRevision = false, string(snapshot.Revision)
	recorded, err := w.RecordDiagnosticEvidence(batch)
	if err != nil {
		t.Fatal(err)
	}
	// Leave the native cache unchanged: EditPlan must discover this write
	// while revalidating its frozen result set under plansMu.
	writeFile(t, path, "package main\nvar Blue = 2\n")
	runBounded := func(label string, run func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- run() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not finish; possible plansMu re-entry", label)
		}
	}
	runBounded("EditPlan after external change", func() error {
		_, err := w.EditPlan(plan.PlanID, plan.PlanRevision, PlanEdit{
			Mode: "update", Operations: []PlanOperation{operation},
		})
		var conflict *Conflict
		if !errors.As(err, &conflict) || conflict.Code != ConflictDocumentChanged {
			return fmt.Errorf("expected stale result-set conflict, got %v", err)
		}
		return nil
	})
	// Check persistence before diagnostics can save the state again.
	reopened, err := newDiagnosticStore(stateDir, w.Identity().ID)
	if err != nil {
		t.Fatal(err)
	}
	if item := reopened.state.Items[recorded.Current[0].ID]; item.Status != DiagnosticStatusStale {
		t.Fatalf("content-change stale transition was not persisted: %+v", item)
	}
	runBounded("subsequent plan and diagnostics operations", func() error {
		inspected, err := w.InspectPlan(plan.PlanID, plan.PlanRevision)
		if err != nil || inspected.PlanRevision != plan.PlanRevision {
			return fmt.Errorf("plan changed after rejected edit: %+v, %v", inspected, err)
		}
		fresh, err := w.Search(SearchRequest{Query: "Blue", Mode: SearchLiteral, Paths: []string{"main.go"}})
		if err != nil {
			return err
		}
		replacement := operation
		replacement.Target = &PlanTarget{Handle: HandleID(fresh.ResultSet.Handle)}
		edited, err := w.EditPlan(plan.PlanID, plan.PlanRevision, PlanEdit{
			Mode: "update", Operations: []PlanOperation{replacement},
		})
		if err != nil || edited.PlanRevision != plan.PlanRevision+1 {
			return fmt.Errorf("fresh plan edit failed: %+v, %v", edited, err)
		}
		report, err := w.Diagnostics(recorded.Cursor)
		if err != nil || report.StaleCount != 1 || len(report.New) != 0 || len(report.Resolved) != 0 {
			return fmt.Errorf("diagnostics after plan edit: %+v, %v", report, err)
		}
		return nil
	})
}
