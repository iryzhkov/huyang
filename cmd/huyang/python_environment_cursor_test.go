//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type m9CursorInput struct{ bytes.Buffer }

func (w *m9CursorInput) Close() error { return nil }

// Replay the real probe call paths through an in-memory MCP wire. No Huyang,
// Neovim, manager or other subprocess runs; only scratch capture files are written.
func TestM9AcknowledgedCursorCallSequence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if os.Getenv("HOME") != dir {
		t.Fatal("HOME must name the test scratch directory before capture writes")
	}
	fixed, events := m9PolicyFixture(t)
	var trace bytes.Buffer
	for _, event := range events {
		if err := json.NewEncoder(&trace).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	m9Write(t, filepath.Join(dir, "lsp.jsonl"), trace.String())
	current := map[string]any{
		"id": fixed.IDs[0], "document": "main.py", "status": "current",
		"finding": map[string]any{"code": "reportAssignmentType", "severity": 1},
	}
	stale := map[string]any{"id": fixed.IDs[0], "document": "main.py", "status": "stale"}
	report := func(cursor string, items, staleItems []any) map[string]any {
		return map[string]any{"outcome": "provisional", "data": map[string]any{"diagnostics": map[string]any{
			"cursor": cursor, "confidence": "provisional", "current": items, "stale": staleItems,
			"coverage": map[string]any{"edited_documents": map[string]any{"evidence_ids": []any{"ev_empty"}}},
		}}}
	}
	ack := report("diagcur_3", []any{current}, nil)
	collected := report("diagcur_6", []any{current}, nil)
	post := report("diagcur_8", nil, []any{stale})
	fixReply := map[string]any{"outcome": "provisional", "data": map[string]any{},
		"diagnostic_updates": []any{map[string]any{"id": fixed.IDs[0], "kind": "persisting"}}}
	retired := report("diagcur_8", nil, []any{stale})
	m9Report(retired)["notices_truncated"] = true
	// Keep one extra ordinary read after an explicit retired request to prove
	// that neither a report head nor a lower explicit cursor moves the floor.
	replies := []map[string]any{ack, collected, fixReply, post, post, retired, post}
	var responses bytes.Buffer
	for i, reply := range replies {
		if err := json.NewEncoder(&responses).Encode(map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "result": map[string]any{"structuredContent": reply},
		}); err != nil {
			t.Fatal(err)
		}
	}
	log, err := os.Create(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	input := &m9CursorInput{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &m9Probe{
		t: t, ctx: ctx, dir: dir, root: "/project", events: -1,
		wire: &probeMCP{in: input, out: bufio.NewScanner(&responses), log: log},
		evidenceBatches: map[string]map[string]any{"ev_empty": {
			"kind": "lsp_pull", "selected": true, "complete": false,
			"provider_id": "pyright#7", "document": fixed.Path, "document_revision": fixed.Revision,
			"transaction_id": fixed.Transaction, "expected_version": float64(4), "dimension": "edited_documents",
			"result_id": "opaque-5", "observed_at": "1970-01-01T00:00:00.000031Z",
		}},
		postFixCoverage: map[string]any{},
	}
	p.call("diagnostics", map[string]any{"full": true, "since": "diagcur_3"})
	if got := p.collect(); m9Report(got)["cursor"] != "diagcur_6" {
		t.Fatal("collect did not consume its scripted full report")
	}
	p.postFix = fixed
	p.call("edit_apply", map[string]any{})
	p.collectPostFix()
	if p.postFixCoverage["outcome"] != "clean_unproven" || len(p.postFixReplies) != 3 {
		t.Fatalf("post-fix path did not retain its fixing and intermediate replies: %#v / %d",
			p.postFixCoverage, len(p.postFixReplies))
	}
	for _, reply := range p.postFixReplies {
		if resolved, err := m9PostFixLedger(reply, fixed, true); err != nil || len(resolved) != 0 {
			t.Fatalf("pure-pull proof/veto checks changed: %v / %v", resolved, err)
		}
	}
	bad := p.call("diagnostics", map[string]any{"full": true, "since": "diagcur_0"})
	if _, err := m9PostFixLedger(bad, fixed, true); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("explicit retired request bypassed the truncation check: %v", err)
	}
	p.call("diagnostics", map[string]any{"full": true})

	var requests []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(input.String()), "\n") {
		var request map[string]any
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, m9Object(request["params"]))
	}
	if len(requests) != len(replies) {
		t.Fatalf("unexpected probe calls: %d", len(requests))
	}
	for _, i := range []int{1, 3, 4, 6} {
		args := m9Object(requests[i]["arguments"])
		if requests[i]["name"] != "diagnostics" || args["full"] != true || args["since"] != "diagcur_3" {
			t.Errorf("collect/post-fix request %d must retain acknowledged floor 3: %#v", i+1, requests[i])
		}
	}
	if args := m9Object(requests[5]["arguments"]); args["since"] != "diagcur_0" {
		t.Fatalf("explicit retired request was rewritten: %#v", args)
	}
}
