package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fakePlanStager struct {
	epoch      uint64
	buffers    map[string][]byte
	preimages  map[string][]byte
	failAfter  int
	cancel     bool
	rollbacks  int
	commits    int
	afterStage func()
}

func (f *fakePlanStager) Epoch() uint64 { return f.epoch }

func (f *fakePlanStager) Stage(ctx context.Context, request PlanStageRequest) error {
	f.preimages = make(map[string][]byte)
	for index, file := range request.Files {
		if !bytes.Equal(f.buffers[file.Path], file.Before) {
			return errors.New("provider_preimage_changed")
		}
		f.preimages[file.Path] = append([]byte(nil), f.buffers[file.Path]...)
		if file.AfterExists {
			f.buffers[file.Path] = append([]byte(nil), file.After...)
		} else {
			delete(f.buffers, file.Path)
		}
		if f.cancel && index == 0 {
			<-ctx.Done()
			return ctx.Err()
		}
		if f.failAfter == index+1 {
			return errors.New("provider_died")
		}
	}
	if f.afterStage != nil {
		f.afterStage()
	}
	return nil
}

func (f *fakePlanStager) Commit(_ context.Context, _ string) error {
	f.commits++
	return nil
}

func (f *fakePlanStager) Rollback(_ context.Context, _ string) error {
	f.rollbacks++
	for path, content := range f.preimages {
		f.buffers[path] = append([]byte(nil), content...)
	}
	return nil
}

func prepareFixture(t *testing.T) (*Workspace, string, PlanRecord, *fakePlanStager) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "alpha\n")
	writeFile(t, filepath.Join(root, "b.txt"), "beta\n")
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{})
	a, err := ws.NewRange("a.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ws.NewRange("b.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ws.CreatePlan([]PlanOperation{
		{OpID: "a", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &a}, Content: "ALPHA"},
		{OpID: "b", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &b}, Content: "BETA"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stager := &fakePlanStager{epoch: 7, buffers: map[string][]byte{"a.txt": []byte("alpha\n"), "b.txt": []byte("beta\n")}}
	return ws, root, plan, stager
}

func TestExclusivePrepareStagesUnsavedAndRollbackRestoresExactPreimages(t *testing.T) {
	ws, root, plan, stager := prepareFixture(t)
	prepared, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != PlanReady || prepared.Preparation == nil {
		t.Fatalf("unexpected prepared state: %+v", prepared)
	}
	if prepared.Preparation.CanonicalChanged || prepared.Preparation.Diagnostics != "suppressed" ||
		prepared.Preparation.DiskChecks != "unavailable_buffer_backed_prepare" ||
		prepared.Preparation.IntermediateReports {
		t.Fatalf("dishonest preparation evidence: %+v", prepared.Preparation)
	}
	if string(stager.buffers["a.txt"]) != "ALPHA\n" || string(stager.buffers["b.txt"]) != "BETA\n" {
		t.Fatalf("provider buffers were not staged: %#v", stager.buffers)
	}
	if err := ws.CheckProviderAccess("other"); err != nil {
		t.Fatalf("canonical provider access should remain independent of sandbox staging: %v", err)
	}
	if err := ws.CheckProviderAccess(plan.PlanID); err != nil {
		t.Fatalf("transaction provider access was blocked: %v", err)
	}
	for path, want := range map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"} {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(content) != want {
			t.Fatalf("canonical %s changed before commit: %q, %v", path, content, err)
		}
	}
	rolled, err := ws.RollbackPlan(context.Background(), plan.PlanID, plan.PlanRevision, stager)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.State != PlanRolledBack || stager.rollbacks != 1 {
		t.Fatalf("rollback state=%s calls=%d", rolled.State, stager.rollbacks)
	}
	if string(stager.buffers["a.txt"]) != "alpha\n" || string(stager.buffers["b.txt"]) != "beta\n" {
		t.Fatalf("rollback did not restore exact preimages: %#v", stager.buffers)
	}
	if err := ws.CheckProviderAccess("other"); err != nil {
		t.Fatalf("lease was not released: %v", err)
	}
}

