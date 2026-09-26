package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Busy reports whether the open workspace holds work that must not be
// dropped: a plan being prepared, committed or recovered right now, a plan
// whose prepared sandbox or canonical commit is still outstanding, or a
// commit journal on disk that recovery has not closed. The workspace
// registry asks before it forgets a workspace whose root has gone.
func (w *Workspace) Busy() bool {
	w.prepareMu.Lock()
	active := len(w.activePlans) > 0
	w.prepareMu.Unlock()
	if active {
		return true
	}
	w.plansMu.Lock()
	for _, plan := range w.plans {
		if planHoldsWork(plan.State) {
			w.plansMu.Unlock()
			return true
		}
	}
	w.plansMu.Unlock()
	return PersistedWorkPending(w.stateDir, w.Identity().ID)
}

// planHoldsWork reports whether a plan in this state still owns a sandbox or
// an unfinished canonical write. Open and previewed plans are drafts that
// own neither, and terminal plans are finished.
func planHoldsWork(state PlanState) bool {
	switch state {
	case PlanPreparing, PlanProvisional, PlanReady, PlanCommitting, PlanRecoveryRequired, PlanRollingBack:
		return true
	}
	return false
}

// PersistedWorkPending reports, from the state directory alone, whether a
// workspace has work its next open must finish: a commit journal that is
// still prepared, applying or awaiting recovery, or a plan record that was
// committing or awaiting recovery. A plan that was merely prepared is not
// pending here, because opening the workspace fails it anyway: its sandbox
// died with the process that prepared it.
//
// It reads only those records, so the registry can ask about a workspace it
// has not opened. A record that cannot be read counts as pending, because
// the answer decides whether state is deleted and doubt has to keep it.
func PersistedWorkPending(stateDir string, id ID) bool {
	if stateDir == "" || !validID(id) {
		return false
	}
	journalPending := func(content []byte) bool {
		var journal struct {
			State CommitJournalState `json:"state"`
		}
		if err := json.Unmarshal(content, &journal); err != nil {
			return true
		}
		return journal.State != CommitJournalCommitted && journal.State != CommitJournalRolledBack
	}
	if recordsPending(filepath.Join(stateDir, "commit-journals", string(id)), journalPending) {
		return true
	}
	planPending := func(content []byte) bool {
		var record struct {
			Plan struct {
				State PlanState `json:"state"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(content, &record); err != nil {
			return true
		}
		return record.Plan.State == PlanCommitting || record.Plan.State == PlanRecoveryRequired
	}
	return recordsPending(filepath.Join(stateDir, "plans", string(id)), planPending)
}

// recordsPending reports whether any JSON record in directory is pending by
// the given test. A missing directory holds nothing; a directory or record
// that cannot be read is pending.
func recordsPending(directory string, pending func([]byte) bool) bool {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil || pending(content) {
			return true
		}
	}
	return false
}
