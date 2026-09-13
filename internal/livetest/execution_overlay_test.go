//go:build live

package livetest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionOverlayBranchesCallbackAndStaleness(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "overlay_go"))
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	w := workspaceIdentity(t, opened)
	for _, side := range []string{"left", "right"} {
		args := []string{}
		if side == "left" {
			args = append(args, "left")
		}
		started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": side + "-start", "action": "start",
			"file": filepath.Join(root, "main.go"), "adapter": "delve", "args": args, "wait_ms": 10000,
			"trace_policy": map[string]any{"mode": "path", "targets": []any{
				map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "sink"}}}}}})
		id, _ := data(started)["trace_id"].(string)
		if id == "" {
			t.Fatalf("%v", started)
		}
		last := started
		for i := 0; i < 4; i++ {
			if data(last)["debug"].(map[string]any)["state"] == "exited" {
				break
			}
			last = call(t, session, "debug_control", map[string]any{"workspace_id": w, "idempotency_key": fmt.Sprintf("%s-%d", side, i), "action": "continue", "wait_ms": 10000})
		}
		combined := call(t, session, "path_explain", map[string]any{"workspace_id": w, "mode": "combined", "trace_id": id, "from": "main", "to": "sink", "use_provider": false})
		payload := data(combined)
		overlay, ok := payload["overlay"].(map[string]any)
		if !ok {
			t.Fatalf("missing overlay: %v", combined)
		}
		if len(payload["paths"].([]any)) < 2 {
			t.Fatalf("static alternatives lost: %v", combined)
		}
		if len(overlay["runtime_only_edges"].([]any)) == 0 {
			t.Fatalf("callback extension absent: %v trace=%v", combined, call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id}))
		}
		observed := false
		for _, raw := range overlay["edges"].([]any) {
			if raw.(map[string]any)["status"] == "observed" {
				observed = true
			}
		}
		if !observed {
			t.Fatalf("no static call observed: %v", overlay)
		}
		snapshot := payload["snapshot"].(map[string]any)
		graph := snapshot["execution"].(map[string]any)
		names := map[string]string{}
		for _, raw := range graph["nodes"].([]any) {
			node := raw.(map[string]any)
			name, _ := node["name"].(string)
			names[node["id"].(string)] = name
		}
		seen := map[string]bool{}
		for _, raw := range overlay["observations"].([]any) {
			for _, id := range raw.(map[string]any)["nodes"].([]any) {
				seen[names[id.(string)]] = true
			}
		}
		other := "left"
		if side == "left" {
			other = "right"
		}
		if !seen[side] || seen[other] {
			t.Fatalf("branch observations wrong: %v", seen)
		}
		if side == "right" {
			call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operation": map[string]any{"kind": "replace_literal", "path": "main.go", "old": "println(\"hit\")", "new": "println(\"changed\")"}})
			stale := call(t, session, "path_explain", map[string]any{"workspace_id": w, "mode": "combined", "trace_id": id, "from": "main", "to": "sink", "use_provider": false})
			if outcome(stale) != "failed" || !strings.Contains(renderJSON(t, stale), "graph_source_changed") {
				t.Fatalf("stale trace accepted: %v", stale)
			}
		}
	}
}
