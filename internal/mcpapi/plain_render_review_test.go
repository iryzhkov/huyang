package mcpapi

import (
	"strings"
	"testing"
)

// Regressions for the M8 review rounds.

// A clean applied edit leaves its ordinary evidence IDs out, but an unknown
// field of the evidence record is still printed.
func TestPlainCleanEditKeepsUnknownEvidenceFields(t *testing.T) {
	edit := func(evidence map[string]any) string {
		envelope := okEnvelope("Edited a.go; no new diagnostics", map[string]any{
			"canonical_changed": true, "changed_paths": []any{"a.go"}, "revision": "wsrev_3", "document_revision": "docrev_a",
		})
		envelope["evidence"] = evidence
		return plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "replace_literal"}}, envelope)
	}
	guarded := edit(map[string]any{"ids": []any{"ev_1"}, "truncated": false, "future_guard": "g_1"})
	requireText(t, guarded, "Edited a.go (revision=wsrev_3; revision_id=docrev_a)\n"+`evidence detail: {"future_guard":"g_1"}`+"\n")
	requireAbsent(t, guarded, "ev_1")
	ordinary := edit(map[string]any{"ids": []any{"ev_1"}, "truncated": false})
	requireText(t, ordinary, "Edited a.go (revision=wsrev_3; revision_id=docrev_a)\n")
}

// An unknown entry field named index keeps its own value; the entry's
// position is printed outside the source fields.
func TestPlainBatchEntryFieldNamedIndexKeepsItsValue(t *testing.T) {
	batch := okEnvelope("Read 2 targets", map[string]any{
		"entries": []any{
			map[string]any{"path": "a.txt", "bytes": 2, "lines": 1},
			map[string]any{"path": "b.txt", "bytes": 2, "lines": 1, "index": "opaque-token"},
		},
		"files": []any{
			map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1},
			map[string]any{"path": "b.txt", "content": "b\n", "revision_id": "docrev_b", "lines": 1},
		},
	})
	got := plainFor(t, "read", nil, batch)
	requireContains(t, got, `entries[1] detail: {"index":"opaque-token"}`+"\n")
	requireAbsent(t, got, "entries[0]", `"index":1`)
}

func requireAbsent(t *testing.T, got string, unwanted ...string) {
	t.Helper()
	for _, text := range unwanted {
		if strings.Contains(got, text) {
			t.Fatalf("plain reply contains %q:\n%s", text, got)
		}
	}
}

// A batch continuation repeats the original target and the batch's
// effective options: byte offsets and line numbers count within that
// selection, so a bare target.path would read unrelated bytes under the
// same revision guard.
func TestPlainBatchContinuationRepeatsTheOriginalSelection(t *testing.T) {
	symbol := map[string]any{
		"path": "b.go", "name_path": "Big", "kind": "function_declaration", "content": "10\tfu", "revision_id": "docrev_b",
		"handle": "sym_b", "start_line": 10, "end_line": 20, "selection_bytes": 50, "delivered_bytes": 4, "byte_offset": 0,
		"byte_end": 4, "max_bytes": 4, "truncated": true, "byte_truncated": true, "next_byte_offset": 4,
		"continuation": map[string]any{"byte_offset": 4, "expected_revision_id": "docrev_b", "max_bytes": 4},
	}
	batch := func(files ...any) map[string]any {
		entries := make([]any, len(files))
		for index := range files {
			entries[index] = map[string]any{"path": "x"}
		}
		return okEnvelope("Read targets", map[string]any{"entries": entries, "files": files})
	}
	arguments := map[string]any{
		"targets":  []any{map[string]any{"path": "a.txt"}, map[string]any{"symbol_locator": map[string]any{"path": "b.go", "name_path": "Big"}}},
		"numbered": true, "max_bytes": 4,
	}
	got := plainFor(t, "read", arguments, batch(map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1}, symbol))
	requireContains(t, got, `continue: read targets=[{"byte_offset":4,"expected_revision_id":"docrev_b","max_bytes":4,"numbered":true,"symbol_locator":{"name_path":"Big","path":"b.go"}}]`+"\n")
	requireAbsent(t, got, "target.path")

	lines := map[string]any{"path": "c.txt", "content": "5\tx\n", "revision_id": "docrev_c", "lines": 300, "start_line": 5, "end_line": 200,
		"truncated": true, "delivered_end_line": 14}
	windowed := map[string]any{"targets": []any{map[string]any{"path": "c.txt", "start_line": 5, "end_line": 200}}, "numbered": true, "max_lines": 10}
	got = plainFor(t, "read", windowed, batch(lines))
	requireContains(t, got, "c.txt (revision_id=docrev_c; lines=5-14/300; truncated=max_lines)\n",
		`continue: read targets=[{"end_line":200,"max_lines":10,"numbered":true,"path":"c.txt","start_line":15}]`+"\n")

	// Without the original call the step says to repeat the same target
	// rather than inventing one.
	got = plainFor(t, "read", nil, batch(symbol))
	requireContains(t, got, "continue: repeat target 0 with the same selection and options, adding byte_offset=4 expected_revision_id=docrev_b max_bytes=4\n")
}

