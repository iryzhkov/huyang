package providerpool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/provider"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// expiryFactory opens stub providers that identify themselves, which staged
// diagnostics require of the provider that reports them.
type expiryFactory struct{ reapFactory }

func (f *expiryFactory) Open(c OpenConfig) (provider.Provider, error) {
	opened, err := f.reapFactory.Open(c)
	stub := opened.(*reapProvider)
	stub.descriptor.ID = provider.ID(fmt.Sprintf("stub_%d", len(f.opened)))
	stub.descriptor.Backend = "stub"
	return stub, err
}

// expiryFixture is a pool on a fake clock that starts at the real time, since
// plan records are stamped with it, and one project workspace with one file.
func expiryFixture(t *testing.T) (*Pool, *workspacecore.Workspace, *expiryFactory, func(time.Duration)) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := workspacecore.Open(workspacecore.OpenOptions{
		Kind: workspacecore.KindProject, Root: root, ProviderEpoch: 1, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &expiryFactory{}
	p := New(t.TempDir(), f)
	p.planIdleTTL = time.Hour
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	p.now = func() time.Time { return time.Unix(0, clock.Load()) }
	t.Cleanup(p.Close)
	return p, w, f, func(d time.Duration) { clock.Add(int64(d)) }
}

// prepareThroughPool prepares a plan that creates one file, the way change_plan does.
func prepareThroughPool(t *testing.T, p *Pool, w *workspacecore.Workspace) (workspacecore.PlanRecord, *SandboxStager) {
	t.Helper()
	plan, err := w.CreatePlan([]workspacecore.PlanOperation{{
		OpID: "create", Kind: workspacecore.OperationCreateFile, Path: "new.txt", Content: "new\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.PlanStager(w, plan.PlanID, plan.PlanRevision, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := w.PreparePlan(context.Background(), plan.PlanID, plan.PlanRevision, raw)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.State != workspacecore.PlanReady && prepared.State != workspacecore.PlanProvisional {
		t.Fatalf("prepared state = %s", prepared.State)
	}
	return prepared, raw.(*SandboxStager)
}

func TestIdlePreparedPlanReleasesSandboxProviderAndStager(t *testing.T) {
	p, w, f, advance := expiryFixture(t)
	prepared, stager := prepareThroughPool(t, p, w)
	tree := stager.Sandbox().Tree
	// A prepared plan holds its sandbox and the diagnostics provider, which
	// is what makes an abandoned one a leak.
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("sandbox missing after prepare: %v", err)
	}
	if len(f.opened) != 2 || f.opened[0].closes.Load() != 1 || f.opened[1].closes.Load() != 0 {
		t.Fatalf("after prepare: %d providers opened, want the staging one closed and the diagnostics one open", len(f.opened))
	}
	advance(59 * time.Minute)
	if n := p.ExpireIdlePlans(); n != 0 {
		t.Fatalf("expired %d plans before the limit", n)
	}
	if p.Stager(w.Identity().ID, prepared.PlanID) == nil {
		t.Fatal("stager dropped before the limit")
	}
	// The lookup above is use: the hour starts again from it.
	advance(59 * time.Minute)
	if n := p.ExpireIdlePlans(); n != 0 {
		t.Fatal("a plan whose preparation was just looked up expired")
	}
	advance(2 * time.Minute)
	if n := p.ExpireIdlePlans(); n != 1 {
		t.Fatalf("expired %d plans, want 1", n)
	}
	if plan, err := w.InspectPlan(prepared.PlanID, 0); err != nil || plan.State != workspacecore.PlanExpired {
		t.Fatalf("plan after expiry = %#v, %v", plan, err)
	}
	if f.opened[1].closes.Load() != 1 {
		t.Fatal("diagnostics provider of the expired plan is still open")
	}
	if _, err := os.Stat(tree); !os.IsNotExist(err) {
		t.Fatalf("sandbox of the expired plan remains: %v", err)
	}
	p.mu.Lock()
	remaining := len(p.stagers)
	p.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d stagers left in the map", remaining)
	}
	// apply looks the stager up first; there is none, and the plan says why.
	if _, err := p.PlanStager(w, prepared.PlanID, prepared.PlanRevision, false); err == nil {
		t.Fatal("an expired plan still has a stager to apply from")
	}
	if _, err := w.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager); workspacecore.ErrorCode(err) != workspacecore.CodePlanStateInvalid {
		t.Fatalf("apply of an expired plan = %v", err)
	}
}

func TestFinishedStagersLeaveTheMap(t *testing.T) {
	p, w, f, advance := expiryFixture(t)
	prepared, stager := prepareThroughPool(t, p, w)
	committed, err := w.CommitPlan(context.Background(), prepared.PlanID, prepared.PlanRevision,
		prepared.Preparation.PreparedRevision, stager, workspacecore.AcceptProvisional("diagnostics"))
	if err != nil || committed.State != workspacecore.PlanCommitted {
		t.Fatalf("commit = %#v, %v", committed, err)
	}
	if f.opened[1].closes.Load() != 1 {
		t.Fatal("provider of an applied plan is still open")
	}
	if n := p.ExpireIdlePlans(); n != 0 || p.Stager(w.Identity().ID, prepared.PlanID) == nil {
		t.Fatal("a just-finished stager was dropped inside its grace")
	}
	advance(stagerReleaseGrace)
	p.ExpireIdlePlans()
	if p.Stager(w.Identity().ID, prepared.PlanID) != nil {
		t.Fatal("the stager of an applied plan stayed in the map")
	}
}

func TestUnpreparedPlansAreSweptThroughTheWorkspaceSource(t *testing.T) {
	p, w, _, advance := expiryFixture(t)
	plan, err := w.CreatePlan([]workspacecore.PlanOperation{{
		OpID: "create", Kind: workspacecore.OperationCreateFile, Path: "new.txt", Content: "new\n",
	}})
	if err != nil {
		t.Fatal(err)
	}
	advance(2 * time.Hour)
	if n := p.ExpireIdlePlans(); n != 0 {
		t.Fatal("swept a workspace nobody listed")
	}
	p.SetWorkspaceSource(func() []*workspacecore.Workspace { return []*workspacecore.Workspace{w} })
	if n := p.ExpireIdlePlans(); n != 1 {
		t.Fatalf("expired %d plans, want 1", n)
	}
	if current, err := w.InspectPlan(plan.PlanID, 0); err != nil || current.State != workspacecore.PlanExpired {
		t.Fatalf("plan = %#v, %v", current, err)
	}
}
