package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// verify_run refreshes the canonical documents before comparing revisions,
// so an external write since the requested revision is a revision conflict
// rather than a verification of stale bytes.
func TestVerifyRunObservesExternalBytesBeforeRevisionCheck(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{"main.go": "package sample\nvar Value = 1\n"})
	defer direct.closeProviders()
	workspace := direct.get(workspacecore.ID(workspaceID))
	revision := "wsrev_1"
	if got := workspace.Identity().StateSeq; got != 1 {
		revision = fmt.Sprintf("wsrev_%d", got)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package sample\nvar Value = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := direct.call(context.Background(), "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "external-verify",
		"revision_or_transaction": revision, "stages": []any{"parser"},
	})
	if result["outcome"] != "conflict" || result["code"] != "revision_changed" {
		t.Fatalf("verification observed external bytes under an old revision: %#v", result)
	}
}

// Affected-scope verification of the canonical revision runs the parser on
// the edited file only and reports unavailable diagnostics immediately
// instead of waiting for a provider that cannot start.
func TestVerifyRunAffectedScopeCoversOnlyEditedFileAndReturnsQuickly(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "Changed = 1", "Changed = 3", "affected-edit")
	revision := applied["data"].(map[string]any)["revision"].(string)
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
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

// An affected-scope verification of a revision no receipt describes, here
// an external write, takes the affected files from Git rather than refusing,
// and says so.
func TestVerifyRunAffectedScopeFallsBackToGitWithoutReceipts(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	defer direct.closeProviders()
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte("package sample\nvar Changed = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verified := direct.call(context.Background(), "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "git-affected",
		"revision_or_transaction": "current", "stages": []any{"parser"}, "test_scope": "affected",
	})
	if verified["outcome"] != "ok" {
		t.Fatalf("affected verification without receipts = %#v", verified)
	}
	scope := affectedParserScope(t, verified)
	if len(scope) != 1 || scope[0] != "changed.go" {
		t.Fatalf("affected scope = %#v, want the file Git reports as changed", scope)
	}
	if warnings := verified["warnings"].([]string); len(warnings) == 0 || !strings.Contains(warnings[0], "Git") {
		t.Fatalf("Git fallback carries no warning: %#v", verified)
	}
}

// When Git answers that nothing changed, the affected set is empty and says
// so; it is not widened to every file, which is for when Git cannot answer.
// A deletion is a change: the files beside the deleted one are affected.
func TestVerifyRunAffectedScopeFromGitIsEmptyOrHoldsDeletions(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"a/gone.go":  "package a\nvar Gone = 1\n",
		"a/kept.go":  "package a\nvar Kept = 2\n",
		"b/other.go": "package b\nvar Other = 3\n",
	})
	defer direct.closeProviders()
	for _, args := range [][]string{
		{"init", "-q"}, {"add", "."},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"},
	} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	verify := func(key string) map[string]any {
		return direct.call(context.Background(), "verify_run", map[string]any{
			"workspace_id": workspaceID, "idempotency_key": key,
			"revision_or_transaction": "current", "stages": []any{"parser"}, "test_scope": "affected",
		})
	}
	// An external write and its revert leave a revision no receipt describes
	// and a tree Git reports as clean.
	kept := filepath.Join(root, "a/kept.go")
	for _, content := range []string{"package a\nvar Kept = 4\n", "package a\nvar Kept = 2\n"} {
		if err := os.WriteFile(kept, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		direct.call(context.Background(), "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	}
	clean := verify("clean-affected")
	if scope := affectedParserScope(t, clean); len(scope) != 0 {
		t.Fatalf("a clean tree was widened to %#v", scope)
	}
	if warnings := clean["warnings"].([]string); len(warnings) == 0 || !strings.HasPrefix(warnings[0], "no_changes") {
		t.Fatalf("an empty affected set carries no warning: %#v", clean)
	}
	if err := os.Remove(filepath.Join(root, "a/gone.go")); err != nil {
		t.Fatal(err)
	}
	deleted := verify("deleted-affected")
	if deleted["outcome"] != "ok" {
		t.Fatalf("deletion-only verification = %#v", deleted)
	}
	if scope := affectedParserScope(t, deleted); len(scope) != 1 || scope[0] != "a/kept.go" {
		t.Fatalf("deletion-only scope = %#v, want the file beside the deleted one", scope)
	}
	if warnings := deleted["warnings"].([]string); len(warnings) == 0 || !strings.Contains(warnings[0], "1 deleted") {
		t.Fatalf("deletion is not counted in the warning: %#v", deleted)
	}
}

// Without receipts or Git, an affected-scope verification widens to every
// file with a scope_widened warning instead of refusing.
func TestVerifyRunAffectedScopeWidensWithoutReceiptsOrGit(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"changed.go":   "package sample\nvar Changed = 1\n",
		"unchanged.go": "package sample\nvar Unchanged = 2\n",
	})
	defer direct.closeProviders()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(root))
	if err := os.WriteFile(filepath.Join(root, "changed.go"), []byte("package sample\nvar Changed = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verified := direct.call(context.Background(), "verify_run", map[string]any{
		"workspace_id": workspaceID, "idempotency_key": "widened-affected",
		"revision_or_transaction": "current", "stages": []any{"parser"}, "test_scope": "affected",
	})
	if verified["outcome"] != "ok" {
		t.Fatalf("affected verification without receipts or Git = %#v", verified)
	}
	if scope := affectedParserScope(t, verified); len(scope) != 2 {
		t.Fatalf("widened scope = %#v, want every file", scope)
	}
	if warnings := verified["warnings"].([]string); len(warnings) == 0 || !strings.HasPrefix(warnings[0], "scope_widened") {
		t.Fatalf("widened scope carries no warning: %#v", verified)
	}
}

func affectedParserScope(t *testing.T, verified map[string]any) []string {
	t.Helper()
	stages := mcpapi.AnySlice(verified["data"].(map[string]any)["verification"].(map[string]any)["stages"])
	for _, stage := range stages {
		if stage := stage.(map[string]any); stage["stage"] == "parser" {
			scope, _ := stage["scope"].([]string)
			return scope
		}
	}
	t.Fatalf("no parser stage: %#v", verified)
	return nil
}

// The changed paths of the target revision come from the receipt that
// produced it, including plan receipts that list several paths.
func TestCanonicalChangedPathsComeFromTheReceiptOfTheTargetRevision(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	workspaceID := workspacecore.ID("ws_receipt_test")
	direct.receipts.replays[string(workspaceID)+"\x00change_plan\x00receipt"] = &directReplay{
		complete: true,
		result: map[string]any{
			"data": map[string]any{
				"canonical_changed": true,
				"from_revision":     "wsrev_19",
				"revision":          "wsrev_20",
				"changed_paths":     []any{"pkg/a.py", "tests/test_a.py"},
			},
		},
	}
	got, err := direct.receipts.CanonicalChangedPaths(workspaceID, 20)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pkg/a.py", "tests/test_a.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %#v, want %#v", got, want)
	}
}
