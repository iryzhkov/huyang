package bridge

import (
	"errors"
	"fmt"
	"sort"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func (h *toolHandlers) revisionDiff(requestID string, workspace *workspacecore.Workspace, arguments map[string]any) map[string]any {
	if err := workspace.PrimeDocuments(); err != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if _, err := workspace.RefreshKnownDocuments(); err != nil {
		return mcpapi.Failure(requestID, workspace, "workspace_refresh_failed", err)
	}
	if err := h.registry.PersistIdentity(workspace.Identity().ID); err != nil {
		return mcpapi.Failure(requestID, workspace, "service_state_persist_failed", err)
	}
	from := fmt.Sprint(arguments["from_revision"])
	to := fmt.Sprint(arguments["to_revision_or_current"])
	current := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if to == "current" {
		to = current
	}
	fromSeq, fromErr := workspaceRevisionSequence(from)
	toSeq, toErr := workspaceRevisionSequence(to)
	if fromErr != nil || toErr != nil || fromSeq > toSeq {
		return mcpapi.Envelope(requestID, workspace, "failed", "invalid_revision_range", "revision_diff requires an ordered wsrev_N range", map[string]any{"from_revision": from, "to_revision": to})
	}
	if toSeq > workspace.Identity().StateSeq {
		return mcpapi.Envelope(requestID, workspace, "conflict", "revision_changed", "Requested target revision is newer than the workspace", map[string]any{"current_revision": current})
	}
	recorded := h.provenance.RecordedRevisionDiffs(string(workspace.Identity().ID), fromSeq, toSeq)
	sort.Slice(recorded, func(i, j int) bool {
		if recorded[i].From != recorded[j].From {
			return recorded[i].From < recorded[j].From
		}
		if recorded[i].To != recorded[j].To {
			return recorded[i].To < recorded[j].To
		}
		return recorded[i].Path < recorded[j].Path
	})
	known := make([]recordedRevisionDiff, 0, len(recorded))
	segments := make([]any, 0, len(recorded))
	gaps := make([]any, 0)
	cursor := fromSeq
	for _, item := range recorded {
		sameRevision := len(known) > 0 &&
			known[len(known)-1].From == item.From &&
			known[len(known)-1].To == item.To
		if item.To <= cursor {
			if !sameRevision {
				continue
			}
		} else if item.From < cursor {
			continue
		}
		if item.From > cursor {
			gaps = append(gaps, map[string]any{
				"from_revision": fmt.Sprintf("wsrev_%d", cursor),
				"to_revision":   fmt.Sprintf("wsrev_%d", item.From),
			})
		}
		known = append(known, item)
		segments = append(segments, map[string]any{
			"from_revision": fmt.Sprintf("wsrev_%d", item.From),
			"to_revision":   fmt.Sprintf("wsrev_%d", item.To),
			"path":          item.Path,
		})
		if item.To > cursor {
			cursor = item.To
		}
	}
	if cursor < toSeq {
		gaps = append(gaps, map[string]any{
			"from_revision": fmt.Sprintf("wsrev_%d", cursor),
			"to_revision":   fmt.Sprintf("wsrev_%d", toSeq),
		})
	}
	if len(gaps) > 0 {
		diffs := make([]any, 0, len(known))
		paths := make([]string, 0, len(known))
		for _, item := range known {
			diffs = append(diffs, mcpapi.CompactRevisionDiff(item.Diff))
			if item.Path != "" {
				paths = append(paths, item.Path)
			}
		}
		sort.Strings(paths)
		paths = mcpapi.UniqueStrings(paths)
		result := mcpapi.Envelope(requestID, workspace, "partial", "diff_evidence_incomplete", "Known native edit segments are returned with explicit uncovered revision gaps", map[string]any{
			"from_revision": from, "to_revision": to, "current_revision": current,
			"known_segments": segments, "gaps": gaps, "diffs": diffs,
		})
		result["warnings"] = []string{"Uncovered gaps may contain external writes, provider edits, or expired receipts; no diff is inferred for them."}
		next := []any{map[string]any{"tool": "workspace_inspect", "action": "record_current_revision_as_new_baseline", "view": "status"}}
		if len(paths) > 0 {
			next = append([]any{map[string]any{"tool": "read", "action": "inspect_known_changed_paths", "paths": paths}}, next...)
		}
		result["next"] = next
		return result
	}
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
	summary := fmt.Sprintf("%d native edit events cover %d net-changed paths between %s and %s", len(diffs), netChangedPaths, from, to)
	return mcpapi.Envelope(requestID, workspace, "ok", "", summary, map[string]any{
		"from_revision": from, "to_revision": to, "current_revision": current, "diffs": diffs,
		"semantics": "net endpoint identity with ordered edit evidence",
	})
}

func workspaceRevisionSequence(revision string) (uint64, error) {
	var sequence uint64
	if _, err := fmt.Sscanf(revision, "wsrev_%d", &sequence); err != nil || sequence == 0 {
		return 0, errors.New("invalid workspace revision")
	}
	return sequence, nil
}
