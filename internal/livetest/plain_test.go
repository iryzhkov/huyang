//go:build live

package livetest

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callText runs one tool through an adapter started with the default plain
// style and returns its single text block, failing on a structured copy.
func callText(t *testing.T, session sessionHandle, name string, arguments map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("%s plain reply: structured=%v blocks=%d", name, result.StructuredContent != nil, len(result.Content))
	}
	return result.Content[0].(*mcp.TextContent).Text, result.IsError
}

// The default adapter answers in plain text across the real socket and
// subprocess, and a guard read from that text alone protects the overwrite
// that follows it.
func TestPlainRepliesCrossTheAdapterWithUsableGuards(t *testing.T) {
	instance := start(t)
	session := instance.connectPlain("full")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("alpha beta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	read, _ := callText(t, session, "read", map[string]any{"root": root, "target": map[string]any{"path": "note.txt"}})
	match := regexp.MustCompile(`^note\.txt \(revision_id=(docrev_[0-9a-f]{64}); lines=1-1/1\)\nalpha beta\n$`).FindStringSubmatch(read)
	if match == nil {
		t.Fatalf("plain read = %q", read)
	}
	searched, _ := callText(t, session, "search", map[string]any{"root": root, "query": "beta"})
	if !strings.HasPrefix(searched, "note.txt:1:alpha beta\n") {
		t.Fatalf("plain search = %q", searched)
	}
	replaced, isError := callText(t, session, "edit_apply", map[string]any{"root": root, "idempotency_key": "live-plain-1",
		"operation": map[string]any{"kind": "create_file", "path": "note.txt", "content": "gamma\n", "replace": true, "revision_id": match[1]}})
	if isError || !strings.HasPrefix(replaced, "Replaced note.txt (revision=wsrev_") {
		t.Fatalf("guarded replace = %q", replaced)
	}
	replayed, _ := callText(t, session, "edit_apply", map[string]any{"root": root, "idempotency_key": "live-plain-1",
		"operation": map[string]any{"kind": "create_file", "path": "note.txt", "content": "gamma\n", "replace": true, "revision_id": match[1]}})
	if !strings.HasPrefix(replayed, "Replayed original receipt; no new mutation") {
		t.Fatalf("replay = %q", replayed)
	}
}
