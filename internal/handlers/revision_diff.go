package handlers

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// revisionRange is the decoded, validated revision_diff request: both
// endpoints as tokens and as sequence numbers, with "current" resolved.
// Warnings say what was corrected on the way: a reversed range, or a
// revision below the first one.
type revisionRange struct {
	from, to, current string
	fromSeq, toSeq    uint64
	warnings          []string
}

// revisionCoverage is what the native receipts say about a revision range:
// the ordered segments they cover and the gaps between them.
type revisionCoverage struct {
	known    []RecordedRevisionDiff
	segments []any
	gaps     []any
	// spans are the gaps as sequence pairs, in the same order as gaps.
	spans [][2]uint64
	// inferred are the gaps the workspace's own observations filled, each
	// with the steps it was filled from.
	inferred []any
	// unlisted counts the paths inventory steps saw but did not keep, which
	// therefore have no inferred entry.
	unlisted int
}

// maxRevisionDiffEntries bounds each list a revision_diff reply carries. A
// range across a checkout or a generator run can hold thousands of paths,
// and past a few hundred a reply is read as a list of names, not a diff.
const maxRevisionDiffEntries = 200

// putBounded sets data[key] to at most maxRevisionDiffEntries items and, when
// it cut the list, says so beside it with the full count.
func putBounded(data map[string]any, key string, items []any) {
	data[key] = items
	if len(items) > maxRevisionDiffEntries {
		data[key] = items[:maxRevisionDiffEntries]
		data[key+"_truncated"] = true
		data[key+"_total"] = len(items)
	}
}

func (h *Handlers) revisionDiff(ctx context.Context, requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
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
	if view, _ := arguments["view"].(string); view == "semantic" {
		return h.revisionDiffSemantic(ctx, requestID, workspace, span)
	}
	recorded := h.provenance.RecordedRevisionDiffs(string(workspace.Identity().ID), span.fromSeq, span.toSeq)
	coverage := coverRevisionRange(recorded, span)
	fillObservedGaps(workspace, &coverage)
	var result map[string]any
	if len(coverage.gaps) > 0 {
		result = partialRevisionDiff(requestID, workspace, span, coverage)
	} else {
		result = completeRevisionDiff(requestID, workspace, span, coverage)
	}
	if len(span.warnings) > 0 {
		warnings, _ := result["warnings"].([]string)
		result["warnings"] = append(append([]string(nil), span.warnings...), warnings...)
	}
	return result
}

// decodeRevisionRange validates the requested range against the workspace.
// Either endpoint may be "current"; a reversed range is swapped and wsrev_0
// is read as wsrev_1, each with a warning, because the caller's meaning is
// plain. What is left to refuse is a token that is no revision at all and a
// target newer than the workspace.
func decodeRevisionRange(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) (revisionRange, map[string]any) {
	span := revisionRange{
		from:    fmt.Sprint(arguments["from_revision"]),
		to:      fmt.Sprint(arguments["to_revision_or_current"]),
		current: fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq),
	}
	if span.from == "current" {
		span.from = span.current
	}
	if span.to == "current" {
		span.to = span.current
	}
	fromSeq, fromErr := revisionEndpoint(span.from)
	toSeq, toErr := revisionEndpoint(span.to)
	if fromErr != nil || toErr != nil {
		result := mcpapi.Envelope(requestID, workspace, "failed", "invalid_revision_range",
			fmt.Sprintf("revision_diff takes wsrev_N revisions or current; the workspace is at %s", span.current),
			map[string]any{"from_revision": span.from, "to_revision": span.to, "current_revision": span.current})
		result["next"] = []any{map[string]any{
			"tool": "revision_diff", "from_revision": "wsrev_1", "to_revision_or_current": "current",
			"note": "Name each end as a wsrev_N revision a reply returned, or current.",
		}}
		return span, result
	}
	for _, endpoint := range []struct {
		token    *string
		sequence *uint64
	}{{&span.from, &fromSeq}, {&span.to, &toSeq}} {
		if *endpoint.sequence == 0 {
			span.warnings = append(span.warnings, fmt.Sprintf("%s precedes the first revision; read as wsrev_1.", *endpoint.token))
			*endpoint.sequence, *endpoint.token = 1, "wsrev_1"
		}
	}
	if fromSeq > toSeq {
		span.warnings = append(span.warnings, fmt.Sprintf("from_revision %s is newer than %s; the range was swapped.", span.from, span.to))
		fromSeq, toSeq = toSeq, fromSeq
		span.from, span.to = span.to, span.from
	}
	if toSeq > workspace.Identity().StateSeq {
		return span, mcpapi.Envelope(requestID, workspace, "conflict", "revision_changed", "Requested target revision is newer than the workspace", map[string]any{"current_revision": span.current})
	}
	span.fromSeq, span.toSeq = fromSeq, toSeq
	return span, nil
}

