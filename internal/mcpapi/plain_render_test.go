package mcpapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	testDocRevision = "docrev_6a8509e15438b9978250ba98989ae0579bd75d0f29a9e68adcbc4a470f2269df"
	testWorkspaceID = "ws_48a0f6403b124c2f43e132c3a2dbe612"
)

func plainFor(t *testing.T, tool string, arguments, envelope map[string]any) string {
	t.Helper()
	text, err := RenderPlain(tool, arguments, envelope)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func okEnvelope(summary string, data map[string]any) map[string]any {
	return map[string]any{
		"api_version": APIVersion, "request_id": "req_1", "outcome": "ok", "summary": summary,
		"data": data, "warnings": []string{}, "next": []any{},
		"workspace": map[string]any{"id": testWorkspaceID, "revision": "wsrev_32"},
	}
}

func requireText(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("plain reply:\n%s\nwant:\n%s", got, want)
	}
}

func requireContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Fatalf("plain reply lacks %q:\n%s", want, got)
		}
	}
}

func jsonTextBytes(t *testing.T, tool string, envelope map[string]any) int {
	t.Helper()
	encoded, err := RenderJSON(CompactTextEnvelope(TrimWireDiffs(tool, envelope)))
	if err != nil {
		t.Fatal(err)
	}
	return len(encoded)
}

// The five-line read of the reply design: a header with the full revision
// and the delivered range, then the exact text, smaller than its JSON text.
func TestPlainReadHeaderKeepsTheFullGuardAndExactText(t *testing.T) {
	envelope := okEnvelope("Read go.mod", map[string]any{
		"content": "module github.com/iryzhkov/huyang\n\ngo 1.27\n\nrequire (\n", "end_line": 5, "lines": 25,
		"path": "go.mod", "revision_id": testDocRevision, "start_line": 1,
	})
	got := plainFor(t, "read", nil, envelope)
	requireText(t, got, "go.mod (revision_id="+testDocRevision+"; lines=1-5/25)\nmodule github.com/iryzhkov/huyang\n\ngo 1.27\n\nrequire (\n")
	if len(got) >= jsonTextBytes(t, "read", envelope) {
		t.Fatalf("plain read is %d bytes, not smaller than its JSON text", len(got))
	}
}

func TestPlainReadKeepsTrailingBlankLinesEmptyFilesAndMissingNewlines(t *testing.T) {
	for name, test := range map[string]struct {
		data map[string]any
		want string
	}{
		"blank tail": {map[string]any{"path": "a.txt", "content": "x\n\n\n", "revision_id": "docrev_a", "lines": 3}, "a.txt (revision_id=docrev_a; lines=1-3/3)\nx\n\n\n"},
		"empty":      {map[string]any{"path": "e.txt", "content": "", "revision_id": "docrev_e", "lines": 0}, "e.txt (revision_id=docrev_e; lines=0)\n"},
		"no newline": {map[string]any{"path": "n.txt", "content": "a\nb", "revision_id": "docrev_n", "lines": 2}, "n.txt (revision_id=docrev_n; lines=1-2/2; no_final_newline)\na\nb\n"},
		"odd path":   {map[string]any{"path": "my file.txt", "content": "z\n", "revision_id": "docrev_o", "lines": 1}, "\"my file.txt\" (revision_id=docrev_o; lines=1-1/1)\nz\n"},
	} {
		t.Run(name, func(t *testing.T) {
			requireText(t, plainFor(t, "read", nil, okEnvelope("Read", test.data)), test.want)
		})
	}
}

// A read cut at max_lines names the delivered range, not the requested one,
// and its continuation is a copyable call.
func TestPlainCappedReadNamesTheDeliveredRangeAndContinuation(t *testing.T) {
	envelope := okEnvelope("Read big.go: lines 1 to 80 of 412; truncated at max_lines", map[string]any{
		"path": "big.go", "content": "l1\n", "revision_id": "docrev_b", "lines": 412, "truncated": true, "delivered_end_line": 80,
	})
	envelope["next"] = []any{
		map[string]any{"tool": "read", "action": "outline_then_window_what_matters", "path": "big.go", "view": "outline"},
		map[string]any{"tool": "read", "action": "continue_from_line", "path": "big.go", "start_line": 81},
	}
	requireText(t, plainFor(t, "read", nil, envelope),
		"big.go (revision_id=docrev_b; lines=1-80/412; truncated=max_lines)\nl1\n"+
			`next: read target.path="big.go" view="outline" (outline_then_window_what_matters); read target.path="big.go" start_line=81 (continue_from_line)`+"\n")
}

