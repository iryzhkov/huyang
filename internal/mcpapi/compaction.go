package mcpapi

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// Result compaction. The contract is compact by default with detail behind
// IDs and explicit arguments: search hits carry anchors only on request,
// diagnostics list bodies only when evidence exists, the workspace overview
// summarises directories, and diagnostic notices are delivered once per
// client.

// CompactDiagnosticReport shapes the diagnostics tool result. Evidence IDs
// are reported once, on the envelope, so the report itself carries none;
// resolved findings collapse to their IDs; when no evidence exists only the
// reasons and counts are returned. full restores the raw report.
func CompactDiagnosticReport(report workspacecore.DiagnosticReport, outcome string, full bool) map[string]any {
	if full {
		return map[string]any{"diagnostics": report}
	}
	coverage := make(map[string]any, len(report.Coverage))
	for name, dimension := range report.Coverage {
		coverage[name] = map[string]any{
			"state": dimension.State, "confidence": dimension.Confidence,
			"reasons": NonNilStrings(dimension.Reasons),
		}
	}
	resolvedIDs := make([]string, 0, len(report.Resolved))
	for _, item := range report.Resolved {
		resolvedIDs = append(resolvedIDs, item.ID)
	}
	// A finding announced as new and since marked stale (its document
	// changed underneath it) is not current evidence; it is counted under
	// stale_count rather than listed as new.
	current := make([]workspacecore.DiagnosticItem, 0, len(report.New))
	for _, item := range report.New {
		if item.Status != workspacecore.DiagnosticStatusStale {
			current = append(current, item)
		}
	}
	compact := map[string]any{
		"confidence": report.Confidence, "coverage": coverage, "cursor": report.Cursor,
		"new_count": len(current), "resolved_count": len(report.Resolved), "resolved_ids": resolvedIDs,
		"stale_count":       report.StaleCount,
		"preexisting_count": report.PreexistingCount, "provisional_reasons": NonNilStrings(report.ProvisionalReasons),
	}
	if outcome != "unavailable" {
		items := make([]map[string]any, 0, len(current))
		for _, item := range current {
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

// CompactSearchHits returns the bounded hit list. The default hit carries
// path, line, column, match and the editable handle; the exact byte anchors
// are added only with include_ranges.
func CompactSearchHits(hits []workspacecore.SearchHit, limit int, includeRanges, includeHandles bool) ([]map[string]any, bool) {
	if limit <= 0 {
		limit = 100
	}
	returned := hits
	if len(returned) > limit {
		returned = returned[:limit]
	}
	compact := make([]map[string]any, 0, len(returned))
	for _, hit := range returned {
		item := map[string]any{"path": hit.Path, "line": hit.Line, "match": hit.Match}
		// The column and the editable handle matter only to a caller that
		// will edit by handle; replace_literal needs neither.
		if includeHandles || includeRanges {
			item["column"] = hit.Column
			if hit.MatchHandle != nil {
				item["handle"] = hit.MatchHandle.Handle
			}
		}
		if includeRanges {
			item["byte_start"], item["byte_end"], item["range"] = hit.ByteStart, hit.ByteEnd, hit.Range
		}
		compact = append(compact, item)
	}
	return compact, len(hits) > len(returned)
}

// CompactResultSet keeps the handle and the counts an agent decides on; the
// per-set coverage, constraints and echoed query stay out of the reply.
func CompactResultSet(set *workspacecore.ResultSet) map[string]any {
	if set == nil {
		return nil
	}
	compact := map[string]any{"handle": set.Handle, "match_count": set.MatchCount, "file_count": set.FileCount}
	// Flags that hold their normal value are left out; only a deviation
	// (an incomplete set, hits that cannot all be edited, a refinement)
	// is worth the tokens.
	if !set.Complete {
		compact["complete"] = false
	}
	if !set.AllMatchesEligible {
		compact["all_matches_eligible"] = false
	}
	if set.Parent != "" {
		compact["parent"], compact["retained"], compact["eliminated"] = set.Parent, set.Retained, set.Eliminated
	}
	return compact
}

// CompactOrientation summarises a workspace overview by top-level entry:
// directories carry their file count and byte total, files their kind and
// size. The full entry listing stays behind overview=full.
func CompactOrientation(orientation workspacecore.Orientation) map[string]any {
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
	truncated := len(order) > MaxStructuredEntries
	if truncated {
		order = order[:MaxStructuredEntries]
	}
	topLevel := make([]map[string]any, 0, len(order))
	for _, name := range order {
		item := byName[name]
		record := map[string]any{"path": item.path, "kind": item.kind}
		if item.kind == "directory" {
			record["files"] = item.files
		}
		topLevel = append(topLevel, record)
	}
	// The workspace identity is already the envelope's, and coverage that is
	// complete says only that the walk finished, which the entry count says
	// too: both were a repeat in every reply that opens a workspace.
	compact := map[string]any{
		"entry_count": len(orientation.Entries), "top_level": topLevel,
		"top_level_count": len(byName), "top_level_truncated": truncated,
	}
	if !orientation.Coverage.Complete {
		compact["coverage"] = orientation.Coverage
	}
	return compact
}

// CompactRecentCommits keeps the abbreviated ID and subject of each commit.
// The opaque handle that read view=changes needs is not here: it is 32 hex
// characters per commit in a reply that opens a workspace, for a call an
// agent rarely makes, and read view=history hands out the same handles when
// it does.
func CompactRecentCommits(list workspacecore.CommitList, limit int) map[string]any {
	commits := make([]map[string]any, 0, min(len(list.Commits), limit))
	for _, commit := range list.Commits[:min(len(list.Commits), limit)] {
		commits = append(commits, map[string]any{
			"abbreviated_id": commit.AbbreviatedID, "subject": commit.Subject,
		})
	}
	compact := map[string]any{"commits": commits}
	if !list.Coverage.Complete && len(list.Coverage.Unavailable) > 0 {
		compact["coverage"] = list.Coverage
	}
	return compact
}

// CompactLanguageSupport removes the per-language install option lists from
// the provider's support report and replaces each with the key under which
// the same options appear once in install_options.
func CompactLanguageSupport(support map[string]any) map[string]any {
	compact := CloneEnvelope(support)
	languages := AnySlice(support["languages"])
	entries := make([]any, 0, len(languages))
	for _, raw := range languages {
		entry, ok := raw.(map[string]any)
		if !ok {
			entries = append(entries, raw)
			continue
		}
		copied := CloneEnvelope(entry)
		if options := AnySlice(entry["install_options"]); len(options) > 0 {
			copied["install_options_key"] = fmt.Sprint(entry["filetype"])
		}
		delete(copied, "install_options")
		entries = append(entries, copied)
	}
	compact["languages"] = entries
	return compact
}

func CompactTextEnvelope(envelope map[string]any) map[string]any {
	compact := map[string]any{
		"api_version": envelope["api_version"], "request_id": envelope["request_id"],
		"outcome": envelope["outcome"], "summary": envelope["summary"],
		"data": CompactTextData(envelope["data"]), "evidence": envelope["evidence"],
		"warnings": envelope["warnings"], "next": envelope["next"],
	}
	// The optional fields, copied when present. diagnostic_updates and guide
	// are the two an agent acts on and neither is on every reply, so a
	// renderer that forgets one drops it silently: the guide reached every
	// in-process test and no client at all until a live test crossed the
	// socket and found it missing.
	for _, key := range []string{
		"code", "workspace", "transaction", "idempotency", "idempotency_persisted",
		"guide", "diagnostic_updates", "diagnostic_updates_truncated",
	} {
		if value, ok := envelope[key]; ok {
			compact[key] = value
		}
	}
	return compact
}

func CompactTextData(value any) any {
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

const MaxStructuredEntries = 100

func CompactStructuredEnvelope(envelope map[string]any) map[string]any {
	compact := CloneEnvelope(envelope)
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
			truncated := len(entries) > MaxStructuredEntries
			if truncated {
				entries = entries[:MaxStructuredEntries]
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
	// The invariants are why a prepared plan may not be applicable, so they
	// travel with the record rather than being summarised away.
	if len(plan.Invariants) > 0 {
		result["invariants"] = plan.Invariants
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
			// The bodies are too large for every plan reply to carry them, and
			// leaving the fields present and null said "there is no patch"
			// rather than "the patch is not in this reply". They are dropped
			// and replaced by their sizes, the way a preview diff already
			// reports them; the hashes stay, so the change is still checkable.
			preparation.CommittedDiffs[index].Summarize()
		}
		for index := range preparation.ToolDelta {
			preparation.ToolDelta[index].Before = nil
			preparation.ToolDelta[index].After = nil
		}
		result["preparation"] = compactPreparation(preparation)
	}
	return result
}

// compactPreparation renders a preparation with its verification stages
// compacted the way verify_run compacts them. A prepared plan carried the
// verbose record of every stage -- the command argv, the mode, the started
// revision and a coverage object of zeros for each -- which was two
// thirds of a prepare reply and none of it the answer to "did it prepare".
func compactPreparation(preparation workspacecore.PlanPreparation) map[string]any {
	stages := preparation.Verification
	preparation.Verification = nil
	encoded, err := json.Marshal(preparation)
	if err != nil {
		preparation.Verification = stages
		return map[string]any{"preparation": preparation}
	}
	var compact map[string]any
	if err := json.Unmarshal(encoded, &compact); err != nil {
		preparation.Verification = stages
		return map[string]any{"preparation": preparation}
	}
	if len(stages) == 0 {
		return compact
	}
	records := make([]any, 0, len(stages))
	for _, stage := range stages {
		record, _ := compactVerificationStage(stage, false)
		records = append(records, record)
	}
	compact["verification"] = records
	return compact
}

// CompactRevisionDiff keeps the default revision history response bounded by
// omitting complete endpoint bodies. The patch and endpoint hashes retain exact,
// independently checkable evidence without JSON's base64 expansion of []byte.
func CompactRevisionDiff(value any) any {
	switch diff := value.(type) {
	case workspacecore.ExactDiff:
		return map[string]any{"path": diff.Path, "before_sha256": diff.BeforeSHA256, "after_sha256": diff.AfterSHA256, "patch": diff.Patch}
	case map[string]any:
		return map[string]any{"path": diff["path"], "before_sha256": diff["before_sha256"], "after_sha256": diff["after_sha256"], "patch": diff["patch"]}
	default:
		return map[string]any{"detail": "diff receipt unavailable", "value_type": fmt.Sprintf("%T", value)}
	}
}

func CompactTextChange(change workspacecore.TextChange) map[string]any {
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

// VerificationListLimit and VerificationOutputLimit bound the lists and the
// command output a compacted verification result carries per stage.
const (
	VerificationListLimit   = 20
	VerificationOutputLimit = 4096
)

func boundedVerificationStrings(values []string) ([]string, bool) {
	if len(values) <= VerificationListLimit {
		return NonNilStrings(values), false
	}
	return append([]string(nil), values[:VerificationListLimit]...), true
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
		// Which analysis this answer came from and who contributed to it: an
		// impact claim is only as good as the snapshot beneath it, and a
		// caller cannot weigh it without knowing whether a language server
		// was one of the voices.
		"snapshot": graph.Snapshot, "producers": NonNilStrings(graph.Producers),
		"changed": changed, "changed_count": len(graph.Changed), "changed_truncated": changedTruncated,
		"affected": affected, "affected_count": len(graph.Affected), "affected_truncated": affectedTruncated,
		"affected_without_tests": untested, "affected_without_tests_count": len(graph.Untested),
		"affected_without_tests_truncated": untestedTruncated,
		"node_count":                       len(graph.Nodes), "edge_count": len(graph.Edges),
		"risk_count": len(graph.Risks), "risk_kind_counts": riskKinds,
		"coverage": graph.Coverage, "adapters": NonNilStrings(graph.Adapters),
		"variants_included": NonNilStrings(graph.Included), "variants_omitted": NonNilStrings(graph.Omitted),
		"recommend_full": graph.RecommendFull, "details_truncated": truncated,
	}, truncated
}

// CompactVerificationResult bounds every list and output a verification
// result carries; the second result is the sorted set of evidence IDs
// across its stages.
func CompactVerificationResult(result workspacecore.VerificationResult, verbose bool) (map[string]any, []string) {
	compacted := map[string]any{"revision": result.Revision}
	var evidenceIDs []string
	detailsTruncated := false
	stages := make([]any, 0, len(result.Stages))
	for _, stage := range result.Stages {
		stageData, truncated := compactVerificationStage(stage, verbose)
		detailsTruncated = detailsTruncated || truncated
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
		targeted, truncated := compactTargetedTests(result.Targeted)
		compacted["targeted_tests"] = targeted
		detailsTruncated = detailsTruncated || truncated
	}
	if len(result.ToolDelta) > 0 {
		compacted["tool_delta"] = compactToolDelta(result.ToolDelta)
		compacted["tool_delta_count"] = len(result.ToolDelta)
		compacted["tool_delta_truncated"] = len(result.ToolDelta) > VerificationListLimit
		detailsTruncated = true
	}
	if result.FullTestGate != "" {
		compacted["full_test_gate"] = result.FullTestGate
	}
	compacted["details_truncated"] = detailsTruncated
	sort.Strings(evidenceIDs)
	return compacted, NonNilStrings(UniqueStrings(evidenceIDs))
}

// compactVerificationStage bounds one stage's output and lists; the second
// result reports whether anything was cut.
func compactVerificationStage(stage workspacecore.VerificationStage, verbose bool) (map[string]any, bool) {
	output := stage.Output
	outputTruncated := len(output) > VerificationOutputLimit
	if outputTruncated {
		output = output[:VerificationOutputLimit]
	}
	scope, scopeTruncated := boundedVerificationStrings(stage.Scope)
	writes, writesTruncated := boundedVerificationStrings(stage.Writes)
	executed, executedTruncated := boundedVerificationStrings(stage.ExecutedTests)
	selected, selectedTruncated := compactSelectedTests(stage.SelectedTests)
	truncated := outputTruncated || scopeTruncated || writesTruncated || executedTruncated || selectedTruncated
	if !verbose {
		// The default stage record is what an agent acts on: the verdict,
		// the output when the stage did not pass, why it was skipped, and
		// the test counts. Mode, revision, coverage and the per-item lists
		// are the verbose record.
		record := map[string]any{"stage": stage.Stage, "status": stage.Status, "exit": stage.Exit, "duration_ms": stage.DurationMS}
		if stage.Status != workspacecore.VerificationPassed {
			record["output"], record["output_truncated"] = output, outputTruncated
		} else if len(stage.Output) > 0 {
			// A passing stage shows the end of its output, which is where a
			// command says what it did ("ok example.com/ledger 0.01s", "5
			// passed in 0.3s"). Without it a caller has a verdict and no
			// evidence, and agents were observed running the same command
			// again in a shell to see it. The rest stays behind verbose and
			// evidence_get.
			record["output_tail"] = outputTail(stage.Output)
			record["output_bytes"], record["output_truncated"] = len(stage.Output), true
			outputTruncated = true
		}
		if !stage.Coverage.Complete && len(stage.Coverage.Skipped) > 0 {
			record["skipped"] = stage.Coverage.Skipped
		}
		record["test_scope"], record["test_verdict"] = stage.TestScope, stage.TestVerdict
		record["executed_test_count"], record["selected_test_count"] = len(stage.ExecutedTests), len(stage.SelectedTests)
		record["writes"], record["writes_count"] = writes, len(stage.Writes)
		// The affected scope says which files the stage covered; it is
		// short and it is the answer to "did this check my change".
		record["scope"], record["scope_count"] = scope, len(stage.Scope)
		return pruneEmpty(record), outputTruncated || scopeTruncated || writesTruncated
	}
	return pruneEmpty(map[string]any{
		"stage": stage.Stage, "mode": stage.Mode, "started_revision": stage.StartedRevision,
		"exit": stage.Exit, "status": stage.Status, "duration_ms": stage.DurationMS,
		"coverage": stage.Coverage, "evidence_ids": NonNilStrings(stage.EvidenceIDs),
		"scope": scope, "scope_count": len(stage.Scope), "scope_truncated": scopeTruncated,
		"writes": writes, "writes_count": len(stage.Writes), "writes_truncated": writesTruncated,
		"output": output, "output_bytes": len(stage.Output), "output_truncated": outputTruncated,
		"test_scope": stage.TestScope, "test_verdict": stage.TestVerdict,
		"selected_tests": selected, "selected_test_count": len(stage.SelectedTests),
		"selected_tests_truncated": selectedTruncated,
		"executed_tests":           executed, "executed_test_count": len(stage.ExecutedTests),
		"executed_tests_truncated": executedTruncated,
	}), truncated
}

// VerificationTailLines and VerificationTailBytes bound the evidence a
// passing stage shows: enough for a command's closing summary, never enough
// to be the output itself.
const (
	VerificationTailLines = 3
	VerificationTailBytes = 240
	// tailMarker opens a tail that had to be cut, and counts against the
	// byte bound so the field never exceeds it.
	tailMarker = "..."
)

// outputTail is the last few non-empty lines of a command's output, oldest
// first, bounded in lines and bytes.
func outputTail(output string) string {
	var lines []string
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimRight(line, "\r \t"); strings.TrimSpace(trimmed) != "" {
			lines = append(lines, trimmed)
		}
	}
	if len(lines) > VerificationTailLines {
		lines = lines[len(lines)-VerificationTailLines:]
	}
	tail := strings.Join(lines, "\n")
	if len(tail) > VerificationTailBytes {
		// Keep the end, marker included in the bound: the last line is the
		// one that summarises. The cut moves forward to a rune boundary so
		// the reply stays valid UTF-8.
		cut := len(tail) - VerificationTailBytes + len(tailMarker)
		for cut < len(tail) && !utf8.RuneStart(tail[cut]) {
			cut++
		}
		tail = tailMarker + tail[cut:]
	}
	return tail
}

// CompactOutline is the outline an agent acts on: one entry per declaration
// with its name, kind, line range and the handle that addresses it. The full
// handle records - hashes, anchors, revisions, one per declaration - stay
// behind their ids, which read and edit_apply accept on their own. Listing
// them made the outline of a fifty-declaration file larger than the file.
// MaxOutlineSections bounds the declarations one outline lists. A generated
// file can hold thousands of them, and the outline is what an agent reads
// instead of the file: a list that long is not a cheaper way to see the file,
// it is the file again under another name.
const MaxOutlineSections = 200

func CompactOutline(outline workspacecore.Outline, content []byte) map[string]any {
	listed := outline.Sections
	if len(listed) > MaxOutlineSections {
		listed = listed[:MaxOutlineSections]
	}
	sections := make([]map[string]any, 0, len(listed))
	for index, section := range listed {
		entry := map[string]any{
			"name": section.Name, "kind": section.Kind,
			"start_line": lineOfByte(content, section.ByteStart),
			"end_line":   endLineOfByte(content, section.ByteEnd),
		}
		if index < len(outline.Handles) {
			entry["handle"] = outline.Handles[index].Handle
		}
		sections = append(sections, entry)
	}
	compact := map[string]any{
		"path": outline.Path, "sections": sections,
		// declaration_count is what the file holds, listed_count is what this
		// reply carries: an outline that was cut says so here rather than
		// reading as a complete list of a shorter file.
		"declaration_count": len(outline.Sections), "listed_count": len(sections),
		"sections_truncated": len(sections) < len(outline.Sections),
		"coverage":           outline.Coverage,
	}
	// A document nothing could section answers a handle to the whole of it,
	// which is the only way to address it.
	if outline.FallbackHandle != nil {
		compact["fallback_handle"] = outline.FallbackHandle.Handle
		compact["fallback_range"] = outline.Fallback
	}
	return compact
}

// endLineOfByte is the last line a declaration occupies. A declaration that
// ends at a line boundary ends on the line before it, not on the empty start
// of the next one.
func endLineOfByte(content []byte, offset int) int {
	if offset > 0 && offset <= len(content) && content[offset-1] == '\n' {
		offset--
	}
	return lineOfByte(content, offset)
}

// lineOfByte is the 1-based line a byte offset falls on.
func lineOfByte(content []byte, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	return 1 + strings.Count(string(content[:offset]), "\n")
}

// pruneEmpty drops the keys of a compact record whose values carry no
// information: empty strings and lists, false flags and zero counts. The
// stage name, status, exit code and duration always stay.
func pruneEmpty(record map[string]any) map[string]any {
	keep := map[string]bool{"stage": true, "status": true, "exit": true, "duration_ms": true, "mode": true}
	for key, value := range record {
		if keep[key] {
			continue
		}
		switch typed := value.(type) {
		case nil:
			delete(record, key)
		case string:
			if typed == "" {
				delete(record, key)
			}
		case bool:
			if !typed {
				delete(record, key)
			}
		case int:
			if typed == 0 {
				delete(record, key)
			}
		case []string:
			if len(typed) == 0 {
				delete(record, key)
			}
		case []any:
			if len(typed) == 0 {
				delete(record, key)
			}
		}
	}
	return record
}

// compactSelectedTests keeps the first VerificationListLimit selected
// tests with their reasons and variants.
func compactSelectedTests(tests []workspacecore.SelectedTest) ([]any, bool) {
	kept := min(len(tests), VerificationListLimit)
	selected := make([]any, 0, kept)
	for _, test := range tests[:kept] {
		selected = append(selected, map[string]any{
			"name": test.Name, "reasons": NonNilStrings(test.Reasons), "variants": NonNilStrings(test.Variants),
		})
	}
	return selected, len(tests) > VerificationListLimit
}

func compactTargetedTests(targeted *workspacecore.TargetedTestResult) (map[string]any, bool) {
	selected, selectedTruncated := compactSelectedTests(targeted.Selected)
	executed, executedTruncated := boundedVerificationStrings(targeted.Executed)
	truncated := selectedTruncated || executedTruncated || len(targeted.Graph.Nodes) > 0 ||
		len(targeted.Graph.Edges) > 0 || len(targeted.Graph.Risks) > 0
	return map[string]any{
		"status": targeted.Status, "selected": selected,
		"selected_count": len(targeted.Selected), "selected_truncated": selectedTruncated,
		"executed": executed, "executed_count": len(targeted.Executed),
		"executed_truncated": executedTruncated,
		"graph_revision":     targeted.Graph.Revision,
	}, truncated
}

// compactToolDelta describes each tool-written path by its kinds, modes and
// sizes, never its bytes, and keeps the first VerificationListLimit.
func compactToolDelta(deltas []workspacecore.ToolDelta) []any {
	kept := min(len(deltas), VerificationListLimit)
	compact := make([]any, 0, kept)
	for _, delta := range deltas[:kept] {
		compact = append(compact, map[string]any{
			"path": delta.Path, "classification": delta.Classification,
			"before_exists": delta.BeforeExists, "after_exists": delta.AfterExists,
			"before_kind": delta.BeforeKind, "after_kind": delta.AfterKind,
			"before_mode": delta.BeforeMode, "after_mode": delta.AfterMode,
			"before_target": delta.BeforeTarget, "after_target": delta.AfterTarget,
			"before_bytes": len(delta.Before), "after_bytes": len(delta.After),
		})
	}
	return compact
}