// A notice that repeats a delta finding with the same kind still prints its
// attribution.
func TestPlainSameKindNoticeKeepsItsAttribution(t *testing.T) {
	envelope := okEnvelope("Edited a.go", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"a.go"}, "revision": "wsrev_3", "document_revision": "docrev_a",
		"diagnostic_delta": map[string]any{"new": []any{map[string]any{"id": "d1", "severity": 1, "path": "a.go", "line": 4, "message": "boom"}}, "resolved": []any{}},
	})
	envelope["diagnostic_updates"] = []any{map[string]any{"id": "d1", "kind": "new", "severity": 1, "path": "a.go", "line": 4, "message": "boom",
		"attribution": map[string]any{"rank": "ambiguous", "candidates": []any{"edit_a", "edit_b"}}}}
	got := plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "replace_literal"}}, envelope)
	requireContains(t, got, "a.go:4: error: boom (attribution: candidates=edit_a,edit_b rank=ambiguous)\n")
	if strings.Count(got, "boom") != 1 {
		t.Fatalf("the finding was printed more than once:\n%s", got)
	}
}

// The last state of a finding wins: one resolved by the delta and announced
// again by a later notice is current, and one announced by the delta and
// resolved by a later notice is resolved.
func TestPlainLastFindingStateWins(t *testing.T) {
	edit := func(delta map[string]any, update map[string]any) string {
		envelope := okEnvelope("Edited a.go", map[string]any{
			"canonical_changed": true, "changed_paths": []any{"a.go"}, "revision": "wsrev_3", "diagnostic_delta": delta,
		})
		envelope["diagnostic_updates"] = []any{update}
		return plainFor(t, "edit_apply", nil, envelope)
	}
	resurrected := edit(map[string]any{"new": []any{}, "resolved": []any{"d"}},
		map[string]any{"id": "d", "kind": "new", "severity": 1, "path": "a.go", "line": 2, "message": "back"})
	requireContains(t, resurrected, "a.go:2: error: back (late finding; not attributed to this call)\n")
	requireAbsent(t, resurrected, "resolved:")

	resolved := edit(map[string]any{"new": []any{map[string]any{"id": "d", "severity": 1, "path": "a.go", "line": 2, "message": "gone"}}, "resolved": []any{}},
		map[string]any{"id": "d", "kind": "resolved", "path": "a.go"})
	requireContains(t, resolved, "resolved: 1 finding(s)\n")
	requireAbsent(t, resolved, "gone")
}