func TestPlainByteWindowKeepsItsContinuationGuard(t *testing.T) {
	envelope := okEnvelope("Read min.js; truncated at max_bytes (4 of 10 selected bytes)", map[string]any{
		"path": "min.js", "content": "abcd", "revision_id": "docrev_m", "lines": 1,
		"selection_bytes": 10, "delivered_bytes": 4, "byte_offset": 0, "byte_end": 4, "max_bytes": 4,
		"truncated": true, "byte_truncated": true, "next_byte_offset": 4,
		"continuation": map[string]any{"byte_offset": 4, "expected_revision_id": "docrev_m", "max_bytes": 4},
	})
	envelope["next"] = []any{map[string]any{"tool": "read", "action": "repeat_same_target_and_options_with_byte_offset", "byte_offset": 4, "expected_revision_id": "docrev_m", "max_bytes": 4}}
	requireText(t, plainFor(t, "read", nil, envelope),
		"min.js (revision_id=docrev_m; lines=1-1/1; bytes=0-4/10; truncated=max_bytes; no_final_newline)\nabcd\n"+
			`next: read byte_offset=4 expected_revision_id="docrev_m" max_bytes=4 (repeat_same_target_and_options_with_byte_offset)`+"\n")
}

func TestPlainSymbolAndPreparedReads(t *testing.T) {
	symbol := plainFor(t, "read", nil, okEnvelope("Read symbol", map[string]any{
		"path": "a.go", "name_path": "Ledger/Balance", "kind": "method_declaration", "content": "func (l Ledger) Balance() {}\n",
		"revision_id": "docrev_s", "handle": "sym_1", "start_line": 10, "end_line": 10,
	}))
	requireText(t, symbol, "a.go#Ledger/Balance (revision_id=docrev_s; kind=method_declaration; lines=10-10; handle=sym_1)\nfunc (l Ledger) Balance() {}\n")
	prepared := plainFor(t, "read", nil, okEnvelope("Read a.go at prep_abc", map[string]any{
		"path": "a.go", "content": "x\n", "lines": 1, "start_line": 1, "end_line": 1,
		"revision_id": "prep_abc", "plan_id": "plan_1", "plan_revision": 2, "handle": "prep_abc_0011_a.go",
	}))
	requireContains(t, prepared, "a.go (prepared_revision=prep_abc; plan_id=plan_1; plan_revision=2; lines=1-1/1; handle=prep_abc_0011_a.go)\n")
	if strings.Contains(prepared, "revision_id=prep") {
		t.Fatalf("a prepared read was labelled with a canonical revision_id:\n%s", prepared)
	}
}

// A batch keeps every successful target and prints a failed one as its own
// refusal with its recovery.
func TestPlainReadBatchKeepsSuccessfulSiblingsAndLeafRecovery(t *testing.T) {
	envelope := okEnvelope("Read 1 of 2 targets; 1 failed", map[string]any{
		"entries": []any{map[string]any{"path": "a.txt", "bytes": 2, "lines": 1}, map[string]any{"path": "missing.txt", "code": "document_not_found"}},
		"files": []any{
			map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1},
			map[string]any{"path": "missing.txt", "code": "document_not_found", "error": "No file at missing.txt; the workspace holds docs/missing.txt",
				"next": []any{map[string]any{"tool": "read", "action": "read_candidate", "path": "docs/missing.txt"}}, "candidates": []any{"docs/missing.txt"}},
		},
	})
	envelope["outcome"] = "partial"
	envelope["next"] = []any{map[string]any{"tool": "read", "action": "read_candidate", "path": "docs/missing.txt", "target_index": 1, "target_path": "missing.txt"}}
	got := plainFor(t, "read", nil, envelope)
	requireContains(t, got,
		"a.txt (revision_id=docrev_a; lines=1-1/1)\na\n",
		"missing.txt: document_not_found: No file at missing.txt; the workspace holds docs/missing.txt\n",
		"candidates: docs/missing.txt\n",
		"partial: Read 1 of 2 targets; 1 failed\n",
		`next: read target.path="docs/missing.txt" target_index=1 target_path="missing.txt" (read_candidate)`,
	)
}

func TestPlainOutlineListsDeclarationsWithHandles(t *testing.T) {
	got := plainFor(t, "read", nil, okEnvelope("Outline read: 2 declarations, the first 1 listed; truncated at the outline bound", map[string]any{
		"path": "a.go", "declaration_count": 2, "listed_count": 1, "sections_truncated": true,
		"sections": []any{map[string]any{"name": "main", "kind": "function_declaration", "start_line": 3, "end_line": 5, "handle": "sym_a"}},
		"coverage": map[string]any{"complete": true, "capped": false, "semantic": "parser_sections"},
	}))
	requireText(t, got, "a.go outline (1 of 2 declarations; truncated)\n3-5 function_declaration main handle=sym_a\n")
}

