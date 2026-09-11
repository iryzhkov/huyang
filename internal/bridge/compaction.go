package bridge

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Result compaction. The contract is compact by default with detail behind
// IDs and explicit arguments: search hits carry anchors only on request,
// diagnostics list bodies only when evidence exists, the workspace overview
// summarises directories, and diagnostic notices are delivered once per
// client.

// compactDiagnosticReport shapes the diagnostics tool result. Evidence IDs
// are reported once, on the envelope, so the report itself carries none;
// resolved findings collapse to their IDs; when no evidence exists only the
// reasons and counts are returned. full restores the raw report.
func compactDiagnosticReport(report workspacecore.DiagnosticReport, outcome string, full bool) map[string]any {
	if full {
		return map[string]any{"diagnostics": report}
	}
	coverage := make(map[string]any, len(report.Coverage))
	for name, dimension := range report.Coverage {
		coverage[name] = map[string]any{
			"state": dimension.State, "confidence": dimension.Confidence,
			"reasons": nonNilStrings(dimension.Reasons),
		}
	}
	resolvedIDs := make([]string, 0, len(report.Resolved))
	for _, item := range report.Resolved {
		resolvedIDs = append(resolvedIDs, item.ID)
	}
	compact := map[string]any{
		"confidence": report.Confidence, "coverage": coverage, "cursor": report.Cursor,
		"new_count": len(report.New), "resolved_count": len(report.Resolved), "resolved_ids": resolvedIDs,
		"preexisting_count": report.PreexistingCount, "provisional_reasons": nonNilStrings(report.ProvisionalReasons),
	}
	if outcome != "unavailable" {
		items := make([]map[string]any, 0, len(report.New))
		for _, item := range report.New {
			items = append(items, compactDiagnosticItem(item))
		}
		compact["new"] = items
	}
	return map[string]any{"diagnostics": compact}
}

func compactDiagnosticItem(item workspacecore.DiagnosticItem) map[string]any {
	compact := map[string]any{
		"id": item.ID, "document": item.Document, "producer": item.Producer,
		"severity": item.Finding.Severity, "range": item.Finding.Range, "message": item.Finding.Message,
		"attribution": item.Attribution.Rank,
	}
	if item.Finding.Code != "" {
		compact["code"] = item.Finding.Code
	}
	if item.Finding.Source != "" {
		compact["source"] = item.Finding.Source
	}
	if item.DocumentRevision != "" {
		compact["document_revision"] = item.DocumentRevision
	}
	if item.TransactionID != "" {
		compact["transaction_id"] = item.TransactionID
	}
	return compact
}

// compactSearchHits returns the bounded hit list. The default hit carries
// path, line, column, match and the editable handle; the exact byte anchors
// are added only with include_ranges.
func compactSearchHits(hits []workspacecore.SearchHit, limit int, includeRanges bool) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = 100
	}
	returned := hits
	if len(returned) > limit {
		returned = returned[:limit]
	}
	compact := make([]map[string]any, 0, len(returned))
	for _, hit := range returned {
		item := map[string]any{
			"path": hit.Path, "line": hit.Line, "column": hit.Column, "match": hit.Match,
		}
		if hit.MatchHandle != nil {
			item["handle"] = hit.MatchHandle.Handle
		}
		if includeRanges {
			item["byte_start"], item["byte_end"], item["range"] = hit.ByteStart, hit.ByteEnd, hit.Range
		}
		compact = append(compact, item)
	}
	return compact, len(hits) > len(returned)
}

