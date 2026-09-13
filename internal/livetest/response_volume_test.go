//go:build live

package livetest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExperimentalByteReadsAndCompactReceiptsCrossSocket(t *testing.T) {
	instance := start(t)
	session := instance.connect("experimental")
	root := t.TempDir()
	source := strings.Repeat("x", 131072) + "marker-needed"
	if err := os.WriteFile(filepath.Join(root, "min.txt"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	opened := call(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	id := workspaceIdentity(t, opened)
	args := map[string]any{"workspace_id": id, "target": map[string]any{"path": "min.txt"}, "max_lines": 1, "max_bytes": 1024, "response_mode": "compact"}
	reply, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "read", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if reply.StructuredContent != nil {
		t.Fatal("duplicated bounded source")
	}
	var first map[string]any
	if err := json.Unmarshal([]byte(reply.Content[0].(*mcp.TextContent).Text), &first); err != nil {
		t.Fatal(err)
	}
	if len(data(first)["content"].(string)) != 1024 || data(first)["byte_truncated"] != true {
		t.Fatalf("%#v", first)
	}
	// Seek directly near the end, guarded by the first read's source revision.
	args["byte_offset"], args["expected_revision_id"] = 131072, data(first)["revision_id"]
	last := call(t, session, "read", args)
	if data(last)["content"] != "marker-needed" {
		t.Fatalf("%#v", last)
	}
	status := call(t, session, "workspace_inspect", map[string]any{"workspace_id": id, "view": "revision", "response_mode": "compact"})
	if len(data(status)) != 1 || data(status)["revision"] == nil {
		t.Fatalf("%#v", status)
	}
	edit := call(t, session, "edit_apply", map[string]any{"workspace_id": id, "response_mode": "compact", "operation": map[string]any{"kind": "replace_literal", "path": "min.txt", "old": "marker-needed", "new": "marker-repaired"}})
	if data(edit)["canonical_changed"] != true || data(edit)["details_omitted"] != true {
		t.Fatalf("%#v", edit)
	}
	if refused := call(t, session, "read", args); refused["code"] != "read_revision_changed" {
		t.Fatalf("%#v", refused)
	}
	wire, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("source=%d bytes; bounded text-only MCP result=%d bytes; revision envelope=%d bytes", len(source), len(wire), len(renderJSON(t, status)))
}