// The search of the reply design, with the line text the handler attaches.
func TestPlainSearchPrintsGrepRowsContextAndFooter(t *testing.T) {
	envelope := okEnvelope("1 match", map[string]any{
		"hits":       []any{map[string]any{"context": "2\t\n3\tgo 1.27\n4\t\n", "line": 3, "path": "go.mod", "source_line": "go 1.27"}},
		"result_set": map[string]any{"file_count": 1, "handle": "set_771052aba0822331b117bd880e48efb7", "match_count": 1},
		"returned":   1, "total": 1,
	})
	requireText(t, plainFor(t, "search", nil, envelope), "go.mod-2-\ngo.mod:3:go 1.27\ngo.mod-4-\n1 match in 1 file; complete\n")
}

func TestPlainSearchMergesContextWindows(t *testing.T) {
	got := plainFor(t, "search", nil, okEnvelope("3 matches", map[string]any{
		"hits": []any{
			map[string]any{"path": "a", "line": 3, "context": "2\tb\n3\tc\n4\td\n"},
			map[string]any{"path": "a", "line": 4, "context": "3\tc\n4\td\n5\te\n"},
			map[string]any{"path": "a", "line": 9, "context": "8\th\n9\ti\n10\tj\n"},
		},
		"result_set": map[string]any{"file_count": 1, "handle": "set_c", "match_count": 3}, "returned": 3, "total": 3,
	}))
	requireText(t, got, "a-2-b\na:3:c\na:4:d\na-5-e\n--\na-8-h\na:9:i\na-10-j\n3 matches in 1 file; complete\n")
}

// A limited result names the set handle that refines it; an acquisition
// that stopped at its bound says the count is a floor and the set cannot
// back an all-match edit.
func TestPlainSearchFooterKeepsTheHandleAndCompleteness(t *testing.T) {
	limited := okEnvelope("240 matches", map[string]any{
		"hits":       []any{map[string]any{"path": "a.go", "line": 41, "source_line": "    return foo()"}, map[string]any{"path": "b.go", "line": 12, "source_line": "foo := 1"}},
		"result_set": map[string]any{"handle": "set_x", "match_count": 240, "file_count": 12}, "returned": 2, "total": 240,
	})
	limited["warnings"] = []string{"response limited to 2 of 240 matches"}
	limited["next"] = []any{map[string]any{"tool": "search", "action": "refine", "result_set_handle": "set_x"}}
	requireText(t, plainFor(t, "search", nil, limited),
		"a.go:41:    return foo()\nb.go:12:foo := 1\n2 of 240 matches in 12 files; complete; result_set=set_x\n"+
			"warning: response limited to 2 of 240 matches\n"+`next: search result_set_handle="set_x" (refine)`+"\n")

	capped := okEnvelope("at least 1000 matches", map[string]any{
		"hits": []any{map[string]any{"path": "a.go", "line": 1, "source_line": "foo"}}, "returned": 1, "total": 1000,
		"total_is_lower_bound": true, "match_limit": 1000,
		"result_set": map[string]any{"handle": "set_y", "match_count": 1000, "file_count": 3, "complete": false, "all_matches_eligible": false},
		"coverage":   map[string]any{"complete": false, "capped": true},
	})
	requireContains(t, plainFor(t, "search", nil, capped),
		"1 of at least 1000 matches in 3 files; incomplete; match_limit=1000; all_matches_eligible=false; result_set=set_y\n",
		"coverage incomplete: ")

	refined := okEnvelope("1 matches retained; 4 eliminated", map[string]any{
		"hits": []any{map[string]any{"path": "a.go", "line": 3, "source_line": "x := foo"}}, "returned": 1, "total": 1,
		"result_set": map[string]any{"handle": "set_c", "match_count": 1, "file_count": 1, "parent": "set_p", "retained": 1, "eliminated": 4},
	})
	requireText(t, plainFor(t, "search", nil, refined), "a.go:3:x := foo\n1 match in 1 file; complete; refined from set_p: retained 1, eliminated 4; result_set=set_c\n")
}