// compactOrientation summarises a workspace overview by top-level entry:
// directories carry their file count and byte total, files their kind and
// size. The full entry listing stays behind overview=full.
func compactOrientation(orientation workspacecore.Orientation) map[string]any {
	type summary struct {
		path  string
		kind  string
		files int
		bytes int64
	}
	byName := map[string]*summary{}
	var order []string
	for _, entry := range orientation.Entries {
		name := entry.Path
		kind := string(entry.Kind)
		if slash := strings.IndexByte(entry.Path, '/'); slash >= 0 {
			name = entry.Path[:slash]
			kind = "directory"
		}
		item := byName[name]
		if item == nil {
			item = &summary{path: name, kind: kind}
			byName[name] = item
			order = append(order, name)
		}
		item.files++
		item.bytes += entry.Size
	}
	sort.Strings(order)
	truncated := len(order) > maxStructuredEntries
	if truncated {
		order = order[:maxStructuredEntries]
	}
	topLevel := make([]map[string]any, 0, len(order))
	for _, name := range order {
		item := byName[name]
		record := map[string]any{"path": item.path, "kind": item.kind, "bytes": item.bytes}
		if item.kind == "directory" {
			record["files"] = item.files
		}
		topLevel = append(topLevel, record)
	}
	return map[string]any{
		"workspace": orientation.Workspace, "coverage": orientation.Coverage,
		"entry_count": len(orientation.Entries), "top_level": topLevel,
		"top_level_count": len(byName), "top_level_truncated": truncated,
	}
}

// compactRecentCommits keeps the handle, abbreviated ID and subject of each
// commit; the full summary is available through read view=changes.
func compactRecentCommits(list workspacecore.CommitList, limit int) map[string]any {
	commits := make([]map[string]any, 0, min(len(list.Commits), limit))
	for _, commit := range list.Commits[:min(len(list.Commits), limit)] {
		commits = append(commits, map[string]any{
			"handle": commit.Handle, "abbreviated_id": commit.AbbreviatedID, "subject": commit.Subject,
		})
	}
	return map[string]any{"commits": commits, "coverage": list.Coverage}
}

// compactLanguageSupport removes the per-language install option lists from
// the provider's support report and replaces each with the key under which
// the same options appear once in install_options.
func compactLanguageSupport(support map[string]any) map[string]any {
	compact := cloneEnvelope(support)
	languages := anySlice(support["languages"])
	entries := make([]any, 0, len(languages))
	for _, raw := range languages {
		entry, ok := raw.(map[string]any)
		if !ok {
			entries = append(entries, raw)
			continue
		}
		copied := cloneEnvelope(entry)
		if options := anySlice(entry["install_options"]); len(options) > 0 {
			copied["install_options_key"] = fmt.Sprint(entry["filetype"])
		}
		delete(copied, "install_options")
		entries = append(entries, copied)
	}
	compact["languages"] = entries
	return compact
}

func compactTextEnvelope(envelope map[string]any) map[string]any {
	compact := map[string]any{
		"api_version": envelope["api_version"], "request_id": envelope["request_id"],
		"outcome": envelope["outcome"], "summary": envelope["summary"],
		"data": compactTextData(envelope["data"]), "evidence": envelope["evidence"],
		"warnings": envelope["warnings"], "next": envelope["next"],
	}
	for _, key := range []string{"code", "workspace", "transaction", "idempotency", "idempotency_persisted"} {
		if value, ok := envelope[key]; ok {
			compact[key] = value
		}
	}
	return compact
}

func compactTextData(value any) any {
	compacted := compactStructuredData(value)
	data, ok := compacted.(map[string]any)
	if !ok {
		return compacted
	}
	for _, key := range []string{"overview", "map"} {
		orientation, ok := data[key].(map[string]any)
		if !ok {
			continue
		}
		delete(orientation, "entries")
	}
	return data
}

const maxStructuredEntries = 100

func compactStructuredEnvelope(envelope map[string]any) map[string]any {
	compact := cloneEnvelope(envelope)
	compact["data"] = compactStructuredData(envelope["data"])
	return compact
}

func compactStructuredData(value any) any {
	data, ok := value.(map[string]any)
	if !ok {
		return value
	}
	compact := make(map[string]any, len(data))
	for key, item := range data {
		switch typed := item.(type) {
		case workspacecore.Orientation:
			entries := typed.Entries
			truncated := len(entries) > maxStructuredEntries
			if truncated {
				entries = entries[:maxStructuredEntries]
			}
			compact[key] = map[string]any{
				"workspace": typed.Workspace, "coverage": typed.Coverage,
				"entries": entries, "entry_count": len(typed.Entries), "entries_truncated": truncated,
			}
		case workspacecore.PlanRecord:
			compact[key] = compactPlanRecord(typed)
		default:
			compact[key] = item
		}
	}
	return compact
}

