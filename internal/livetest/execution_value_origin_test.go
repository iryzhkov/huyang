//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionValueOriginSamplesAndGoroutineAmbiguity(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	edited := call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "_ = password", "new": "_ = password; n++"}})
	if outcome(edited) != "ok" {
		t.Fatalf("%v", edited)
	}
	targets := []any{}
	for _, offset := range []int{3, 6} {
		targets = append(targets, map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": offset})
	}
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "origin-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "conditions", "capture_values": true, "targets": targets}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	watched := call(t, session, "debug_breakpoints", map[string]any{"workspace_id": w, "idempotency_key": "origin-watch", "action": "watch", "value_name": "n"})
	t.Logf("native watch: %v", watched)
	if data(watched)["debug"].(map[string]any)["watch"].(map[string]any)["available"] != true {
		t.Fatalf("%v", watched)
	}
	last := started
	for i := 0; i < 8; i++ {
		if data(last)["debug"].(map[string]any)["state"] == "exited" {
			break
		}
		last = call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("origin-%d", i), "action": "continue", "wait_ms": 10000})
	}
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "value_origin", "trace_id": id, "value_name": "n"})
	origin, ok := data(inspected)["value_origin"].(map[string]any)
	if !ok || origin["status"] != "unknown" || origin["identity"] != "unresolved" {
		t.Fatalf("%v", inspected)
	}
	// Coincident line breakpoints may hide watchpoint attribution; sampled
	// differences remain useful without inventing a native hit.
	difference, ambiguous := false, false
	threads := map[any]bool{}
	for _, raw := range origin["samples"].([]any) {
		sample := raw.(map[string]any)
		threads[sample["thread"]] = true
		if sample["difference"] == "different_sampled_value" {
			difference = true
		}
		if sample["difference"] == "unknown" {
			ambiguous = true
		}
	}
	if !difference || !ambiguous || len(threads) < 2 {
		t.Fatalf("%v", origin)
	}
	secret := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "value_origin", "trace_id": id, "value_name": "password"})
	if strings.Contains(renderJSON(t, secret), "trace-secret-value") {
		t.Fatal("origin disclosed redacted value")
	}
}
