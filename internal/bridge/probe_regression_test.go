package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func openProbeProject(t *testing.T, files map[string]string) (*directWorkspaces, string, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	direct := newDirectWorkspaces(t.TempDir())
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	t.Cleanup(cleanup)
	opened := callModern(t, session, "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := opened["workspace"].(map[string]any)["id"].(string)
	return direct, workspaceID, root
}

func applyLiteralProbeEdit(t *testing.T, direct *directWorkspaces, workspaceID, query, replacement, key string) map[string]any {
	t.Helper()
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	searched := callModern(t, session, "search", map[string]any{
		"workspace_id": workspaceID, "query": query, "mode": "literal",
	})
	hits := searched["data"].(map[string]any)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("search %q returned %d hits: %#v", query, len(hits), searched)
	}
	return callModern(t, session, "edit_apply", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": key,
		"operation": map[string]any{
			"kind":    "replace_range",
			"target":  map[string]any{"file_range": hits[0].(map[string]any)["range"]},
			"content": replacement,
		},
	})
}

func TestWorkspaceInspectViewsExposeAndAdvanceCanonicalRevision(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"a.go": "package sample\nvar Before = 1\n",
	})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()

	for _, view := range []string{"status", "overview", "map"} {
		inspected := callModern(t, session, "workspace_inspect", map[string]any{
			"workspace_id": workspaceID, "view": view,
		})
		data := inspected["data"].(map[string]any)
		if data["view"] != view || data["revision"] != "wsrev_1" {
			t.Fatalf("%s inspection omitted its view/revision: %#v", view, inspected)
		}
		_, hasOverview := data["overview"]
		if view == "status" && hasOverview {
			t.Fatalf("status inspection unexpectedly returned repository overview: %#v", inspected)
		}
		if view != "status" && !hasOverview {
			t.Fatalf("%s inspection omitted repository orientation: %#v", view, inspected)
		}
	}

	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Before", "After", "inspect-revision-edit")
	if applied["outcome"] != "provisional" {
		t.Fatalf("edit failed: %#v", applied)
	}
	if !strings.Contains(applied["summary"].(string), "diagnostics are unavailable") {
		t.Fatalf("edit misreported unavailable diagnostics: %#v", applied)
	}
	verification := applied["data"].(map[string]any)["verification"].(map[string]any)
	if verification["confidence"] != "unavailable" {
		t.Fatalf("edit invented diagnostic timeout evidence: %#v", applied)
	}
	inspected := callModern(t, session, "workspace_inspect", map[string]any{
		"workspace_id": workspaceID, "view": "status",
	})
	if revision := inspected["data"].(map[string]any)["revision"]; revision != "wsrev_2" {
		t.Fatalf("inspection revision did not advance after edit: %#v", inspected)
	}
}

func TestReadSymbolLocatorDoesNotSilentlyReturnWholeFileWithoutParser(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"main.rb": "class Widget\n  def call\n    :ok\n  end\nend\n",
	})
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	result := callModern(t, session, "read", map[string]any{
		"workspace_id": workspaceID,
		"target": map[string]any{"symbol_locator": map[string]any{
			"path": filepath.Join(root, "main.rb"), "name_path": "Widget#call",
		}},
		"view": "source",
	})
	if result["outcome"] != "unavailable" || result["code"] != "semantic_provider_unavailable" {
		t.Fatalf("text-only symbol read made an unsupported semantic claim: %#v", result)
	}
	data := result["data"].(map[string]any)
	if _, leaked := data["content"]; leaked {
		t.Fatalf("unresolved symbol locator silently returned file content: %#v", result)
	}
	next := result["next"].([]any)
	if len(next) == 0 || next[0].(map[string]any)["tool"] != "search" || next[0].(map[string]any)["query"] != "Widget#call" {
		t.Fatalf("symbol fallback is not actionable: %#v", result)
	}
}

func TestCanonicalAffectedVerificationUsesOnlyEditedFileAndReturnsQuickly(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Changed = 1", "Changed = 3", "affected-edit")
	revision := applied["data"].(map[string]any)["revision"].(string)
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	started := time.Now()
	verified := callModern(t, session, "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "affected-verify",
		"revision_or_transaction": revision,
		"stages":                  []string{"parser", "diagnostics"}, "test_scope": "affected",
	})
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("verification waited for an unavailable provider: %s (%#v)", elapsed, verified)
	}
	if verified["outcome"] != "partial" {
		t.Fatalf("parser plus unavailable diagnostics should be partial: %#v", verified)
	}
	stages := verified["data"].(map[string]any)["verification"].(map[string]any)["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("verification stages = %#v", verified)
	}
	parser := stages[0].(map[string]any)
	scope := parser["scope"].([]any)
	if len(scope) != 1 || scope[0] != "changed.go" {
		t.Fatalf("affected parser scope includes unrelated files: %#v", verified)
	}
	if diagnostics := stages[1].(map[string]any); diagnostics["status"] != "unavailable" {
		t.Fatalf("unavailable diagnostics were not reported immediately: %#v", verified)
	}
}

func TestRevisionDiffCollapsesExactEditRevert(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"note.txt": "alpha beta\n",
	})
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "revert-forward")
	applyLiteralProbeEdit(t, direct, workspaceID, "gamma", "beta", "revert-back")
	session, cleanup := connectOfficialClient(t, profileEdit, direct)
	defer cleanup()
	diffed := callModern(t, session, "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "current",
	})
	if diffed["outcome"] != "ok" {
		t.Fatalf("revision diff failed: %#v", diffed)
	}
	diffs := diffed["data"].(map[string]any)["diffs"].([]any)
	if len(diffs) != 0 {
		t.Fatalf("exactly reverted path remains in endpoint diff: %#v", diffed)
	}
}

func TestCompactTextDataSummarizesOrientationWithoutDroppingResults(t *testing.T) {
	orientation := workspacecore.Orientation{Entries: []workspacecore.Entry{{Path: "a.go"}, {Path: "b.go"}}}
	data := compactTextData(map[string]any{"overview": orientation, "content": "kept"}).(map[string]any)
	if data["content"] != "kept" {
		t.Fatalf("substantive text data was dropped: %#v", data)
	}
	summary := data["overview"].(map[string]any)
	if summary["entry_count"] != 2 {
		t.Fatalf("orientation summary = %#v", summary)
	}
	if _, present := summary["entries"]; present {
		t.Fatalf("compact text duplicated orientation entries: %#v", summary)
	}
}
