package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// sweepFixture registers one project workspace in a fresh state directory
// and gives it a file in every per-workspace store.
func sweepFixture(t *testing.T) (direct *directWorkspaces, stateDir, root string, id workspacecore.ID) {
	t.Helper()
	stateDir = t.TempDir()
	root = filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	direct = newDirectWorkspaces(stateDir)
	if direct.loadErr != nil {
		t.Fatal(direct.loadErr)
	}
	opened, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: root, StateDir: stateDir, ProviderEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	adopted, _, err := direct.registry.Adopt(opened, nil)
	if err != nil {
		t.Fatal(err)
	}
	id = adopted.Identity().ID
	writeWorkspaceStateFixture(t, stateDir, id)
	return direct, stateDir, root, id
}

// writeWorkspaceStateFixture writes a finished record into every store.
func writeWorkspaceStateFixture(t *testing.T, stateDir string, id workspacecore.ID) {
	t.Helper()
	writeSweepFile(t, filepath.Join(stateDir, "receipts", string(id)+".json"), `{"Version":1,"Receipts":[]}`)
	writeSweepFile(t, filepath.Join(stateDir, "diagnostics", string(id)+".json"), `{}`)
	writeSweepFile(t, filepath.Join(stateDir, "test-history", string(id)+".json"), `{}`)
	writeSweepFile(t, filepath.Join(stateDir, "plans", string(id), "plan_done.json"),
		`{"version":3,"plan":{"plan_id":"plan_done","workspace_id":"`+string(id)+`","state":"COMMITTED"}}`)
	writeSweepFile(t, filepath.Join(stateDir, "commit-journals", string(id), "plan_done.json"), `{"state":"committed"}`)
}

func writeSweepFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// existingStatePaths lists the per-workspace paths of id that exist.
func existingStatePaths(stateDir string, id workspacecore.ID) []string {
	var found []string
	for _, path := range workspaceStatePaths(stateDir, id) {
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}

func TestSweepForgetsAWorkspaceWhoseRootStayedGoneAndDeletesItsState(t *testing.T) {
	direct, stateDir, root, id := sweepFixture(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	direct.sweepWorkspaceState(now)
	if !direct.registry.registered(id) || len(existingStatePaths(stateDir, id)) != 5 {
		t.Fatal("a root seen missing once was forgotten before its grace period")
	}

	// The first sighting is durable, so a restart does not restart the clock.
	restarted := newDirectWorkspaces(stateDir)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	record := restarted.registry.records[id]
	if record.MissingSince == nil || record.MissingSince.Sub(now) > time.Second || now.Sub(*record.MissingSince) > time.Second {
		t.Fatalf("missing-since mark after restart = %v, want %v", record.MissingSince, now)
	}

	restarted.sweepWorkspaceState(now.Add(missingRootGrace + time.Minute))
	if restarted.registry.registered(id) || restarted.registry.Lookup(id) != nil {
		t.Fatal("workspace whose root stayed gone past the grace period is still registered")
	}
	if left := existingStatePaths(stateDir, id); len(left) != 0 {
		t.Fatalf("state of the forgotten workspace was left behind: %v", left)
	}
	content, err := os.ReadFile(filepath.Join(stateDir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), string(id)) {
		t.Fatal("registry.json still names the forgotten workspace")
	}
}

func TestSweepClearsTheMarkOfARootThatCameBack(t *testing.T) {
	direct, stateDir, root, id := sweepFixture(t)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	direct.sweepWorkspaceState(now)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	direct.sweepWorkspaceState(now.Add(time.Hour))
	if direct.registry.records[id].MissingSince != nil {
		t.Fatal("a root that came back is still marked missing")
	}
	// Gone again: the grace period starts over from the new sighting.
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	direct.sweepWorkspaceState(now.Add(missingRootGrace))
	direct.sweepWorkspaceState(now.Add(missingRootGrace + time.Hour))
	if !direct.registry.registered(id) || len(existingStatePaths(stateDir, id)) != 5 {
		t.Fatal("workspace forgotten on the strength of a sighting from before its root came back")
	}
}

func TestSweepKeepsABusyWorkspaceWhoseRootIsGone(t *testing.T) {
	cases := map[string]func(t *testing.T, direct *directWorkspaces, stateDir string, id workspacecore.ID) (release func()){
		"stateful call in flight": func(t *testing.T, direct *directWorkspaces, _ string, id workspacecore.ID) func() {
			key := string(id) + "\x00edit_apply\x00key"
			_, pending := direct.receipts.lookupOrBegin(key, "hash")
			return func() { direct.receipts.abandon(key, pending) }
		},
		"scheduler lane held": func(t *testing.T, direct *directWorkspaces, _ string, id workspacecore.ID) func() {
			release, err := direct.scheduler.acquire(context.Background(), string(id), mcpapi.ClassCanonicalWrite)
			if err != nil {
				t.Fatal(err)
			}
			return release
		},
		"commit journal awaiting recovery": func(t *testing.T, _ *directWorkspaces, stateDir string, id workspacecore.ID) func() {
			path := filepath.Join(stateDir, "commit-journals", string(id), "plan_open.json")
			writeSweepFile(t, path, `{"state":"applying"}`)
			return func() { writeSweepFile(t, path, `{"state":"rolled_back"}`) }
		},
		"plan committing": func(t *testing.T, _ *directWorkspaces, stateDir string, id workspacecore.ID) func() {
			path := filepath.Join(stateDir, "plans", string(id), "plan_open.json")
			writeSweepFile(t, path, `{"version":3,"plan":{"plan_id":"plan_open","state":"COMMITTING"}}`)
			return func() { writeSweepFile(t, path, `{"version":3,"plan":{"plan_id":"plan_open","state":"FAILED"}}`) }
		},
	}
	for name, hold := range cases {
		t.Run(name, func(t *testing.T) {
			direct, stateDir, root, id := sweepFixture(t)
			release := hold(t, direct, stateDir, id)
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			direct.sweepWorkspaceState(now)
			direct.sweepWorkspaceState(now.Add(missingRootGrace + time.Minute))
			if !direct.registry.registered(id) || len(existingStatePaths(stateDir, id)) != 5 {
				t.Fatal("a workspace that is still held was forgotten")
			}
			release()
			direct.sweepWorkspaceState(now.Add(missingRootGrace + 2*time.Minute))
			if direct.registry.registered(id) {
				t.Fatal("workspace still registered once nothing held it")
			}
		})
	}
}

func TestStartupDeletesOrphanWorkspaceStateOnly(t *testing.T) {
	direct, stateDir, _, kept := sweepFixture(t)
	direct.closeProviders()
	orphan := workspacecore.ID("ws_" + strings.Repeat("ab", 16))
	held := workspacecore.ID("ws_" + strings.Repeat("cd", 16))
	writeWorkspaceStateFixture(t, stateDir, orphan)
	writeSweepFile(t, filepath.Join(stateDir, "commit-journals", string(held), "plan_open.json"), `{"state":"recovery_required"}`)
	bystanders := []string{
		filepath.Join(stateDir, "diagnostics", "notes.json"),
		filepath.Join(stateDir, "receipts", "."+string(orphan)+".json-123.tmp"),
	}
	for _, path := range bystanders {
		writeSweepFile(t, path, `{}`)
	}

	restarted := newDirectWorkspaces(stateDir)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	if left := existingStatePaths(stateDir, orphan); len(left) != 0 {
		t.Fatalf("orphan state was left behind: %v", left)
	}
	if len(existingStatePaths(stateDir, kept)) != 5 {
		t.Fatal("state of a registered workspace was deleted as an orphan")
	}
	if len(existingStatePaths(stateDir, held)) != 1 {
		t.Fatal("an orphan commit journal awaiting recovery was deleted")
	}
	for _, path := range bystanders {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s is not per-workspace state and was removed: %v", path, err)
		}
	}
}

func TestRegisteredWorkspacesOpenOnFirstUse(t *testing.T) {
	direct, stateDir, _, id := sweepFixture(t)
	direct.closeProviders()
	// The fixture's journal is only a state marker, not one recovery can
	// read, and this test opens the workspace for real.
	if err := os.RemoveAll(filepath.Join(stateDir, "commit-journals", string(id))); err != nil {
		t.Fatal(err)
	}
	pendingRoot := t.TempDir()
	opened, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: pendingRoot, StateDir: stateDir, ProviderEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := direct.registry.Adopt(opened, nil)
	if err != nil {
		t.Fatal(err)
	}
	pendingID := pending.Identity().ID
	plan, err := json.Marshal(map[string]any{"version": 3, "plan": map[string]any{
		"plan_id": "plan_interrupted", "workspace_id": pendingID, "state": "COMMITTING",
	}})
	if err != nil {
		t.Fatal(err)
	}
	writeSweepFile(t, filepath.Join(stateDir, "plans", string(pendingID), "plan_interrupted.json"), string(plan))

	restarted := newDirectWorkspaces(stateDir)
	if restarted.loadErr != nil {
		t.Fatal(restarted.loadErr)
	}
	if restarted.registry.loaded(id) != nil {
		t.Fatal("a workspace with nothing to recover was opened at startup")
	}
	if restarted.registry.loaded(pendingID) == nil {
		t.Fatal("a workspace with an interrupted commit was not opened, and so not recovered, at startup")
	}
	first := restarted.registry.Lookup(id)
	if first == nil || first.Identity().ID != id || restarted.registry.Lookup(id) != first {
		t.Fatal("lookup did not open the registered workspace exactly once")
	}
	reopened, err := workspacecore.Open(workspacecore.OpenOptions{Kind: workspacecore.KindProject, Root: first.Identity().Root, StateDir: stateDir, ProviderEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	adopted, created, err := restarted.registry.Adopt(reopened, nil)
	if err != nil || created || adopted != first {
		t.Fatalf("reopening the same root adopted %v created=%v err=%v, want the registered workspace", adopted, created, err)
	}
}
