package workspace

import (
	"fmt"
	"testing"
)

// An inventory step keeps a bounded number of paths on each side and counts
// the rest, so one checkout of thousands of files does not make the log, and
// every revision_diff reply built from it, hold every path.
func TestObservedInventoryBoundsItsPaths(t *testing.T) {
	root := t.TempDir()
	ws := newNativeWorkspace(t, KindProject, root, nil, Limits{MaxFiles: 10, MaxDepth: 4, MaxBytes: 1024, MaxMatches: 10})
	previous, current := map[string]struct{}{}, map[string]struct{}{}
	for index := range maxObservedPaths + 50 {
		current[fmt.Sprintf("%s/new-%03d.txt", root, index)] = struct{}{}
	}
	previous[root+"/gone.txt"] = struct{}{}
	change := ws.observedInventory(previous, current)
	if len(change.Added) != maxObservedPaths || change.AddedOmitted != 50 {
		t.Fatalf("added %d paths, omitted %d", len(change.Added), change.AddedOmitted)
	}
	if change.Added[0] != "new-000.txt" || len(change.Removed) != 1 || change.RemovedOmitted != 0 {
		t.Fatalf("inventory step = %+v", change)
	}
}

// The log is cut back to maxObservedChanges only once it has grown
// observedSlack past it, so a full log is not copied on every step, and the
// newest steps are the ones kept.
func TestObservedLogTrimsWithSlack(t *testing.T) {
	ws := newNativeWorkspace(t, KindProject, t.TempDir(), nil, Limits{MaxFiles: 10, MaxDepth: 4, MaxBytes: 1024, MaxMatches: 10})
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.observed = nil
	observe := func() {
		ws.identity.StateSeq++
		ws.observeLocked(ObservedChange{Reason: ObservedProviderEpoch})
	}
	for range maxObservedChanges + observedSlack {
		observe()
	}
	if len(ws.observed) != maxObservedChanges+observedSlack {
		t.Fatalf("log was trimmed before its slack was spent: %d entries", len(ws.observed))
	}
	observe()
	if len(ws.observed) != maxObservedChanges {
		t.Fatalf("log past its slack holds %d entries", len(ws.observed))
	}
	if last := ws.observed[len(ws.observed)-1]; last.Seq != ws.identity.StateSeq {
		t.Fatalf("newest step was not kept: %+v", last)
	}
}
