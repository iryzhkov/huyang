//go:build live

package livetest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutionTraceRestartAndConcurrentStaticQuery(t *testing.T) {
	instance := startWithLanguageServers(t)
	session := instance.connect("experimental")
	root := fixture(t, filepath.Join("execution", "trace_go"))
	w := workspaceIdentity(t, call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root}))
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "hardening-start", "action": "start", "file": filepath.Join(root, "main.go"),
		"adapter": "delve", "wait_ms": 10000, "trace_policy": map[string]any{"mode": "path", "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "leaf"}}, "line_offset": 2}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "path_explain", Arguments: map[string]any{"workspace_id": w, "from": "main", "to": "leaf", "use_provider": false}})
		done <- err
	}()
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	if data(inspected)["trace"] == nil {
		t.Fatalf("%v", inspected)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	instance.redeploy()
	recovered := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	trace, ok := data(recovered)["trace"].(map[string]any)
	if !ok || trace["finished"] == nil || trace["completion"] != "daemon_restart" || !strings.Contains(renderJSON(t, trace), "capture_interrupted") {
		t.Fatalf("%v", recovered)
	}
}
