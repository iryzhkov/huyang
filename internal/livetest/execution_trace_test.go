//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestExecutionTraceRealDelve(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "trace_go"))
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspace := workspaceIdentity(t, opened)
	target := map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": 2}
	started := call(t, session, "debug_session", map[string]any{"workspace_id": workspace, "idempotency_key": "trace-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "path", "targets": []any{target}, "max_events": 16}})
	t.Logf("start: %v", started)
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("no trace started: %v", started)
	}
	last := started
	for i := 0; i < 4; i++ {
		debug, _ := data(last)["debug"].(map[string]any)
		if debug["state"] == "exited" {
			break
		}
		last = call(t, session, "debug_control", map[string]any{"workspace_id": workspace, "idempotency_key": fmt.Sprintf("trace-continue-%d", i), "action": "continue", "wait_ms": 10000})
		t.Logf("continue %d: %v", i, last)
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": workspace, "action": "trace", "trace_id": id})
	trace, ok := data(inspected)["trace"].(map[string]any)
	if !ok {
		t.Fatalf("no trace: %v", inspected)
	}
	if trace["finished"] == nil {
		t.Fatalf("recording did not finish: %v", trace)
	}
	threads := map[any]bool{}
	mapped := 0
	for _, raw := range trace["events"].([]any) {
		event := raw.(map[string]any)
		if event["kind"] == "stopped" {
			threads[event["thread"]] = true
		}
		if values, _ := event["values"].([]any); len(values) > 0 {
			t.Fatal("values retained by default")
		}
		for _, rawFrame := range event["frames"].([]any) {
			frame := rawFrame.(map[string]any)
			if frame["mapping"] == "launch_snapshot" {
				mapped++
			}
		}
	}
	if len(threads) < 2 || mapped == 0 {
		t.Fatalf("threads=%v mapped=%d trace=%v", threads, mapped, trace)
	}
	combined := call(t, session, "path_explain", map[string]any{"workspace_id": workspace, "mode": "combined", "trace_id": id, "from": "main", "to": "leaf", "use_provider": false})
	explanation, ok := data(combined)["explanation"].(map[string]any)
	if !ok || explanation["cause_status"] != "unknown" || len(data(combined)["thread_transitions"].([]any)) == 0 {
		t.Fatalf("concurrent explanation lost uncertainty: %v", combined)
	}
	breakpoints := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": workspace, "idempotency_key": "trace-bps", "action": "list"})
	if data(breakpoints)["debug"].(map[string]any)["count"] != float64(0) {
		t.Fatalf("temporary breakpoints remain: %v", breakpoints)
	}
}