func TestPlainSearchHandlesAndUnavailableLineText(t *testing.T) {
	got := plainFor(t, "search", map[string]any{"include_handles": true}, okEnvelope("2 matches", map[string]any{
		"hits": []any{
			map[string]any{"path": "a.go", "line": 3, "source_line": "x foo", "column": 3, "handle": "rng_1"},
			map[string]any{"path": "b.go", "line": 7, "column": 1, "handle": "rng_2"},
		},
		"result_set": map[string]any{"handle": "set_h", "match_count": 2, "file_count": 2}, "returned": 2, "total": 2,
	}))
	requireText(t, got, "a.go:3:x foo\n  handle=rng_1 column=3\nb.go:7\n  handle=rng_2 column=1\n"+
		"note: line text unavailable for 1 hit(s): the document changed after the search; read it to see the current text\n"+
		"2 matches in 2 files; complete; result_set=set_h\n")
}

// A finding in both the edit's delta and the notice stream is printed once,
// with the delta's line; a unique late finding keeps its attribution; the
// evidence that addresses them is named.
func TestPlainEditMergesFindingsByIDAndKeepsLateAttribution(t *testing.T) {
	envelope := okEnvelope("Created internal/mcpapi/plain_render.go; 1 new diagnostic(s), 1 resolved", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"internal/mcpapi/plain_render.go"},
		"diagnostic_delta": map[string]any{
			"new": []any{map[string]any{"code": "MissingFieldOrMethod", "id": "diag_eb52", "line": 147, "message": "r.read undefined",
				"path": "internal/mcpapi/plain_render.go", "producer": "gopls", "severity": 1}},
			"resolved": []any{"diag_old"},
		},
		"document_revision": "docrev_3131", "from_revision": "wsrev_2", "revision": "wsrev_3",
	})
	envelope["evidence"] = map[string]any{"ids": []string{"ev_8c79"}, "truncated": false}
	envelope["diagnostic_updates"] = []any{
		map[string]any{"id": "diag_eb52", "kind": "new", "severity": 1, "path": "internal/mcpapi/plain_render.go", "line": 146, "message": "r.read undefined"},
		map[string]any{"id": "diag_late", "kind": "new", "severity": 2, "path": "a.go", "line": 9, "message": "unused variable",
			"attribution": map[string]any{"rank": "late", "candidates": []any{"edit_req_1"}, "reasons": []any{"earlier_transaction"}}},
	}
	got := plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "create_file"}}, envelope)
	requireText(t, got, "Created internal/mcpapi/plain_render.go (revision=wsrev_3; revision_id=docrev_3131)\n"+
		"internal/mcpapi/plain_render.go:147: error: r.read undefined [MissingFieldOrMethod] (gopls)\n"+
		"a.go:9: warning: unused variable (late finding; attribution: candidates=edit_req_1 rank=late reasons=earlier_transaction)\n"+
		"resolved: 1 finding(s)\n"+
		"evidence: ev_8c79\n")
}

// Late findings arrive on any tool, and an unattributed one is not claimed
// for the call that carried it.
func TestPlainLateFindingsOnReadsAndUnknownTools(t *testing.T) {
	read := okEnvelope("Read a.txt", map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1})
	read["diagnostic_updates"] = []any{map[string]any{"id": "d1", "kind": "new", "severity": 1, "path": "b.go", "line": 3, "message": "boom"}}
	read["diagnostic_updates_truncated"] = true
	requireText(t, plainFor(t, "read", nil, read), "a.txt (revision_id=docrev_a; lines=1-1/1)\na\n"+
		"b.go:3: error: boom (late finding; not attributed to this call)\n"+
		"findings truncated: more diagnostic changes than one reply carries; read them with diagnostics\n")

	inspect := okEnvelope("No new diagnostics", map[string]any{"diagnostics": map[string]any{"confidence": "authoritative", "new_count": 0}})
	inspect["diagnostic_updates"] = []any{
		map[string]any{"id": "d2", "kind": "persisting", "path": "c.go", "line": 1, "message": "still"},
		map[string]any{"id": "d3", "kind": "resolved", "path": "c.go"},
	}
	requireText(t, plainFor(t, "diagnostics", nil, inspect), "No new diagnostics\n"+
		`data: {"diagnostics":{"confidence":"authoritative","new_count":0}}`+"\n"+
		"c.go:1: severity unspecified: still (still present; not attributed to this call)\n"+
		"resolved: 1 finding(s)\n")
}

