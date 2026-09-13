//go:build live

package livetest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExecutionTraceRequestCancellation(t *testing.T) {
	session, root, w := traceLiveSetup(t)
	edited := call(t, session, "edit_apply", map[string]any{"workspace_id": w, "operations": []any{
		map[string]any{"kind": "replace_literal", "path": "main.go", "old": "import \"os\"", "new": "import (\"os\"; \"time\")"},
		map[string]any{"kind": "replace_literal", "path": "main.go", "old": "println(a + b)", "new": "time.Sleep(time.Minute); println(a + b)"}}})
	if outcome(edited) != "ok" {
		t.Fatalf("%v", edited)
	}
	started := call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "cancel-start", "action": "start",
		"file": filepath.Join(root, "main.go"), "adapter": "delve", "wait_ms": 10000,
		"trace_policy": map[string]any{"mode": "path", "targets": []any{
			map[string]any{"target": map[string]any{"symbol_locator": map[string]any{"path": "main.go", "name_path": "main"}}, "line_offset": 1}}}})
	id, _ := data(started)["trace_id"].(string)
	if id == "" {
		t.Fatalf("%v", started)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "debug_control", Arguments: map[string]any{
		"workspace_id": w, "idempotency_key": "cancel-control", "action": "continue", "wait_ms": 10000}})
	if err == nil {
		t.Fatal("control did not wait until cancellation")
	}
	call(t, session, "debug_session", map[string]any{"workspace_id": w, "idempotency_key": "cancel-stop", "action": "stop"})
	inspected := call(t, session, "debug_inspect", map[string]any{"workspace_id": w, "action": "trace", "trace_id": id})
	trace := data(inspected)["trace"].(map[string]any)
	if trace["finished"] == nil || !strings.Contains(renderJSON(t, trace), "capture_interrupted") {
		t.Fatalf("cancelled capture not finalized: %v", trace)
	}
}
