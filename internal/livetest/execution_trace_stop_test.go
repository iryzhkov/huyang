//go:build live

package livetest

import (
	"path/filepath"
	"testing"
)

func TestExecutionTraceStopAndEventCap(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "cap-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "path", "max_events": 1, "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": 2}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	stopped := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "cap-stop", "action": "stop"})
	if data(stopped)["trace_error"] != nil {
		t.Fatalf("%v", stopped)
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	trace := data(inspected)["trace"].(map[string]any)
	if trace["finished"] == nil || trace["digest"] == "" || len(trace["events"].([]any)) != 1 || trace["dropped_events"].(float64) < 1 {
		t.Fatalf("stop did not finalize bounded trace: %v", trace)
	}
	listed := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": w, "idempotency_key": "cap-list", "action": "list"})
	if data(listed)["debug"].(map[string]any)["count"] != float64(0) {
		t.Fatal("temporary breakpoints survived stop")
	}
}