func TestDisjointPlansPrepareConcurrentlyWithoutBlockingCanonicalReads(t *testing.T) {
	ws, root, _, _ := prepareFixture(t)
	a, err := ws.NewRange("a.txt", 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ws.NewRange("b.txt", 0, 4)
	if err != nil {
		t.Fatal(err)
	}
	planA, err := ws.CreatePlan([]PlanOperation{{OpID: "a-only", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &a}, Content: "A"}})
	if err != nil {
		t.Fatal(err)
	}
	planB, err := ws.CreatePlan([]PlanOperation{{OpID: "b-only", Kind: OperationReplaceRange, Target: &PlanTarget{FileRange: &b}, Content: "B"}})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	block := func() { entered <- struct{}{}; <-release }
	stagerA := &fakePlanStager{epoch: 1, buffers: map[string][]byte{"a.txt": []byte("alpha\n")}, afterStage: block}
	stagerB := &fakePlanStager{epoch: 1, buffers: map[string][]byte{"b.txt": []byte("beta\n")}, afterStage: block}
	results := make(chan error, 2)
	go func() {
		_, err := ws.PreparePlan(context.Background(), planA.PlanID, planA.PlanRevision, stagerA)
		results <- err
	}()
	go func() {
		_, err := ws.PreparePlan(context.Background(), planB.PlanID, planB.PlanRevision, stagerB)
		results <- err
	}()
	<-entered
	<-entered
	if content, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(content) != "alpha\n" {
		t.Fatalf("canonical read changed during parallel prepare: %q, %v", content, err)
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareFaultsAndCancellationRestoreEveryBufferAndReleaseLease(t *testing.T) {
	for _, test := range []struct {
		name      string
		failAfter int
		cancel    bool
	}{
		{name: "first_apply", failAfter: 1},
		{name: "second_apply", failAfter: 2},
		{name: "cancelled", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ws, root, plan, stager := prepareFixture(t)
			stager.failAfter, stager.cancel = test.failAfter, test.cancel
			ctx := context.Background()
			if test.cancel {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			if _, err := ws.PreparePlan(ctx, plan.PlanID, plan.PlanRevision, stager); err == nil {
				t.Fatal("prepare unexpectedly succeeded")
			}
			if stager.rollbacks != 1 || string(stager.buffers["a.txt"]) != "alpha\n" ||
				string(stager.buffers["b.txt"]) != "beta\n" {
				t.Fatalf("fault leaked staged buffers: calls=%d buffers=%#v", stager.rollbacks, stager.buffers)
			}
			if err := ws.CheckProviderAccess("other"); err != nil {
				t.Fatalf("fault retained lease: %v", err)
			}
			for path, want := range map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"} {
				content, _ := os.ReadFile(filepath.Join(root, path))
				if string(content) != want {
					t.Fatalf("fault changed disk %s: %q", path, content)
				}
			}
		})
	}
}

func TestPrepareFinalTransitionFailureRollsProviderBack(t *testing.T) {
	ws, _, plan, stager := prepareFixture(t)
	plansDir := filepath.Join(ws.stateDir, "plans")
	stager.afterStage = func() {
		if err := os.Rename(plansDir, plansDir+".saved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plansDir, []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err == nil {
		t.Fatal("prepare unexpectedly survived final durable transition failure")
	}
	if stager.rollbacks != 1 || string(stager.buffers["a.txt"]) != "alpha\n" ||
		string(stager.buffers["b.txt"]) != "beta\n" {
		t.Fatalf("final transition failure leaked buffers: calls=%d buffers=%#v", stager.rollbacks, stager.buffers)
	}
	if err := ws.CheckProviderAccess("other"); err != nil {
		t.Fatalf("final transition failure retained lease: %v", err)
	}
}

func TestRestartInvalidatesPreparedProviderView(t *testing.T) {
	ws, _, plan, stager := prepareFixture(t)
	if _, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err != nil {
		t.Fatal(err)
	}
	identity := ws.Identity()
	restarted, err := Open(OpenOptions{
		Kind: identity.Kind, Root: identity.Root, ProviderEpoch: identity.Epoch,
		StateDir: ws.stateDir, Identity: identity.ID, StateSeq: identity.StateSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := restarted.InspectPlan(plan.PlanID, plan.PlanRevision)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != PlanFailed || restored.Preparation != nil {
		t.Fatalf("restart retained vanished provider preparation: %+v", restored)
	}
	if err := restarted.CheckProviderAccess("other"); err != nil {
		t.Fatalf("restart restored a stale lease: %v", err)
	}
}

func TestPrepareStateTransitionFailureDoesNotStageOrRetainLease(t *testing.T) {
	ws, _, plan, stager := prepareFixture(t)
	plansDir := filepath.Join(ws.stateDir, "plans")
	if err := os.Rename(plansDir, plansDir+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plansDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, stager); err == nil {
		t.Fatal("prepare unexpectedly survived durable transition failure")
	}
	if len(stager.preimages) != 0 {
		t.Fatalf("provider was touched before PREPARING became durable: %#v", stager.preimages)
	}
	if err := ws.CheckProviderAccess("other"); err != nil {
		t.Fatalf("transition failure retained lease: %v", err)
	}
}
