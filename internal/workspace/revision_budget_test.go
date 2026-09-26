package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// limitHistoricalRevisions shrinks the workspace's historical revision budget
// so a test can overflow it with a handful of files.
func limitHistoricalRevisions(ws *Workspace, limit int64) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.revisionBudgetLocked().limit = limit
}

func refreshWith(t *testing.T, ws *Workspace, path, content string) DocumentSnapshot {
	t.Helper()
	writeFile(t, path, content)
	snapshot, err := ws.Refresh(path, ProviderLayer{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestHistoricalRevisionBudgetEvictsOldestSupersededFirst(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "probe.txt")
	probe := refreshWith(t, ws, path, "probe 0\n")
	limit := 4 * revisionRecordBytes(probe)
	limitHistoricalRevisions(ws, limit)

	current := make(map[string]DocumentSnapshot)
	var first []DocumentSnapshot
	for version := 0; version < 3; version++ {
		for index := 0; index < 5; index++ {
			file := filepath.Join(root, fmt.Sprintf("file%d.txt", index))
			snapshot := refreshWith(t, ws, file, fmt.Sprintf("file %d version %d\n", index, version))
			if version == 0 {
				first = append(first, snapshot)
			}
			current[file] = snapshot
		}
	}
	if got := ws.HistoricalRevisionBytes(); got > limit || got <= 0 {
		t.Fatalf("historical revision bytes = %d, want within (0, %d]", got, limit)
	}
	ws.mu.Lock()
	for file, snapshot := range current {
		if _, ok := ws.revisions[snapshot.Revision]; !ok {
			t.Errorf("current revision of %s was evicted", file)
		}
	}
	_, oldestRetained := ws.revisions[first[0].Revision]
	ws.mu.Unlock()
	if oldestRetained {
		t.Fatal("the least recently superseded revision survived an overflowing budget")
	}
	for file, snapshot := range current {
		if _, err := ws.ValidateMutation(ws.Identity().ID, file, snapshot.Revision, ProviderLayer{}); err != nil {
			t.Fatalf("current revision of %s no longer validates: %v", file, err)
		}
	}
}

func TestEvictedRevisionIsRefusedAsUnknownAndNeverMatches(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "doc.txt")
	evicted := refreshWith(t, ws, path, "one\n")
	limitHistoricalRevisions(ws, revisionRecordBytes(evicted))
	retained := refreshWith(t, ws, path, "two\n")
	refreshWith(t, ws, path, "three\n")

	_, err := ws.ValidateMutation(ws.Identity().ID, path, evicted.Revision, ProviderLayer{})
	var conflict *Conflict
	if !errors.As(err, &conflict) || conflict.Code != ConflictWorkspaceEpoch || conflict.Detail != unknownRevisionDetail {
		t.Fatalf("evicted revision: got %v, want an unknown-revision refusal", err)
	}
	_, err = ws.ValidateMutation(ws.Identity().ID, path, retained.Revision, ProviderLayer{})
	if got := conflictCode(t, err); got != ConflictDocumentChanged {
		t.Fatalf("retained superseded revision: got %s, want %s", got, ConflictDocumentChanged)
	}

	// Revision tokens derive from content, so once the evicted bytes are
	// back the revision is current again and validates as it should.
	writeFile(t, path, "one\n")
	if _, err := ws.ValidateMutation(ws.Identity().ID, path, evicted.Revision, ProviderLayer{}); err != nil {
		t.Fatalf("restored content did not validate its revision: %v", err)
	}
}

func TestHistoricalRevisionBudgetTracksPerDocumentPruning(t *testing.T) {
	ws, root := testWorkspace(t)
	path := filepath.Join(root, "churn.txt")
	for index := 0; index < maxRevisionsPerDocument*2; index++ {
		refreshWith(t, ws, path, fmt.Sprintf("content %d\n", index))
	}
	ws.mu.Lock()
	defer ws.mu.Unlock()
	history := ws.revisionHistory[path]
	var want int64
	for _, id := range history[:len(history)-1] {
		want += revisionRecordBytes(ws.revisions[id])
	}
	budget := ws.revisionBudgetLocked()
	if budget.bytes != want || budget.order.Len() != len(history)-1 || len(budget.index) != len(history)-1 {
		t.Fatalf("budget holds %d bytes over %d entries, want %d bytes over %d", budget.bytes, budget.order.Len(), want, len(history)-1)
	}
	if _, tracked := budget.index[history[len(history)-1]]; tracked {
		t.Fatal("the current revision is tracked as evictable history")
	}
}