// revisionEndpoint parses one end of a revision range. Unlike
// RevisionSequence it accepts wsrev_0, which the caller clamps.
func revisionEndpoint(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil || fmt.Sprintf("wsrev_%d", sequence) != revision {
		return 0, errors.New("invalid workspace revision")
	}
	return sequence, nil
}

// fillObservedGaps fills the gaps no receipt covers from what the workspace
// itself observed: a document whose bytes changed on disk, files that
// appeared or went away, a new provider epoch. A gap is filled only when
// every step in it was observed, so an answer never claims a step it cannot
// account for. The entries are marked inferred: they carry the content
// hashes on either side and the path, not a patch, because the bytes before
// an external write were never retained.
func fillObservedGaps(workspace *workspacecore.Workspace, coverage *revisionCoverage) {
	var gaps []any
	var spans [][2]uint64
	for index, gap := range coverage.spans {
		changes := workspace.ObservedChanges(gap[0], gap[1])
		steps := map[uint64]bool{}
		for _, change := range changes {
			steps[change.Seq] = true
		}
		if uint64(len(steps)) != gap[1]-gap[0] {
			gaps = append(gaps, coverage.gaps[index])
			spans = append(spans, gap)
			continue
		}
		for _, change := range changes {
			coverage.known = append(coverage.known, observedRevisionDiffs(change)...)
			coverage.unlisted += change.AddedOmitted + change.RemovedOmitted
		}
		filled := revisionSpan(gap[0], gap[1])
		filled["inferred"] = true
		putBounded(filled, "steps", observedSteps(changes))
		coverage.inferred = append(coverage.inferred, filled)
	}
	coverage.gaps, coverage.spans = gaps, spans
	if gaps == nil {
		coverage.gaps = make([]any, 0)
	}
	sort.SliceStable(coverage.known, func(i, j int) bool { return coverage.known[i].From < coverage.known[j].From })
}

// observedSteps describes the steps an inferred segment was filled from. An
// inventory step is described by its counts: the paths it kept are already
// entries in diffs, and listing them here as well doubled the reply.
func observedSteps(changes []workspacecore.ObservedChange) []any {
	steps := make([]any, 0, len(changes))
	for _, change := range changes {
		step := map[string]any{"seq": change.Seq, "reason": change.Reason}
		switch change.Reason {
		case workspacecore.ObservedDocument:
			step["path"] = change.Path
		case workspacecore.ObservedInventory:
			step["added"] = len(change.Added) + change.AddedOmitted
			step["removed"] = len(change.Removed) + change.RemovedOmitted
			if unlisted := change.AddedOmitted + change.RemovedOmitted; unlisted > 0 {
				step["paths_not_listed"] = unlisted
			}
		}
		steps = append(steps, step)
	}
	return steps
}

// unlistedWarning says that inventory steps saw more paths than they kept,
// so the inferred entries do not name every file that appeared or went away.
func unlistedWarning(coverage revisionCoverage) []string {
	if coverage.unlisted == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d paths that appeared or went away in observed inventory steps are counted in inferred_segments but have no entry in diffs; git status lists them.", coverage.unlisted)}
}

// observedRevisionDiffs is what one observed step contributes to the diff:
// a document whose content changed, or the files an inventory step added and
// removed. A step that changed only metadata, or only the provider epoch,
// changed no content and contributes nothing.
func observedRevisionDiffs(change workspacecore.ObservedChange) []RecordedRevisionDiff {
	entry := func(path, before, after, kind string) RecordedRevisionDiff {
		diff := map[string]any{
			"path": path, "before_sha256": before, "after_sha256": after,
			"change": kind, "inferred": true, "source": "observed_on_disk",
			"to_revision": fmt.Sprintf("wsrev_%d", change.Seq),
		}
		return RecordedRevisionDiff{From: change.Seq - 1, To: change.Seq, Path: path, BeforeSHA: before, AfterSHA: after, Diff: diff}
	}
	var diffs []RecordedRevisionDiff
	switch change.Reason {
	case workspacecore.ObservedDocument:
		if change.BeforeSHA256 == change.AfterSHA256 {
			return nil
		}
		kind := "modified"
		switch {
		case change.BeforeSHA256 == "":
			kind = "created"
		case change.AfterSHA256 == "":
			kind = "deleted"
		}
		diffs = append(diffs, entry(change.Path, change.BeforeSHA256, change.AfterSHA256, kind))
	case workspacecore.ObservedInventory:
		for _, path := range change.Added {
			diffs = append(diffs, entry(path, "", "", "created"))
		}
		for _, path := range change.Removed {
			diffs = append(diffs, entry(path, "", "", "deleted"))
		}
	}
	return diffs
}