// An incomplete verdict is stated on every edit, even when its reasons
// repeat the previous reply's.
func TestPlainEditNeverSuppressesProvisionalReasons(t *testing.T) {
	envelope := okEnvelope("Rust diagnostics pending for a.rs; findings arrive through diagnostic_updates on subsequent replies", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"a.rs"}, "document_revision": "docrev_r", "revision": "wsrev_4", "from_revision": "wsrev_3",
		"verification": map[string]any{"confidence": "provisional", "reasons_unchanged": true, "reason_codes": []any{"rust_diagnostics_pending"}},
	})
	envelope["outcome"] = "provisional"
	envelope["next"] = []any{map[string]any{"tool": "diagnostics", "action": "read_late_diagnostic_evidence", "note": "The mutation already applied."}}
	got := plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "replace_literal"}}, envelope)
	requireText(t, got, "Edited a.rs (revision=wsrev_4; revision_id=docrev_r)\n"+
		"diagnostics provisional (reasons unchanged since your last reply) [rust_diagnostics_pending]\n"+
		"provisional: Rust diagnostics pending for a.rs; findings arrive through diagnostic_updates on subsequent replies\n"+
		"next: diagnostics (read_late_diagnostic_evidence) - The mutation already applied.\n")
	unavailable := okEnvelope("no language server is configured", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"a.go"}, "revision": "wsrev_5",
		"verification": map[string]any{"confidence": "unavailable", "reasons": []any{"lsp_not_configured"}},
	})
	unavailable["outcome"] = "provisional"
	requireContains(t, plainFor(t, "edit_apply", nil, unavailable), "diagnostics unavailable: lsp_not_configured\n")
}

func TestPlainEditPreviewReplayAndMultiplePaths(t *testing.T) {
	preview := okEnvelope("Preview of creating probe.txt; canonical bytes unchanged", map[string]any{
		"canonical_changed": false, "changed_paths": []any{"probe.txt"},
		"diffs":         []map[string]any{{"after_sha256": "5891", "before_sha256": "e3b0", "patch": "--- /dev/null\n+++ probe.txt\n@@ new file, 6 bytes @@\n", "path": "probe.txt"}},
		"from_revision": "wsrev_1", "revision": "wsrev_1",
	})
	preview["guide"] = []string{"Name the repository with root on any call."}
	preview["idempotency"] = "created"
	got := plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "create_file"}, "preview_only": true}, preview)
	requireText(t, got, "Preview: create probe.txt (revision=wsrev_1; canonical_changed=false)\n--- /dev/null\n+++ probe.txt\n@@ new file, 6 bytes @@\n")
	if len(got) >= jsonTextBytes(t, "edit_apply", preview) {
		t.Fatal("plain preview is not smaller than its JSON text")
	}

	replay := okEnvelope("Idempotent replay returned the original receipt; this call made no new mutation", map[string]any{
		"canonical_changed": false, "original_canonical_changed": true, "replayed_request": true,
		"changed_paths": []any{"note.txt"}, "document_revision": "docrev_x", "revision": "wsrev_2", "from_revision": "wsrev_1",
	})
	replay["idempotency"] = "replayed"
	requireText(t, plainFor(t, "edit_apply", nil, replay),
		"Replayed original receipt; no new mutation (original revision=wsrev_2; from_revision=wsrev_1; original_canonical_changed=true)\n"+
			"  note.txt revision_id=docrev_x (as of the original edit)\nCurrent revision unconfirmed; refresh before another mutation.\n")

	many := okEnvelope("Applied 2 operations", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"a.go", "b.go"}, "revision": "wsrev_9", "from_revision": "wsrev_8",
		"document_revisions": map[string]any{"a.go": "docrev_a", "b.go": "docrev_b"}, "replacements": 3,
		"locations": []any{"a.go:1:1", "b.go:2:1", "b.go:5:1"}, "format": map[string]any{"formatter": "gofmt", "reformatted": true},
	})
	requireText(t, plainFor(t, "edit_apply", map[string]any{"operations": []any{map[string]any{}, map[string]any{}}}, many),
		"Applied 2 operations to 2 paths (revision=wsrev_9; replacements=3)\n  a.go revision_id=docrev_a\n  b.go revision_id=docrev_b\n"+
			"at a.go:1:1, b.go:2:1, b.go:5:1\nformat: formatter=gofmt reformatted=true\n")

	deleted := okEnvelope("Deleted old.txt", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"old.txt"}, "revision": "wsrev_5", "from_revision": "wsrev_4",
		"git": map[string]any{"old.txt": "tracked"},
	})
	requireText(t, plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "delete_file"}}, deleted),
		"Deleted old.txt (revision=wsrev_5)\ngit: old.txt=tracked (Huyang does not touch the Git index)\n")
}

