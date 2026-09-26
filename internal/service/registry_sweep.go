package service

// Registry sweep. Every workspace ever opened used to stay registered, with
// its receipts, diagnostic ledger, plans, commit journals and test history,
// long after its root was deleted: git worktrees removed after a merge and
// the scratch directories of unattended agent runs accumulate by the
// hundred. The sweep forgets a workspace whose root has been gone for a
// grace period and deletes its per-workspace state, and it deletes
// per-workspace state that no registered workspace owns.
//
// A missing root is not proof of deletion: an unmounted disk, a network
// share that is down, or a directory in the middle of a move look the same
// for a while. Checking that the root's parent still exists would not tell
// them apart, because an unmounted disk leaves its empty mount point behind.
// The rule is therefore one of time: the root must be answered as not
// existing (never a permission or I/O error, which prove nothing) by every
// sweep across missingRootGrace. The first sighting is recorded in the
// registry, so a restart does not reset the clock, and a root that
// reappears clears it. Even past the grace period a workspace that anything
// still holds is kept: a stateful call in flight, a scheduler lane, a
// provider or sandbox stager, a plan being prepared or committed, or a
// commit journal that recovery has not closed.

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

const (
	// missingRootGrace is how long a root must stay gone before its
	// workspace is forgotten. A week outlasts a weekend with a disk
	// unplugged, and forgetting too early costs little anyway: the next
	// open of the root registers it again under a new ID, and only the old
	// receipts and diagnostic history are lost.
	missingRootGrace = 7 * 24 * time.Hour
	// registrySweepInterval is how often the running service sweeps.
	registrySweepInterval = time.Hour
)

// workspaceStateStores are the state directory's per-workspace stores. Each
// holds, for one workspace, a file or directory named after its ID.
var workspaceStateStores = []string{"receipts", "diagnostics", "plans", "commit-journals", "test-history"}

// workspaceStatePaths lists every path under the state directory that
// belongs to one workspace: <store>/<id>.json and <store>/<id>/ in each
// store, which covers the per-plan directory, the legacy single plan file
// and the commit journal directory alike.
func workspaceStatePaths(stateDir string, id workspacecore.ID) []string {
	paths := make([]string, 0, 2*len(workspaceStateStores))
	for _, store := range workspaceStateStores {
		paths = append(paths,
			filepath.Join(stateDir, store, string(id)+".json"),
			filepath.Join(stateDir, store, string(id)))
	}
	return paths
}