func compactPlanRecord(plan workspacecore.PlanRecord) map[string]any {
	operations := make([]map[string]any, 0, len(plan.Operations))
	for _, operation := range plan.Operations {
		item := map[string]any{
			"op_id": operation.OpID, "kind": operation.Kind,
			"path": operation.Path, "from": operation.From, "to": operation.To,
			"revision_id": operation.Revision, "destination_revision_id": operation.DestinationRevision,
			"depends_on": operation.DependsOn, "indentation": operation.Indentation,
		}
		if operation.Content != "" {
			sum := sha256.Sum256([]byte(operation.Content))
			item["content_bytes"] = len(operation.Content)
			item["content_sha256"] = fmt.Sprintf("%x", sum[:])
		}
		if operation.Target != nil {
			target := map[string]any{}
			if operation.Target.Handle != "" {
				target["handle"] = operation.Target.Handle
			}
			if operation.Target.FileRange != nil {
				target["file_range"] = map[string]any{
					"path":        operation.Target.FileRange.Path,
					"revision_id": operation.Target.FileRange.Revision,
				}
			}
			if operation.Target.SymbolLocator != nil {
				target["symbol_locator"] = operation.Target.SymbolLocator
			}
			item["target"] = target
		}
		operations = append(operations, item)
	}
	result := map[string]any{
		"plan_id": plan.PlanID, "workspace_id": plan.WorkspaceID, "state": plan.State,
		"plan_revision": plan.PlanRevision, "base_state_seq": plan.BaseStateSeq,
		"operations": operations, "operation_count": len(plan.Operations),
		"event_count": len(plan.Events), "created_at": plan.CreatedAt, "updated_at": plan.UpdatedAt,
	}
	if plan.Preview != nil {
		diffs := make([]map[string]any, 0, len(plan.Preview.Diffs))
		for _, diff := range plan.Preview.Diffs {
			diffs = append(diffs, map[string]any{
				"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256,
				"before_bytes": len(diff.Before), "after_bytes": len(diff.After), "patch_bytes": len(diff.Patch),
			})
		}
		result["preview"] = map[string]any{
			"preview_revision": plan.Preview.PreviewRevision, "plan_revision": plan.Preview.PlanRevision,
			"outcome": plan.Preview.Outcome, "normalized_order": plan.Preview.NormalizedOrder,
			"affected_files": plan.Preview.AffectedFiles, "diffs": diffs,
			"conflicts": plan.Preview.Conflicts, "canonical_changed": plan.Preview.CanonicalChanged,
		}
	}
	if plan.Preparation != nil {
		preparation := *plan.Preparation
		preparation.ToolDelta = append([]workspacecore.ToolDelta(nil), plan.Preparation.ToolDelta...)
		preparation.CommittedDiffs = append([]workspacecore.ExactDiff(nil), plan.Preparation.CommittedDiffs...)
		for index := range preparation.CommittedDiffs {
			preparation.CommittedDiffs[index].Before = nil
			preparation.CommittedDiffs[index].After = nil
			preparation.CommittedDiffs[index].Patch = ""
		}
		for index := range preparation.ToolDelta {
			preparation.ToolDelta[index].Before = nil
			preparation.ToolDelta[index].After = nil
		}
		result["preparation"] = preparation
	}
	return result
}

