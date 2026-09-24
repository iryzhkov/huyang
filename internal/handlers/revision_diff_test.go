package handlers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
)

// A range across external writes of hundreds of files answers in a bounded
// reply: diffs stop at maxRevisionDiffEntries and say how many there were,
// an inventory step is described by its counts rather than by listing again
// the paths diffs already name, and paths a step did not keep are counted
// in a warning.
func TestRevisionDiffBoundsObservedInventorySteps(t *testing.T) {
	handlers, workspaceID, root := literalFixture(t, map[string]string{"main.go": "package main\n"})
	diff := func(from string) map[string]any {
		return handlers.Execute(context.Background(), "req_diff", "revision_diff", map[string]any{
			"workspace_id": workspaceID, "from_revision": from, "to_revision_or_current": "current",
		})
	}
	from := diff("current")["data"].(map[string]any)["current_revision"].(string)
	for batch := range 3 {
		for index := range 120 {
			name := filepath.Join(root, fmt.Sprintf("gen%d/file-%03d.txt", batch, index))
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(name, []byte("generated\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		diff("current")
	}
	result := diff(from)
	data := result["data"].(map[string]any)
	if diffs := mcpapi.AnySlice(data["diffs"]); len(diffs) != maxRevisionDiffEntries || data["diffs_truncated"] != true {
		t.Fatalf("diffs were not bounded: %d entries, truncated=%v", len(diffs), data["diffs_truncated"])
	}
	if total, _ := data["diffs_total"].(int); total <= maxRevisionDiffEntries {
		t.Fatalf("bounded diffs do not say how many there were: %#v", data["diffs_total"])
	}
	for _, raw := range mcpapi.AnySlice(data["inferred_segments"]) {
		for _, step := range mcpapi.AnySlice(raw.(map[string]any)["steps"]) {
			fields := step.(map[string]any)
			if fields["reason"] != "inventory" {
				continue
			}
			if _, listed := fields["added"].([]string); listed || fields["added"] != 120 || fields["paths_not_listed"] != 20 {
				t.Fatalf("inventory step = %#v", fields)
			}
		}
	}
	warnings := strings.Join(result["warnings"].([]string), " ")
	if !strings.Contains(warnings, "60 paths") || !strings.Contains(warnings, "git status") {
		t.Fatalf("unlisted paths are not counted in a warning: %q", warnings)
	}
}
