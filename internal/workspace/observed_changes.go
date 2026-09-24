package workspace

import (
	"path/filepath"
	"sort"
	"strings"
)

// maxObservedChanges bounds the log of observed state-sequence steps. The log
// only has to reach back as far as a caller is likely to ask revision_diff
// about, and older steps simply stop being attributable.
const maxObservedChanges = 4096

// observedSlack is how far the log may grow past maxObservedChanges before it
// is cut back, so a full log is copied once every observedSlack steps rather
// than on every step.
const observedSlack = 512

// maxObservedPaths bounds the paths an inventory step keeps on each side. A
// checkout or a generator can add thousands of files in one step and the log
// holds thousands of steps; past the bound a step keeps only the count.
const maxObservedPaths = 100

// Reasons an observed step advanced the state sequence.
const (
	ObservedDocument      = "document"
	ObservedInventory     = "inventory"
	ObservedProviderEpoch = "provider_epoch"
)

// ObservedChange is one state-sequence step the workspace advanced because it
// observed something, rather than because it wrote something: a document
// whose bytes or metadata changed on disk, files that appeared or went away,
// or a new provider epoch. Native edits are observed too, since the write is
// seen by the next snapshot, but their receipts say more. What an observation
// can say is limited to what was seen: the content hashes on either side and
// the paths, never the bytes that were there before.
type ObservedChange struct {
	Seq    uint64 `json:"seq"`
	Reason string `json:"reason"`
	// Path, BeforeSHA256 and AfterSHA256 describe a document step. An empty
	// hash is a document that did not exist on that side.
	Path         string `json:"path,omitempty"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	// Added and Removed describe an inventory step, each holding at most
	// maxObservedPaths paths; AddedOmitted and RemovedOmitted count the rest.
	Added          []string `json:"added,omitempty"`
	Removed        []string `json:"removed,omitempty"`
	AddedOmitted   int      `json:"added_omitted,omitempty"`
	RemovedOmitted int      `json:"removed_omitted,omitempty"`
}

// ObservedChanges returns the observed steps whose sequence lies in
// (fromSeq, toSeq], oldest first. A step the log no longer holds, or never
// held because it happened before this process started, is simply absent;
// the caller decides what an absent step means.
func (w *Workspace) ObservedChanges(fromSeq, toSeq uint64) []ObservedChange {
	w.mu.Lock()
	defer w.mu.Unlock()
	var changes []ObservedChange
	for _, change := range w.observed {
		if change.Seq > fromSeq && change.Seq <= toSeq {
			change.Added = append([]string(nil), change.Added...)
			change.Removed = append([]string(nil), change.Removed...)
			changes = append(changes, change)
		}
	}
	return changes
}

// observeLocked records the step that has just advanced the state sequence.
// The caller holds w.mu and has already incremented StateSeq.
func (w *Workspace) observeLocked(change ObservedChange) {
	change.Seq = w.identity.StateSeq
	w.observed = append(w.observed, change)
	if len(w.observed) > maxObservedChanges+observedSlack {
		w.observed = append([]ObservedChange(nil), w.observed[len(w.observed)-maxObservedChanges:]...)
	}
}

// observedPath is the workspace-relative spelling of an absolute path, which is
// how every reply names a file; a path outside the root keeps its absolute form.
func (w *Workspace) observedPath(absolute string) string {
	relative, err := filepath.Rel(w.identity.Root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return absolute
	}
	return filepath.ToSlash(relative)
}

// observedInventory lists the paths that appeared and went away between two
// inventories, each sorted, workspace-relative and bounded by
// maxObservedPaths.
func (w *Workspace) observedInventory(previous, current map[string]struct{}) ObservedChange {
	change := ObservedChange{Reason: ObservedInventory}
	for path := range current {
		if _, known := previous[path]; !known {
			change.Added = append(change.Added, w.observedPath(path))
		}
	}
	for path := range previous {
		if _, exists := current[path]; !exists {
			change.Removed = append(change.Removed, w.observedPath(path))
		}
	}
	sort.Strings(change.Added)
	sort.Strings(change.Removed)
	change.Added, change.AddedOmitted = boundObservedPaths(change.Added)
	change.Removed, change.RemovedOmitted = boundObservedPaths(change.Removed)
	return change
}

// boundObservedPaths keeps the first maxObservedPaths paths and counts the rest.
func boundObservedPaths(paths []string) ([]string, int) {
	if len(paths) <= maxObservedPaths {
		return paths, 0
	}
	return paths[:maxObservedPaths:maxObservedPaths], len(paths) - maxObservedPaths
}
