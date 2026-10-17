//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type m9CursorInput struct{ bytes.Buffer }

func (w *m9CursorInput) Close() error { return nil }

// Return at most one response line per Read, even when Scanner has spare
// capacity. Future calls must not emit their trace events through read-ahead.
type m9CursorResponses struct {
	lines        [][]byte
	beforeLine   func(int)
	line, offset int
}

func (r *m9CursorResponses) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if r.line == len(r.lines) {
		return 0, io.EOF
	}
	if r.offset == 0 {
		r.beforeLine(r.line)
	}
	n := copy(dst, r.lines[r.line][r.offset:])
	r.offset += n
	if r.offset == len(r.lines[r.line]) {
		r.line++
		r.offset = 0
	}
	return n, nil
}

// Replay the real probe call paths through an in-memory MCP wire. No Huyang,
// Neovim, manager or other subprocess runs; only scratch capture files are written.
func TestM9AcknowledgedCursorCallSequence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if os.Getenv("HOME") != dir {
		t.Fatal("HOME must name the test scratch directory before capture writes")
	}
	fixed, initial, _, _ := m9HistoricalFixture(t)
	// Keep observer installation and attachment, then build one actual queued
	// empty pull. It is enqueued during the fix and collected by the next read.
	events, selected := m9AppendHistoricalPull(t, fixed, initial[:2], []any{}, false)
	queuedEnd := 0
	for n, event := range events {
		if event["stage"] == "enqueued" {
			queuedEnd = n + 1
		}
	}
	if queuedEnd == 0 {
		t.Fatal("historical cursor fixture omitted its enqueue")
	}
	finalStart := len(events)
	for _, event := range []map[string]any{
		{"kind": "collection_begin", "collection_id": float64(2)},
		{"kind": "collection_end", "collection_id": float64(2),
			"observer_status": map[string]any{"installed": true, "errors": []any{}}},
		{"kind": "transport_end", "provider_rpc_id": float64(43),
			"observer_status": map[string]any{"installed": true, "errors": []any{}}},
	} {
		events = m9HistoryAppend(events, event)
	}
	trace, err := os.Create(filepath.Join(dir, "lsp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = trace.Close() })
	emit := func(batch []map[string]any) {
		t.Helper()
		for _, event := range batch {
			if err := json.NewEncoder(trace).Encode(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	emit(events[:2])
	current := map[string]any{
		"id": fixed.IDs[0], "document": "main.py", "status": "current",
		"finding": map[string]any{"code": "reportAssignmentType", "severity": 1},
	}
	stale := map[string]any{"id": fixed.IDs[0], "document": "main.py", "status": "stale"}
	report := func(cursor string, items, staleItems []any) map[string]any {
		return map[string]any{"outcome": "provisional", "data": map[string]any{"diagnostics": map[string]any{
			"cursor": cursor, "confidence": "provisional", "current": items, "stale": staleItems,
		}}}
	}
	const evidenceID = "ev_origin"
	ack := report("diagcur_3", []any{current}, nil)
	collected := report("diagcur_6", []any{current}, nil)
	post := report("diagcur_8", nil, []any{stale})
	post["request_id"] = "diagnostics_intermediate"
	post["evidence"] = map[string]any{"ids": []any{evidenceID}}
	m9Report(post)["coverage"] = map[string]any{
		"edited_documents": map[string]any{"evidence_ids": []any{evidenceID}},
	}
	final := m9CloneHistoricalReply(t, post)
	final["request_id"] = "diagnostics_final"
	fixReply := map[string]any{"outcome": "provisional", "request_id": fixed.Transaction,
		"data": map[string]any{"document_revision": fixed.Revision},
		"diagnostic_updates": []any{map[string]any{"id": fixed.IDs[0], "kind": "persisting"}}}
	recovered := map[string]any{"data": map[string]any{"evidence": map[string]any{"payload": selected}}}
	retired := report("diagcur_8", nil, []any{stale})
	m9Report(retired)["notices_truncated"] = true
	// The finite script includes an intermediate full report, a final collection
	// and real evidence recovery. The last read checks the floor after since=0.
	type cursorStep struct {
		name   string
		reply  map[string]any
		events []map[string]any
		since  string
	}
	postFixSteps := []cursorStep{
		{name: "edit_apply", reply: fixReply, events: events[2:queuedEnd]},
		{name: "diagnostics", reply: post, events: events[queuedEnd:finalStart], since: "diagcur_3"},
		{name: "diagnostics", reply: final, events: events[finalStart:], since: "diagcur_3"},
		{name: "evidence_get", reply: recovered},
	}
	steps := append([]cursorStep{
		{name: "diagnostics", reply: ack, since: "diagcur_3"},
		{name: "diagnostics", reply: collected, since: "diagcur_3"},
	}, postFixSteps...)
	steps = append(steps,
		cursorStep{name: "diagnostics", reply: retired, since: "diagcur_0"},
		cursorStep{name: "diagnostics", reply: post, since: "diagcur_3"},
	)
	responses := &m9CursorResponses{beforeLine: func(n int) { emit(steps[n].events) }}
	for i, step := range steps {
		line, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": i + 1, "result": map[string]any{"structuredContent": step.reply},
		})
		if err != nil {
			t.Fatal(err)
		}
		responses.lines = append(responses.lines, append(line, '\n'))
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
		wire: &probeMCP{in: input, out: bufio.NewScanner(responses), log: log},
		evidenceSeen: map[string]bool{}, evidenceBatches: map[string]map[string]any{},
		postFixCoverage: map[string]any{},
	}
	p.call("diagnostics", map[string]any{"full": true, "since": "diagcur_3"})
	if got := p.collect(); m9Report(got)["cursor"] != "diagcur_6" {
		t.Fatal("collect did not consume its scripted full report")
	}
	p.postFix = fixed
	p.historicalFirstCall = p.sequence + 1
	// No timed polling allocation in this finite replay. Declare the boundary
	// before the fix, explicitly read the intermediate, then let collectPostFix
	// perform its mandatory final full read and evidence recovery.
	p.historicalEnd = time.Now()
	p.postFixCoverage["observation_plan"] = map[string]any{
		"sampling_ms": 0, "declared_before_fix": true,
		"sampling_end": p.historicalEnd.UTC().Format(time.RFC3339Nano),
		"boundary": "scripted intermediate full report, final full report and evidence recovery",
	}
	m9JSON(t, filepath.Join(dir, "historical-observation-plan.json"), p.postFixCoverage["observation_plan"])
	p.call("edit_apply", map[string]any{})
	p.call("diagnostics", map[string]any{"full": true})
	p.collectPostFix()
	if p.postFixCoverage["outcome"] != "historical_post_fix_observation_pending" ||
		p.postFixCoverage["analysis_completion_proven"] != false ||
		p.postFixCoverage["clean_resolution_proven"] != false ||
		p.postFixCoverage["old_terminal_gate_qualified"] != false {
		t.Fatalf("post-fix historical qualification changed: %#v", p.postFixCoverage)
	}
	if len(p.postFixReplies) != len(postFixSteps) || len(p.postFixReplies) != p.sequence-p.historicalFirstCall+1 ||
		p.postFixReplies[0]["request_id"] != fixed.Transaction ||
		p.postFixReplies[1]["request_id"] != "diagnostics_intermediate" ||
		p.historicalInspected != len(p.postFixReplies) || !p.evidenceSeen[evidenceID] {
		t.Fatalf("post-fix path did not retain and inspect its fixing/intermediate/final replies: %#v",
			p.postFixReplies)
	}
	if original := m9Object(p.postFixCoverage["original_evidence"]); original["observed_at"] != selected["observed_at"] {
		t.Fatalf("historical evidence lost its original observation time: %#v", original)
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
	if len(requests) != len(steps) || responses.line != len(steps) {
		t.Fatalf("unexpected probe calls/responses: %d/%d, want %d", len(requests), responses.line, len(steps))
	}
	for i, request := range requests {
		step := steps[i]
		if request["name"] != step.name {
			t.Fatalf("call %d: got %#v, want %s", i+1, request, step.name)
		}
		args := m9Object(request["arguments"])
		if step.name == "diagnostics" && (args["full"] != true || args["since"] != step.since) {
			t.Errorf("full read %d must use its expected acknowledged/explicit floor %s: %#v", i+1, step.since, args)
		}
		if step.name == "evidence_get" && args["evidence_id"] != evidenceID {
			t.Errorf("historical recovery changed the original evidence ID: %#v", args)
		}
	}
	if p.acknowledgedCursor != 3 {
		t.Fatalf("report heads or explicit retired cursor moved the acknowledged floor: %d", p.acknowledgedCursor)
	}
}