// A refused operation in a list says which operations stay applied, keeps
// the failed operation's own recovery, and resumes without the prefix.
func TestPlainPartialBatchKeepsAppliedPrefixAndLeafRecovery(t *testing.T) {
	envelope := map[string]any{
		"api_version": APIVersion, "request_id": "req_4", "outcome": "conflict", "code": "literal_count_mismatch",
		"summary": "operation 2: old text occurs 0 time(s) in a.go, expected 1; nothing changed",
		"data": map[string]any{
			"expected_count": 1, "found": 0, "locations": []any{}, "failed_operation": 2, "applied_operations": 2,
			"canonical_changed": true, "changed_paths": []any{"a.go"},
			"operation_recovery": []any{
				map[string]any{"tool": "edit_apply", "action": "retry_with_expected_count_or_a_narrower_path", "found": 0},
				map[string]any{"tool": "edit_apply", "action": "include_more_surrounding_text_in_old"},
			},
		},
		"warnings": []string{"operations 0 to 1 were applied and stay applied"},
		"next": []any{
			map[string]any{"tool": "edit_apply", "action": "retry_from_failed_operation_after_correcting_it", "failed_operation": 2, "skip_applied_operations": 2},
			map[string]any{"tool": "edit_apply", "action": "retry_with_expected_count_or_a_narrower_path", "found": 0},
		},
		"workspace": map[string]any{"id": testWorkspaceID, "revision": "wsrev_5"},
	}
	requireText(t, plainFor(t, "edit_apply", nil, envelope),
		"literal_count_mismatch: operation 2: old text occurs 0 time(s) in a.go, expected 1; nothing changed\n"+
			`operation 2 refused; operations 0-1 stay applied; canonical_changed=true; changed_paths=["a.go"]`+"\n"+
			"failed operation recovery: edit_apply found=0 (retry_with_expected_count_or_a_narrower_path); edit_apply (include_more_surrounding_text_in_old)\n"+
			"workspace revision=wsrev_5\n"+
			"expected_count=1 found=0\n"+
			"warning: operations 0 to 1 were applied and stay applied\n"+
			"next: edit_apply failed_operation=2 skip_applied_operations=2 (retry_from_failed_operation_after_correcting_it); edit_apply found=0 (retry_with_expected_count_or_a_narrower_path)\n")
}

// The refusal of the reply design: code, message and one copyable step.
func TestPlainRefusalsKeepCodeRecoveryAndActualText(t *testing.T) {
	missing := map[string]any{
		"api_version": APIVersion, "request_id": "req_173", "outcome": "failed", "code": "document_not_found",
		"summary": "No file at missing-reply-design-file, and no file of that name anywhere in the workspace",
		"data": map[string]any{"coverage": map[string]any{"complete": true, "files_considered": 913, "files_read": 0, "bytes_read": 0, "capped": false, "semantic": "parser_sections"},
			"path": "missing-reply-design-file"},
		"next":      []any{map[string]any{"action": "locate_the_file_by_name", "query": "missing-reply-design-file", "tool": "search"}},
		"warnings":  []string{},
		"workspace": map[string]any{"id": testWorkspaceID, "revision": "wsrev_32"},
	}
	got := plainFor(t, "read", nil, missing)
	requireText(t, got, "document_not_found: No file at missing-reply-design-file, and no file of that name anywhere in the workspace\n"+
		`next: search query="missing-reply-design-file" (locate_the_file_by_name)`+"\n")
	if len(got) >= jsonTextBytes(t, "read", missing) {
		t.Fatal("plain refusal is not smaller than its JSON text")
	}

	whitespace := map[string]any{
		"api_version": APIVersion, "request_id": "req_5", "outcome": "conflict", "code": "literal_whitespace_mismatch",
		"summary":   "old text matches only after collapsing whitespace: tabs; nothing changed",
		"data":      map[string]any{"locations": []any{"f.go:3:1"}, "actual": "\tif b {\n\t\treturn 2\n\t}\n"},
		"next":      []any{map[string]any{"tool": "edit_apply", "action": "retry_with_old_set_to_actual"}},
		"workspace": map[string]any{"id": testWorkspaceID, "revision": "wsrev_2"},
	}
	requireText(t, plainFor(t, "edit_apply", nil, whitespace),
		"literal_whitespace_mismatch: old text matches only after collapsing whitespace: tabs; nothing changed\n"+
			"workspace revision=wsrev_2\nactual:\n\tif b {\n\t\treturn 2\n\t}\nlocations: f.go:3:1\n"+
			"next: edit_apply (retry_with_old_set_to_actual)\n")

	timeout := map[string]any{
		"api_version": APIVersion, "request_id": "req_6", "outcome": "failed", "code": "request_timeout",
		"summary": "read exceeded the global 2m0s tool-call timeout; the operation was cancelled", "retryable": true,
		"data": map[string]any{"tool": "read", "timeout_ms": 120000, "retry_safe": true},
	}
	requireText(t, plainFor(t, "read", nil, timeout),
		"request_timeout: read exceeded the global 2m0s tool-call timeout; the operation was cancelled (retryable=true)\n"+
			"retry_safe=true timeout_ms=120000\nrequest_id=req_6\n")
}

