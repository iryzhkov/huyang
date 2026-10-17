package workspace

import (
	"sort"
	"time"
)

// pruneLocked applies the retention bounds. Evidence referenced by a current
// or stale item, or by a coverage dimension, is never dropped. The caller
// holds s.mu.
func (s *diagnosticStore) pruneLocked(now time.Time, retained ...string) {
	noticed := s.pruneNoticesLocked()
	s.pruneItemsLocked(now, noticed)
	s.pruneEvidenceLocked(now, retained...)
}

// pruneNoticesLocked drops acknowledged notices and the oldest beyond the cap, and
// returns the IDs the retained notices still name.
func (s *diagnosticStore) pruneNoticesLocked() map[string]bool {
	// Acknowledged notices have been consumed through diagnostics(since).
	// NoticeFloor remembers the highest dropped notice sequence so a cursor
	// older than it is told that its delta is incomplete.
	var retained []DiagnosticNotice
	dropUpTo := func(notice DiagnosticNotice) {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq > s.state.NoticeFloor {
			s.state.NoticeFloor = seq
		}
	}
	for _, notice := range s.state.Notices {
		seq, _ := parseDiagnosticCursor(notice.Cursor)
		if seq > s.state.Ack {
			retained = append(retained, notice)
		} else {
			dropUpTo(notice)
		}
	}
	if excess := len(retained) - maxDiagnosticNotices; excess > 0 {
		dropUpTo(retained[excess-1])
		retained = retained[excess:]
	}
	s.state.Notices = retained
	noticed := map[string]bool{}
	for _, notice := range s.state.Notices {
		noticed[notice.ID] = true
	}
	return noticed
}

// pruneItemsLocked keeps every current or stale item and every item a retained notice
// names; other items stay for the retention window, then up to the cap, oldest first.
func (s *diagnosticStore) pruneItemsLocked(now time.Time, noticed map[string]bool) {
	live := map[string]bool{}
	for _, ids := range s.state.Active {
		for _, id := range ids {
			live[id] = true
		}
	}
	for _, ids := range s.state.Stale {
		for _, id := range ids {
			live[id] = true
		}
	}
	var inactive []string
	for id, item := range s.state.Items {
		if live[id] || noticed[id] {
			continue
		}
		if now.Sub(item.LastSeen) > diagnosticRetentionWindow {
			delete(s.state.Items, id)
			continue
		}
		inactive = append(inactive, id)
	}
	dropOldest(inactive, len(inactive)-maxInactiveDiagnosticItems,
		func(id string) time.Time { return s.state.Items[id].LastSeen },
		func(id string) { delete(s.state.Items, id) })
}

// pruneEvidenceLocked keeps evidence an item or a dimension references; unreferenced
// evidence stays for the retention window, then up to the cap, oldest first.
func (s *diagnosticStore) pruneEvidenceLocked(now time.Time, retained ...string) {
	referenced := map[string]bool{}
	for _, id := range retained {
		referenced[id] = true
	}
	// Apply bounds to restored older ledgers too, rather than waiting for
	// each owner to receive another observation.
	for id, item := range s.state.Items {
		item.EvidenceIDs = recentEvidenceIDs(nil, item.EvidenceIDs...)
		s.state.Items[id] = item
	}
	for name, dimension := range s.state.Dimensions {
		dimension.EvidenceIDs = recentEvidenceIDs(nil, dimension.EvidenceIDs...)
		s.state.Dimensions[name] = dimension
	}
	for key, dimension := range s.state.ProviderDimensions {
		dimension.EvidenceIDs = recentEvidenceIDs(nil, dimension.EvidenceIDs...)
		s.state.ProviderDimensions[key] = dimension
	}
	for _, item := range s.state.Items {
		for _, id := range item.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, dimension := range s.state.Dimensions {
		for _, id := range dimension.EvidenceIDs {
			referenced[id] = true
		}
	}
	for _, dimension := range s.state.ProviderDimensions {
		for _, id := range dimension.EvidenceIDs {
			referenced[id] = true
		}
	}
	var unreferenced []string
	for id, evidence := range s.state.Evidence {
		if referenced[id] {
			continue
		}
		if now.Sub(evidence.RecordedAt) > diagnosticRetentionWindow {
			delete(s.state.Evidence, id)
			continue
		}
		unreferenced = append(unreferenced, id)
	}
	dropOldest(unreferenced, len(unreferenced)-maxUnreferencedDiagnosticEvidence,
		func(id string) time.Time { return s.state.Evidence[id].RecordedAt },
		func(id string) { delete(s.state.Evidence, id) })
	for key, id := range s.baselineIndex {
		if _, exists := s.state.Evidence[id]; !exists {
			delete(s.baselineIndex, key)
		}
	}
}

// dropOldest drops the excess oldest IDs, breaking equal timestamps by ID.
func dropOldest(ids []string, excess int, at func(string) time.Time, drop func(string)) {
	if excess <= 0 {
		return
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := at(ids[i]), at(ids[j])
		if left.Equal(right) {
			return ids[i] < ids[j]
		}
		return left.Before(right)
	})
	for _, id := range ids[:excess] {
		drop(id)
	}
}