// compactRevisionDiff keeps the default revision history response bounded by
// omitting complete endpoint bodies. The patch and endpoint hashes retain exact,
// independently checkable evidence without JSON's base64 expansion of []byte.
func compactRevisionDiff(value any) any {
	switch diff := value.(type) {
	case workspacecore.ExactDiff:
		return map[string]any{"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256, "patch": diff.Patch}
	case map[string]any:
		return map[string]any{"path": diff["path"], "before_sha256": diff["before_sha256"], "after_sha256": diff["after_sha256"], "patch": diff["patch"]}
	default:
		return map[string]any{"detail": "diff receipt unavailable", "value_type": fmt.Sprintf("%T", value)}
	}
}

func compactTextChange(change workspacecore.TextChange) map[string]any {
	return map[string]any{
		"workspace":    change.Workspace,
		"before":       change.Before,
		"after_sha256": change.AfterHash,
		"range":        change.Range,
		"replacement":  string(change.Replacement),
		"diff": map[string]any{
			"path": change.Diff.Path, "before_sha256": change.Diff.BeforeSHA256,
			"after_sha256": change.Diff.AfterSHA256, "patch": change.Diff.Patch,
		},
	}
}

const (
	modernVerificationListLimit   = 20
	modernVerificationOutputLimit = 4096
)

func boundedVerificationStrings(values []string) ([]string, bool) {
	if len(values) <= modernVerificationListLimit {
		return nonNilStrings(values), false
	}
	return append([]string(nil), values[:modernVerificationListLimit]...), true
}

func compactVerificationImpact(graph *workspacecore.ImpactGraph) (map[string]any, bool) {
	if graph == nil {
		return nil, false
	}
	changed, changedTruncated := boundedVerificationStrings(graph.Changed)
	affected, affectedTruncated := boundedVerificationStrings(graph.Affected)
	untested, untestedTruncated := boundedVerificationStrings(graph.Untested)
	riskKinds := map[string]int{}
	for _, risk := range graph.Risks {
		riskKinds[risk.Kind]++
	}
	truncated := changedTruncated || affectedTruncated || untestedTruncated ||
		len(graph.Nodes) > 0 || len(graph.Edges) > 0 || len(graph.Risks) > 0
	return map[string]any{
		"revision": graph.Revision,
		"changed":  changed, "changed_count": len(graph.Changed), "changed_truncated": changedTruncated,
		"affected": affected, "affected_count": len(graph.Affected), "affected_truncated": affectedTruncated,
		"affected_without_tests": untested, "affected_without_tests_count": len(graph.Untested),
		"affected_without_tests_truncated": untestedTruncated,
		"node_count":                       len(graph.Nodes), "edge_count": len(graph.Edges),
		"risk_count": len(graph.Risks), "risk_kind_counts": riskKinds,
		"coverage": graph.Coverage, "adapters": nonNilStrings(graph.Adapters),
		"variants_included": nonNilStrings(graph.Included), "variants_omitted": nonNilStrings(graph.Omitted),
		"recommend_full": graph.RecommendFull, "details_truncated": truncated,
	}, truncated
}

func compactVerificationResult(result workspacecore.VerificationResult) (map[string]any, []string) {
	compacted := map[string]any{"revision": result.Revision}
	var evidenceIDs []string
	detailsTruncated := false
	stages := make([]any, 0, len(result.Stages))
	for _, stage := range result.Stages {
		output := stage.Output
		outputTruncated := len(output) > modernVerificationOutputLimit
		if outputTruncated {
			output = output[:modernVerificationOutputLimit]
			detailsTruncated = true
		}
		scope, scopeTruncated := boundedVerificationStrings(stage.Scope)
		writes, writesTruncated := boundedVerificationStrings(stage.Writes)
		executed, executedTruncated := boundedVerificationStrings(stage.ExecutedTests)
		selected := make([]any, 0, min(len(stage.SelectedTests), modernVerificationListLimit))
		for _, test := range stage.SelectedTests[:min(len(stage.SelectedTests), modernVerificationListLimit)] {
			selected = append(selected, map[string]any{
				"name": test.Name, "reasons": nonNilStrings(test.Reasons), "variants": nonNilStrings(test.Variants),
			})
		}
		selectedTruncated := len(stage.SelectedTests) > modernVerificationListLimit
		detailsTruncated = detailsTruncated || scopeTruncated || writesTruncated || executedTruncated || selectedTruncated
		stageData := map[string]any{
			"stage": stage.Stage, "mode": stage.Mode, "started_revision": stage.StartedRevision,
			"exit": stage.Exit, "status": stage.Status, "duration_ms": stage.DurationMS,
			"coverage": stage.Coverage, "evidence_ids": nonNilStrings(stage.EvidenceIDs),
			"scope": scope, "scope_count": len(stage.Scope), "scope_truncated": scopeTruncated,
			"writes": writes, "writes_count": len(stage.Writes), "writes_truncated": writesTruncated,
			"output": output, "output_bytes": len(stage.Output), "output_truncated": outputTruncated,
			"test_scope": stage.TestScope, "test_verdict": stage.TestVerdict,
			"selected_tests": selected, "selected_test_count": len(stage.SelectedTests),
			"selected_tests_truncated": selectedTruncated,
			"executed_tests":           executed, "executed_test_count": len(stage.ExecutedTests),
			"executed_tests_truncated": executedTruncated,
		}
		stages = append(stages, stageData)
		evidenceIDs = append(evidenceIDs, stage.EvidenceIDs...)
	}
	compacted["stages"] = stages
	compacted["stage_count"] = len(result.Stages)
	if impact, truncated := compactVerificationImpact(result.Impact); impact != nil {
		compacted["impact"] = impact
		detailsTruncated = detailsTruncated || truncated
	}
	if result.Targeted != nil {
		selected := make([]any, 0, min(len(result.Targeted.Selected), modernVerificationListLimit))
		for _, test := range result.Targeted.Selected[:min(len(result.Targeted.Selected), modernVerificationListLimit)] {
			selected = append(selected, map[string]any{
				"name": test.Name, "reasons": nonNilStrings(test.Reasons), "variants": nonNilStrings(test.Variants),
			})
		}
		executed, executedTruncated := boundedVerificationStrings(result.Targeted.Executed)
		selectedTruncated := len(result.Targeted.Selected) > modernVerificationListLimit
		compacted["targeted_tests"] = map[string]any{
			"status": result.Targeted.Status, "selected": selected,
			"selected_count": len(result.Targeted.Selected), "selected_truncated": selectedTruncated,
			"executed": executed, "executed_count": len(result.Targeted.Executed),
			"executed_truncated": executedTruncated,
			"graph_revision":     result.Targeted.Graph.Revision,
		}
		detailsTruncated = detailsTruncated || selectedTruncated || executedTruncated || len(result.Targeted.Graph.Nodes) > 0 ||
			len(result.Targeted.Graph.Edges) > 0 || len(result.Targeted.Graph.Risks) > 0
	}
	if len(result.ToolDelta) > 0 {
		deltas := make([]any, 0, min(len(result.ToolDelta), modernVerificationListLimit))
		for _, delta := range result.ToolDelta[:min(len(result.ToolDelta), modernVerificationListLimit)] {
			deltas = append(deltas, map[string]any{
				"path": delta.Path, "classification": delta.Classification,
				"before_exists": delta.BeforeExists, "after_exists": delta.AfterExists,
				"before_kind": delta.BeforeKind, "after_kind": delta.AfterKind,
				"before_mode": delta.BeforeMode, "after_mode": delta.AfterMode,
				"before_target": delta.BeforeTarget, "after_target": delta.AfterTarget,
				"before_bytes": len(delta.Before), "after_bytes": len(delta.After),
			})
		}
		compacted["tool_delta"] = deltas
		compacted["tool_delta_count"] = len(result.ToolDelta)
		compacted["tool_delta_truncated"] = len(result.ToolDelta) > modernVerificationListLimit
		detailsTruncated = true
	}
	if result.FullTestGate != "" {
		compacted["full_test_gate"] = result.FullTestGate
	}
	compacted["details_truncated"] = detailsTruncated
	sort.Strings(evidenceIDs)
	return compacted, nonNilStrings(uniqueStrings(evidenceIDs))
}