// revisionDiffEntry is how one diff appears in a reply: a receipt is
// compacted to its path, hashes and patch, and an inferred entry, which has
// no patch to compact, is returned as it was built.
func revisionDiffEntry(item RecordedRevisionDiff) any {
	if diff, ok := item.Diff.(map[string]any); ok && diff["inferred"] == true {
		return diff
	}
	return mcpapi.CompactRevisionDiff(item.Diff)
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
			coverage.spans = append(coverage.spans, [2]uint64{cursor, item.From})
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
		coverage.spans = append(coverage.spans, [2]uint64{cursor, span.toSeq})
	}
	return coverage
}

func revisionSpan(from, to uint64) map[string]any {
	return map[string]any{
		"from_revision": fmt.Sprintf("wsrev_%d", from),
		"to_revision":   fmt.Sprintf("wsrev_%d", to),
	}
}

// partialRevisionDiff reports the known and inferred segments alongside the
// gaps that neither a receipt nor an observation accounts for, and names
// those gaps in the summary.
func partialRevisionDiff(requestID string, workspace *workspacecore.Workspace, span revisionRange, coverage revisionCoverage) map[string]any {
	diffs := make([]any, 0, len(coverage.known))
	paths := make([]string, 0, len(coverage.known))
	for _, item := range coverage.known {
		diffs = append(diffs, revisionDiffEntry(item))
		if item.Path != "" {
			paths = append(paths, item.Path)
		}
	}
	sort.Strings(paths)
	paths = mcpapi.UniqueStrings(paths)
	uncovered := make([]string, 0, len(coverage.spans))
	for _, gap := range coverage.spans {
		uncovered = append(uncovered, fmt.Sprintf("wsrev_%d..wsrev_%d", gap[0], gap[1]))
	}
	summary := fmt.Sprintf("No receipt or observation covers %s; known segments are returned with those gaps", strings.Join(uncovered, ", "))
	data := map[string]any{
		"from_revision": span.from, "to_revision": span.to, "current_revision": span.current,
		"gaps": coverage.gaps,
	}
	putBounded(data, "known_segments", coverage.segments)
	putBounded(data, "diffs", diffs)
	if len(coverage.inferred) > 0 {
		putBounded(data, "inferred_segments", coverage.inferred)
	}
	result := mcpapi.Envelope(requestID, workspace, "partial", "diff_evidence_incomplete", summary, data)
	result["warnings"] = append([]string{"Uncovered gaps may contain external writes, provider edits, or expired receipts, possibly from before a service restart; no diff is inferred for them."}, unlistedWarning(coverage)...)
	next := []any{map[string]any{
		"action": "inspect_uncovered_range_with_git", "command": "git status --short && git diff",
		"note": "Git shows what changed on disk against its own baseline, which covers the uncovered revisions " + strings.Join(uncovered, ", ") + " as well.",
	}}
	if len(paths) > 0 {
		next = append(next, map[string]any{"tool": "read", "action": "inspect_known_changed_paths", "paths": paths})
	} else {
		next = append(next, map[string]any{"tool": "workspace_inspect", "action": "record_current_revision_as_new_baseline", "view": "status"})
	}
	result["next"] = next
	return result
}

// completeRevisionDiff reports the ordered edit evidence of a fully covered
// range, dropping the diffs of paths whose endpoint bytes are identical.
// Steps filled from the workspace's observations are included, marked
// inferred, and said so in a warning.
func completeRevisionDiff(requestID string, workspace *workspacecore.Workspace, span revisionRange, coverage revisionCoverage) map[string]any {
	known := coverage.known
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
		diffs = append(diffs, revisionDiffEntry(item))
	}
	netChangedPaths := 0
	for _, ends := range byPath {
		if ends.before == "" || ends.before != ends.after {
			netChangedPaths++
		}
	}
	summary := fmt.Sprintf("%d native edit events cover %d net-changed paths between %s and %s", len(diffs), netChangedPaths, span.from, span.to)
	data := map[string]any{
		"from_revision": span.from, "to_revision": span.to, "current_revision": span.current,
		"semantics": "net endpoint identity with ordered edit evidence",
	}
	putBounded(data, "diffs", diffs)
	if len(coverage.inferred) == 0 {
		return mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
	}
	putBounded(data, "inferred_segments", coverage.inferred)
	summary = fmt.Sprintf("%d edit events, some observed on disk rather than made through Huyang, cover %d net-changed paths between %s and %s", len(diffs), netChangedPaths, span.from, span.to)
	result := mcpapi.Envelope(requestID, workspace, "ok", "", summary, data)
	result["warnings"] = append([]string{"Some revisions have no edit receipt; their entries are inferred from what the workspace observed on disk and carry content hashes but no patch. Read the path, or run git diff on it, for the text."}, unlistedWarning(coverage)...)
	return result
}

// RevisionSequence parses a wsrev_N workspace revision token.
func RevisionSequence(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil || sequence == 0 {
		return 0, errors.New("invalid workspace revision")
	}
	return sequence, nil
}