// removeWorkspaceState deletes a workspace's per-workspace state.
func removeWorkspaceState(stateDir string, id workspacecore.ID) error {
	if !workspacecore.IsID(string(id)) {
		return nil
	}
	var failures []error
	for _, path := range workspaceStatePaths(stateDir, id) {
		if err := os.RemoveAll(path); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// rootMissing reports whether the root is answered as not existing. Any
// other failure to stat it proves nothing about whether it was deleted.
func rootMissing(root string) bool {
	_, err := os.Stat(root)
	return errors.Is(err, os.ErrNotExist)
}

// markMissingRoots records when each registered root was first found gone,
// clears the mark of a root that is back, and returns the workspaces whose
// root has now been gone for longer than missingRootGrace. The roots are
// examined outside the registry lock, so a hung network mount delays the
// sweep and not every lookup.
func (r *workspaceRegistry) markMissingRoots(now time.Time) ([]workspacecore.ID, error) {
	type observed struct {
		id    workspacecore.ID
		root  string
		since *time.Time
	}
	r.mu.RLock()
	records := make([]observed, 0, len(r.records))
	for id, record := range r.records {
		records = append(records, observed{id: id, root: record.Root, since: record.MissingSince})
	}
	r.mu.RUnlock()
	var expired []workspacecore.ID
	marks := map[workspacecore.ID]*time.Time{}
	for _, record := range records {
		missing := rootMissing(record.root)
		switch {
		case !missing && record.since != nil:
			marks[record.id] = nil
		case missing && record.since == nil:
			first := now.UTC()
			marks[record.id] = &first
		case missing && now.Sub(*record.since) >= missingRootGrace:
			expired = append(expired, record.id)
		}
	}
	if len(marks) == 0 {
		return expired, nil
	}
	r.mu.Lock()
	for id, since := range marks {
		if record, ok := r.records[id]; ok {
			record.MissingSince = since
			r.records[id] = record
		}
	}
	r.mu.Unlock()
	return expired, r.persist()
}

// workspaceBusy reports whether anything still holds the workspace, in which
// case its registration and state are kept whatever became of its root.
func (d *directWorkspaces) workspaceBusy(id workspacecore.ID) bool {
	if d.receipts.workspaceInFlight(id) || d.scheduler.busy(string(id)) || d.handlers.ProviderPool().Holds(id) {
		return true
	}
	if opened := d.registry.loaded(id); opened != nil && opened.Busy() {
		return true
	}
	return workspacecore.PersistedWorkPending(d.registry.stateDir, id)
}

// sweepWorkspaceState forgets the workspaces whose root has been gone past
// the grace period and that nothing holds, deletes their state, and deletes
// the state of workspaces that are not registered at all. Every failure is
// logged and left for the next sweep; none of this may stop the service.
func (d *directWorkspaces) sweepWorkspaceState(now time.Time) {
	expired, err := d.registry.markMissingRoots(now)
	if err != nil {
		log.Printf("huyang: registry sweep: %v", err)
	}
	var forget []workspacecore.ID
	for _, id := range expired {
		if !d.workspaceBusy(id) {
			forget = append(forget, id)
		}
	}
	if err := d.registry.forget(forget); err != nil {
		log.Printf("huyang: registry sweep: forget %d workspaces: %v", len(forget), err)
		return
	}
	for _, id := range forget {
		d.dropWorkspaceState(id)
	}
	orphans := d.removeOrphanState()
	if len(forget) > 0 || orphans > 0 {
		log.Printf("huyang: registry sweep forgot %d workspaces whose root has been gone for %s and removed the state of %d unregistered ones",
			len(forget), missingRootGrace, orphans)
	}
}

// dropWorkspaceState removes what the service keeps for a workspace it no
// longer knows: its receipts in memory, its idle scheduler lanes and its
// per-workspace files.
func (d *directWorkspaces) dropWorkspaceState(id workspacecore.ID) {
	d.receipts.forgetWorkspace(id)
	d.scheduler.forget(string(id))
	if err := removeWorkspaceState(d.registry.stateDir, id); err != nil {
		log.Printf("huyang: registry sweep: remove state of %s: %v", id, err)
	}
}

// removeOrphanState deletes per-workspace state whose workspace is not
// registered, which is what a workspace forgotten by an older build, or
// state written for an ID that was never registered, leaves behind. Only
// names shaped like a workspace ID are considered, so nothing else that
// lives in those directories, such as a temporary file mid-rename, is
// touched. It returns how many workspaces' state it removed.
func (d *directWorkspaces) removeOrphanState() int {
	orphans := map[workspacecore.ID]bool{}
	for _, store := range workspaceStateStores {
		entries, err := os.ReadDir(filepath.Join(d.registry.stateDir, store))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := strings.TrimSuffix(entry.Name(), ".json")
			if workspacecore.IsID(name) && !d.registry.registered(workspacecore.ID(name)) {
				orphans[workspacecore.ID(name)] = true
			}
		}
	}
	removed := 0
	for id := range orphans {
		if d.registry.registered(id) || d.workspaceBusy(id) {
			continue
		}
		d.dropWorkspaceState(id)
		removed++
	}
	return removed
}

// startSweeper runs the registry sweep every interval until stopSweeper. It
// is idempotent. The service starts it; the in-process direct mode the tests
// use sweeps only at startup.
func (d *directWorkspaces) startSweeper(interval time.Duration) {
	d.sweepMu.Lock()
	defer d.sweepMu.Unlock()
	if d.sweepStop != nil {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	d.sweepStop, d.sweepDone = stop, done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-ticker.C:
				d.sweepWorkspaceState(now)
			}
		}
	}()
}

// stopSweeper stops the periodic sweep and waits for a sweep in progress.
func (d *directWorkspaces) stopSweeper() {
	d.sweepMu.Lock()
	defer d.sweepMu.Unlock()
	if d.sweepStop == nil {
		return
	}
	close(d.sweepStop)
	<-d.sweepDone
	d.sweepStop, d.sweepDone = nil, nil
}
