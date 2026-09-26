package workspace

import "container/list"

// maxHistoricalRevisionBytes bounds the memory one workspace spends on
// superseded revision records, the snapshots kept only so that a mutation
// guard can tell a caller that the document changed since the revision it
// holds. maxRevisionsPerDocument bounds the depth of one document's history,
// but not the number of documents: a long-lived workspace that is read file
// by file, or that crosses many provider epochs, would otherwise keep up to
// 32 records for every file it ever saw. A record costs roughly 600 bytes
// (measured with runtime.MemStats over 6400 revisions of paths shaped like
// this repository's), so 16 MiB holds about 27000 superseded revisions:
// thirteen for each of the 2000 files a workspace primes by default, or the
// full 32-deep history of some 800 files under active editing.
const maxHistoricalRevisionBytes int64 = 16 << 20

// revisionRecordOverhead approximates the fixed cost of one retained
// revision beyond its variable-length strings: the map entry, the snapshot
// struct, its slot in the per-document history and its list element.
const revisionRecordOverhead = 384

// historicalRevision is one superseded revision in the eviction order.
type historicalRevision struct {
	path  string
	id    RevisionID
	bytes int64
}

// revisionBudget orders superseded revisions from least to most recently
// superseded and tracks their estimated size. The current revision of a
// document is never in it, so the budget can only ever evict history.
type revisionBudget struct {
	limit int64
	bytes int64
	order *list.List
	index map[RevisionID]*list.Element
}

func (w *Workspace) revisionBudgetLocked() *revisionBudget {
	if w.historical == nil {
		w.historical = &revisionBudget{
			limit: maxHistoricalRevisionBytes,
			order: list.New(),
			index: make(map[RevisionID]*list.Element),
		}
	}
	return w.historical
}

func revisionRecordBytes(snapshot DocumentSnapshot) int64 {
	return int64(revisionRecordOverhead + len(snapshot.URI) + len(snapshot.Revision) +
		len(snapshot.ContentSHA256) + 48*len(snapshot.LSPVersions))
}

// retireRevisionLocked moves a document's previous current revision into the
// eviction order once a newer revision has replaced it. The caller holds w.mu.
func (w *Workspace) retireRevisionLocked(absolute string, id RevisionID) {
	snapshot, ok := w.revisions[id]
	if !ok {
		return
	}
	budget := w.revisionBudgetLocked()
	if _, already := budget.index[id]; already {
		return
	}
	entry := historicalRevision{path: absolute, id: id, bytes: revisionRecordBytes(snapshot)}
	budget.index[id] = budget.order.PushBack(entry)
	budget.bytes += entry.bytes
}

// reviveRevisionLocked takes a revision out of the eviction order, either
// because its bytes are back and it is current again or because the
// per-document bound is about to drop it. The caller holds w.mu.
func (w *Workspace) reviveRevisionLocked(id RevisionID) {
	budget := w.revisionBudgetLocked()
	element, ok := budget.index[id]
	if !ok {
		return
	}
	budget.bytes -= element.Value.(historicalRevision).bytes
	budget.order.Remove(element)
	delete(budget.index, id)
}

// enforceRevisionBudgetLocked drops the least recently superseded revisions
// until the retained history fits the budget. A dropped revision is removed
// from both the revision map and its document's history, so a guard that
// later receives it reports it as not known to this service instance, which
// is a refusal like a content change and never a false match: revision
// tokens derive from content, so only identical bytes can match again. The
// caller holds w.mu.
func (w *Workspace) enforceRevisionBudgetLocked() {
	budget := w.revisionBudgetLocked()
	for budget.bytes > budget.limit && budget.order.Len() > 0 {
		front := budget.order.Front()
		entry := front.Value.(historicalRevision)
		budget.order.Remove(front)
		delete(budget.index, entry.id)
		budget.bytes -= entry.bytes
		delete(w.revisions, entry.id)
		history := w.revisionHistory[entry.path]
		for index, id := range history {
			if id == entry.id {
				history = append(history[:index:index], history[index+1:]...)
				break
			}
		}
		if len(history) == 0 {
			delete(w.revisionHistory, entry.path)
		} else {
			w.revisionHistory[entry.path] = history
		}
	}
}

// HistoricalRevisionBytes reports the estimated size of the superseded
// revisions the workspace retains. It exists for tests and inspection of
// retention behaviour.
func (w *Workspace) HistoricalRevisionBytes() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.revisionBudgetLocked().bytes
}