func TestPlainUnpersistedReceiptIsLoud(t *testing.T) {
	envelope := okEnvelope("Edit applied, but its recovery receipt could not be persisted", map[string]any{
		"canonical_changed": true, "changed_paths": []any{"a.txt"}, "document_revision": "docrev_p", "revision": "wsrev_7",
	})
	envelope["outcome"], envelope["code"], envelope["request_id"] = "provisional", "mutation_receipt_persist_failed", "req_9"
	envelope["idempotency_persisted"] = false
	envelope["warnings"] = []string{"disk full"}
	requireContains(t, plainFor(t, "edit_apply", map[string]any{"operation": map[string]any{"kind": "replace_literal"}}, envelope),
		"Edited a.txt (revision=wsrev_7; revision_id=docrev_p)\n",
		"mutation_receipt_persist_failed: Edit applied, but its recovery receipt could not be persisted\n",
		"receipt not persisted: ", "request_id=req_9\n", "warning: disk full\n")
}

// The overviews of the reply design: a short first open and a one-line
// repeated one.
func TestPlainWorkspaceOpenIsAShortOverview(t *testing.T) {
	root := "/home/igor/.t3/worktrees/huyang/fix-local-model-friction"
	first := map[string]any{
		"api_version": APIVersion, "request_id": "req_168", "outcome": "ok", "summary": "Reopened project workspace with 913 entries",
		"data": map[string]any{
			"capabilities": map[string]any{"not_available": map[string]any{"formatter": "configured_untrusted", "lsp": "probe_with_language_server_status"}, "semantic": "parser_sections"},
			"commands": map[string]any{"check": []any{"go-vet: go vet ./..."}, "enable": "add this root to [trust] roots in /home/igor/.config/huyang/config.toml",
				"format_gate": "sh -c test -z \"$(gofmt -l cmd internal)\"", "source": ".huyang.toml", "state": "configured_untrusted",
				"tests": []any{"go-unit: go test ./..."}, "trusted": false},
			"overview": map[string]any{"entry_count": 913, "top_level": []any{
				map[string]any{"files": 1, "kind": "directory", "path": ".github"}, map[string]any{"kind": "regular_text", "path": "go.mod"},
			}, "top_level_count": 2, "top_level_truncated": false},
			"recent_commits":    map[string]any{"commits": []any{map[string]any{"abbreviated_id": "ef16bef", "subject": "Merge"}}},
			"revision":          "wsrev_32",
			"semantic_provider": map[string]any{"backend": "embed", "state": "healthy"},
			"service_limits":    map[string]any{"tool_call_timeout_ms": 120000},
		},
		"guide": []string{"Name the repository with root on any call."}, "next": []any{}, "warnings": []string{},
		"workspace": map[string]any{"id": testWorkspaceID, "kind": "project", "root": root, "epoch": 1, "state_seq": 32},
	}
	got := plainFor(t, "workspace_open", nil, first)
	requireText(t, got, "Workspace "+root+" (id="+testWorkspaceID+"; revision=wsrev_32; kind=project; 913 entries)\n"+
		"top level: .github/ go.mod\n"+
		"commands (.huyang.toml; configured_untrusted): check: go-vet: go vet ./...; tests: go-unit: go test ./...\n"+
		"format gate: sh -c test -z \"$(gofmt -l cmd internal)\"\n"+
		"enable: add this root to [trust] roots in /home/igor/.config/huyang/config.toml\n"+
		"capabilities: semantic=parser_sections; not available: formatter=configured_untrusted lsp=probe_with_language_server_status\n"+
		"provider: embed healthy\n")
	if len(got) >= jsonTextBytes(t, "workspace_open", first) {
		t.Fatal("plain overview is not smaller than its JSON text")
	}
	repeated := map[string]any{
		"api_version": APIVersion, "request_id": "req_169", "outcome": "ok", "summary": "Reopened project workspace with 913 entries; overview unchanged",
		"data": map[string]any{"entry_count": 913, "overview_repeated": true, "revision": "wsrev_32"}, "next": []any{}, "warnings": []string{},
		"workspace": map[string]any{"id": testWorkspaceID, "kind": "project", "root": root, "epoch": 1, "state_seq": 32},
	}
	requireText(t, plainFor(t, "workspace_open", nil, repeated),
		"Workspace "+root+" (id="+testWorkspaceID+"; revision=wsrev_32; 913 entries; overview unchanged)\n")
}

