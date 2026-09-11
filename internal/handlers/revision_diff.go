package handlers

import (
	"errors"
	"fmt"
	"sort"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// revisionRange is the decoded, validated revision_diff request: both
// endpoints as tokens and as sequence numbers, with "current" resolved.
type revisionRange struct {
	from, to, current string
	fromSeq, toSeq    uint64
}

// revisionCoverage is what the native receipts say about a revision range:
// the ordered segments they cover and the gaps between them.
type revisionCoverage struct {
	known    []RecordedRevisionDiff
	segments []any
	gaps     []any
}

func (h *Handlers) revisionDiff(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if err := workspace.PrimeDocuments(); err != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if _, err := workspace.RefreshKnownDocuments(); err != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if err := h.registry.PersistIdentity(workspace.Identity().ID); err != nil {
		return mcpapi.Failure(requestID, workspace, "service_state_persist_failed", err)
	}
	span, failure := decodeRevisionRange(requestID, workspace, arguments)
	if failure != nil {
		return failure
	}
	recorded := h.provenance.RecordedRevisionDiffs(string(workspace.Identity().ID), span.fromSeq, span.toSeq)
	coverage := coverRevisionRange(recorded, span)
	if len(coverage.gaps) > 0 {
		return partialRevisionDiff(requestID, workspace, span, coverage)
	}
	return completeRevisionDiff(requestID, workspace, span, coverage.known)
}

// decodeRevisionRange validates the requested range against the workspace:
// both endpoints must be ordered wsrev_N tokens and the target must not be
// newer than the workspace.
func decodeRevisionRange(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) (revisionRange, map[string]any) {
	span := revisionRange{
		from:    fmt.Sprint(arguments["from_revision"]),
		to:      fmt.Sprint(arguments["to_revision_or_current"]),
		current: fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
	}
	if span.to == "current" {
		span.to = span.current
	}
	fromSeq, fromErr := RevisionSequence(span.from)
	toSeq, toErr := RevisionSequence(span.to)
	if fromErr != nil || toErr != nil || fromSeq > toSeq {
		return span, mcpapi.Envelope(requestID, workspace, "failed", "invalid_revision_range", "revision_diff requires an ordered wsrev_N range", map[string]any{"from_revision": span.from, "to_revision": span.to})
	}
	if toSeq > workspace.Identity().StateSeq {
		return span, mcpapi.Envelope(requestID, workspace, "conflict", "revision_changed", "Requested target revision is newer than the workspace", map[string]any{"current_revision": span.current})
	}
	span.fromSeq, span.toSeq = fromSeq, toSeq
	return span, nil
}

// coverRevisionRange walks the recorded receipts in revision order and
// splits the range into covered segments and uncovered gaps. Receipts for
// one revision step may name several paths; a receipt that starts before
// the cursor and is not part of the current step is skipped as redundant.
func coverRevisionRange(recorded []RecordedRevisionDiff, span revisionRange) revisionCoverage {
	sort.Slice(recorded, func(i, j int) bool {
		if recorded[i].From != recorded[j].From {
			return recorded[i].From < recorded[j].From
		}
		if recorded[i].To != recorded[j].To {
			return recorded[i].To < recorded[j].To
		}
		return recorded[i].Path < recorded[j].Path
	})
	coverage := revisionCoverage{
		known: make([]RecordedRevisionDiff, 0, len(recorded)), segments: make([]any, 0, len(recorded)), gaps: make([]any, 0),
	}
	cursor := span.fromSeq
	for _, item := range recorded {
		last := len(coverage.known) - 1
		sameRevision := last >= 0 && coverage.known[last].From == item.From && coverage.known[last].To == item.To
		if item.To <= cursor {
			if !sameRevision {
				continue
			}
		} else if item.From < cursor {
			continue
		}
		if item.From > cursor {
			coverage.gaps = append(coverage.gaps, revisionSpan(cursor, item.From))
		}
		coverage.known = append(coverage.known, item)
		segment := revisionSpan(item.From, item.To)
		segment["path"] = item.Path
		coverage.segments = append(coverage.segments, segment)
		if item.To > cursor {
			cursor = item.To
		}
	}
	if cursor < span.toSeq {
		coverage.gaps = append(coverage.gaps, revisionSpan(cursor, span.toSeq))
	}
	return coverage
}

func revisionSpan(from, to uint64) map[string]any {
	return map[string]any{
		"from_revision": fmt.Sprintf("wsrev_%d", from),
		"to_revision":   fmt.Sprintf("wsrev_%d", to),
	}
}

// partialRevisionDiff reports the known segments alongside the explicit
// gaps; nothing is inferred for the gaps.
func partialRevisionDiff(requestID string, workspace *workspacecore.Workspace, span revisionRange, coverage revisionCoverage) map[string]any {
	diffs := make([]any, 0, len(coverage.known))
	paths := make([]string, 0, len(coverage.known))
	for _, item := range coverage.known {
		diffs = append(diffs, mcpapi.CompactRevisionDiff(item.Diff))
		if item.Path != "" {
			paths = append(paths, item.Path)
		}
	}
	sort.Strings(paths)
	paths = mcpapi.UniqueStrings(paths)
	result := mcpapi.Envelope(requestID, workspace, "partial", "diff_evidence_incomplete", "Known native edit segments are returned with explicit uncovered revision gaps", map[string]any{
		"from_revision": span.from, "to_revision": span.to, "current_revision": span.current,
		"known_segments": coverage.segments, "gaps": coverage.gaps, "diffs": diffs,
	})
	result["warnings"] = []string{"Uncovered gaps may contain external writes, provider edits, or expired receipts; no diff is inferred for them."}
	next := []any{map[string]any{"tool": "workspace_inspect", "action": "record_current_revision_as_new_baseline", "view": "status"}}
	if len(paths) > 0 {
		next = append([]any{map[string]any{"tool": "read", "action": "inspect_known_changed_paths", "paths": paths}}, next...)
	}
	result["next"] = next
	return result
}

// completeRevisionDiff reports the ordered edit evidence of a fully covered
// range, dropping the diffs of paths whose endpoint bytes are identical.
func completeRevisionDiff(requestID string, workspace *workspacecore.Workspace, span revisionRange, known []RecordedRevisionDiff) map[string]any {
	type endpoints struct{ before, after string }
	byPath := map[string]endpoints{}
	for _, item := range known {
		ends, exists := byPath[item.Path]
		if !exists {
			ends.before = item.BeforeSHA
		}
		ends.after = item.AfterSHA
		byPath[item.Path] = ends
	}
	diffs := make([]any, 0, len(known))
	for _, item := range known {
		ends := byPath[item.Path]
		if ends.before != "" && ends.before == ends.after {
			continue
		}
		diffs = append(diffs, mcpapi.CompactRevisionDiff(item.Diff))
	}
	netChangedPaths := 0
	for _, ends := range byPath {
		if ends.before == "" || ends.before != ends.after {
			netChangedPaths++
		}
	}
	summary := fmt.Sprintf("%d native edit events cover %d net-changed paths between %s and %s", len(diffs), netChangedPaths, span.from, span.to)
	return mcpapi.Envelope(requestID, workspace, "ok", "", summary, map[string]any{
		"from_revision": span.from, "to_revision": span.to, "current_revision": span.current, "diffs": diffs,
		"semantics": "net endpoint identity with ordered edit evidence",
	})
}

// RevisionSequence parses a wsrev_N workspace revision token.
func RevisionSequence(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil || sequence == 0 {
		return 0, errors.New("invalid workspace revision")
	}
	return sequence, nil
}
