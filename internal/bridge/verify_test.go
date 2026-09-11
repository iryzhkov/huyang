package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
