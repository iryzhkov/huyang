//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestExecutionNativeWatchTwoWritesAcrossGoroutines(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "watch_go"))
	w := workspaceIdentity(t, call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root}))
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "watch-start", "action": "start", "file": filepath.Join(root, "main.go"),
		"adapter": "delve", "wait_ms": 10000, "trace_policy": map[string]any{"mode": "conditions", "capture_values": true, "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "main"}}, "line_offset": 3}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	watched := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": w, "idempotency_key": "watch-set", "action": "watch", "value_name": "value"})
	if data(watched)["debug"].(map[string]any)["watch"].(map[string]any)["available"] != true {
		t.Fatalf("%v", watched)
	}
	last := started
	for i := 0; i < 5; i++ {
		if data(last)["debug"].(map[string]any)["state"] == "exited" {
			break
		}
		last = call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("watch-%d", i), "action": "continue", "wait_ms": 10000})
		debug := data(last)["debug"].(map[string]any)
		t.Logf("stop reason=%v hit=%v", debug["reason"], debug["hit"])
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "value_origin", "trace_id": id, "value_name": "value"})
	origin := data(inspected)["value_origin"].(map[string]any)
	hits := origin["mutations"].([]any)
	if len(hits) != 2 {
		t.Fatalf("want two exact writes: %v", origin)
	}
	first, second := hits[0].(map[string]any), hits[1].(map[string]any)
	a, b := first["mutation"].(map[string]any), second["mutation"].(map[string]any)
	if first["thread"] == second["thread"] || a["object_id"] != b["object_id"] || a["evidence"] != "native_data_breakpoint_hit" || second["sequence"].(float64) <= first["sequence"].(float64) {
		t.Fatalf("write identity/order lost: %v", origin)
	}
	if origin["status"] != "unknown" {
		t.Fatal("watch address became whole-program origin")
	}
}