// A field no renderer knows is printed, never dropped: in data, in a
// finding, and on the envelope.
func TestPlainUnknownFieldsFallBackToDetail(t *testing.T) {
	envelope := okEnvelope("Read a.txt", map[string]any{"path": "a.txt", "content": "a\n", "revision_id": "docrev_a", "lines": 1, "future_guard": "g_1"})
	envelope["future_flag"] = true
	envelope["diagnostic_updates"] = []any{map[string]any{"id": "d1", "kind": "new", "severity": 1, "path": "b.go", "line": 2, "message": "m", "fix": "x"}}
	requireContains(t, plainFor(t, "read", nil, envelope),
		`detail: {"future_guard":"g_1"}`, `detail: {"future_flag":true}`, `b.go:2: error: m (late finding; not attributed to this call) {"fix":"x"}`)

	history := okEnvelope("2 provenance spans", map[string]any{"path": "a.go", "spans": []any{map[string]any{"start_line": 1, "end_line": 2, "commit": map[string]any{"handle": "commit_1"}}}})
	requireContains(t, plainFor(t, "read", nil, history), "2 provenance spans\n", `"handle":"commit_1"`)

	oddReceipt := okEnvelope("Something new", map[string]any{"future_receipt": map[string]any{"guard": "g"}})
	requireContains(t, plainFor(t, "edit_apply", nil, oddReceipt), "Something new\n", `data: {"future_receipt":{"guard":"g"}}`)
}

func TestPlainSourceLineIsBoundedAndFromTheGivenBytes(t *testing.T) {
	content := []byte("a\nfoo bar\nb\n")
	if text, cut, ok := PlainSourceLine(content, 6, 9); !ok || cut || text != "foo bar" {
		t.Fatalf("single line = %q %v %v", text, cut, ok)
	}
	multi := []byte("x\nalpha\nbeta\ny\n")
	if text, _, _ := PlainSourceLine(multi, 4, 10); text != "alpha\nbeta" {
		t.Fatalf("multi-line match = %q", text)
	}
	if text, _, _ := PlainSourceLine(multi, 2, 8); text != "alpha" {
		t.Fatalf("a match ending with its newline reached the next line: %q", text)
	}
	long := []byte(strings.Repeat("a", 600) + "NEEDLE" + strings.Repeat("b", 600) + "\n")
	text, cut, ok := PlainSourceLine(long, 600, 606)
	if !ok || !cut || !strings.Contains(text, "NEEDLE") || !strings.HasPrefix(text, "[...]") || !strings.HasSuffix(text, "[...]") || len(text) > MaxPlainLineBytes+10 {
		t.Fatalf("long line = %d bytes cut=%v: %q", len(text), cut, text)
	}
	wide := []byte(strings.Repeat("é", 400) + "\n")
	if text, _, _ := PlainSourceLine(wide, 400, 402); !utf8.ValidString(text) {
		t.Fatalf("cut split a character: %q", text)
	}
	if _, _, ok := PlainSourceLine(content, 5, 100); ok {
		t.Fatal("a span outside the content was accepted")
	}
}

func TestReplyStyleParsingAndConflicts(t *testing.T) {
	for name, want := range map[string]ReplyStyle{"": ReplyStylePlain, "plain": ReplyStylePlain, "json": ReplyStyleJSON} {
		if got, err := ParseReplyStyle(name); err != nil || got != want {
			t.Fatalf("ParseReplyStyle(%q) = %q, %v", name, got, err)
		}
	}
	if _, err := ParseReplyStyle("plain-v1"); err == nil {
		t.Fatal("an unknown style was accepted")
	}
	for _, format := range []string{"both", "structured"} {
		err := ReplyStyleConflict(ReplyStylePlain, map[string]any{"response_format": format})
		if err == nil {
			t.Fatalf("plain accepted response_format=%s", format)
		}
		if argument := InvalidArguments("req_1", "read", err)["data"].(map[string]any)["argument"]; argument != "response_format" {
			t.Fatalf("refusal names argument %v, want response_format", argument)
		}
		if ReplyStyleConflict(ReplyStyleJSON, map[string]any{"response_format": format}) != nil {
			t.Fatalf("json refused response_format=%s", format)
		}
	}
	if ReplyStyleConflict(ReplyStylePlain, map[string]any{"response_format": "text", "response_mode": "compact"}) != nil {
		t.Fatal("plain refused response_format=text or response_mode=compact")
	}
}