// A diagnostic delta is merged with the notices on refusals and on tools
// without a dedicated renderer too, never serialized beside them as JSON.
func TestPlainDeltaFindingsMergeOnEveryRenderingPath(t *testing.T) {
	plan := okEnvelope("Plan applied", map[string]any{
		"plan_id":          "plan_1",
		"diagnostic_delta": map[string]any{"new": []any{map[string]any{"id": "d", "severity": 1, "path": "a.go", "line": 2, "message": "gone"}}, "resolved": []any{}, "future": 1},
	})
	plan["diagnostic_updates"] = []any{map[string]any{"id": "d", "kind": "resolved", "path": "a.go"}}
	got := plainFor(t, "change_plan", nil, plan)
	requireContains(t, got, `data: {"plan_id":"plan_1"}`, "resolved: 1 finding(s)\n", `diagnostic_delta detail: {"future":1}`)
	requireAbsent(t, got, "gone", `"diagnostic_delta":`)

	refusal := map[string]any{
		"api_version": APIVersion, "request_id": "req_2", "outcome": "conflict", "code": "literal_count_mismatch",
		"summary": "operation 1: old text occurs 0 time(s)",
		"data": map[string]any{"failed_operation": 1, "applied_operations": 1, "canonical_changed": true, "changed_paths": []any{"x.go"},
			"diagnostic_delta": map[string]any{"new": []any{map[string]any{"id": "e", "severity": 2, "path": "x.go", "line": 1, "message": "unused"}}, "resolved": []any{}}},
		"workspace": map[string]any{"id": testWorkspaceID, "revision": "wsrev_4"},
	}
	got = plainFor(t, "edit_apply", nil, refusal)
	requireContains(t, got, "operation 1 refused; operations 0-0 stay applied", "x.go:1: warning: unused\n")
	requireAbsent(t, got, `"diagnostic_delta"`)
}

// Requested context does not hide the lines of a multi-line match past the
// window the handler bounds around its first line.
func TestPlainContextKeepsTheWholeMultilineMatch(t *testing.T) {
	search := func(hit map[string]any) string {
		return plainFor(t, "search", nil, okEnvelope("1 match", map[string]any{
			"hits": []any{hit}, "returned": 1, "total": 1,
			"result_set": map[string]any{"handle": "set_m", "match_count": 1, "file_count": 1},
		}))
	}
	requireText(t, search(map[string]any{"path": "f", "line": 3, "source_line": "a\nb\nc\nd", "context": "2\tz\n3\ta\n4\tb\n"}),
		"f-2-z\nf:3:a\nf:4:b\nf:5:c\nf:6:d\n1 match in 1 file; complete\n")
	// Without line text the omitted tail is reported, never silently cut.
	requireText(t, search(map[string]any{"path": "g", "line": 3, "match": "a\nb\nc", "context": "2\tz\n3\ta\n4\tb\n"}),
		"g-2-z\ng:3:a\ng:4:b\n  match continues to line 5; its text past the context is not shown, read the file for it\n1 match in 1 file; complete\n")
}

// A nested record that a renderer summarises keeps any field it does not
// know: coverage, transaction, evidence, batch entries, top-level overview
// entries and service limits.
func TestPlainNestedUnknownFieldsAreKept(t *testing.T) {
	read := okEnvelope("Read a.txt", map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1,
		"coverage": map[string]any{"complete": true, "capped": false, "future": "x"}})
	requireContains(t, plainFor(t, "read", nil, read), `coverage: {"capped":false,"complete":true,"future":"x"}`)

	plan := okEnvelope("Plan ready", map[string]any{"plan_id": "plan_1"})
	plan["transaction"] = map[string]any{"id": "plan_1", "state": "READY", "future_lock": "l"}
	plan["evidence"] = map[string]any{"ids": []any{"ev_1"}, "truncated": false, "future_window": 3}
	requireContains(t, plainFor(t, "change_plan", nil, plan),
		`transaction plan_1 state=READY {"future_lock":"l"}`, `evidence: ev_1 {"future_window":3}`)

	batch := okEnvelope("Read 1 targets", map[string]any{
		"entries": []any{map[string]any{"path": "a.txt", "bytes": 2, "lines": 1, "future_digest": "h"}},
		"files":   []any{map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1}},
	})
	requireContains(t, plainFor(t, "read", nil, batch), `entries[0] detail: {"future_digest":"h"}`+"\n")

	open := map[string]any{
		"api_version": APIVersion, "request_id": "req_3", "outcome": "ok", "summary": "Opened",
		"data": map[string]any{"revision": "wsrev_1",
			"overview":       map[string]any{"entry_count": 1, "top_level": []any{map[string]any{"path": "a.txt", "kind": "regular_text", "future": true}}},
			"service_limits": map[string]any{"tool_call_timeout_ms": 120000, "future_limit": 5},
		},
		"workspace": map[string]any{"id": testWorkspaceID, "kind": "project", "root": "/r"},
	}
	requireContains(t, plainFor(t, "workspace_open", nil, open), `top level: a.txt{"future":true}`, `service limits detail: {"future_limit":5}`)
}
