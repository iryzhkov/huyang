package bridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// revision_diff refreshes the canonical documents itself, so an external
// write made since the last call shows up as a precise gap without a prior
// workspace_inspect.
func TestRevisionDiffObservesExternalChangeWithoutPriorInspect(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{"main.go": "package sample\n"})
	defer direct.closeProviders()
	workspace := direct.get(workspacecore.ID(workspaceID))
	from := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := direct.call(context.Background(), "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": from, "to_revision_or_current": "current",
	})
	if result["outcome"] != "partial" || result["code"] != "diff_evidence_incomplete" {
		t.Fatalf("revision diff did not report the external gap precisely: %#v", result)
	}
	gaps := mcpapi.AnySlice(result["data"].(map[string]any)["gaps"])
	if len(gaps) != 1 {
		t.Fatalf("external change was not exposed as an exact gap: %#v", result)
	}
}

// Two native edits that restore the original bytes net to no changed path.
func TestRevisionDiffCollapsesExactEditRevert(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"note.txt": "alpha beta\n",
	})
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "revert-forward")
	applyLiteralProbeEdit(t, direct, workspaceID, "gamma", "beta", "revert-back")
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
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

// A range that starts before an external gap still returns the native edit
// segments it knows, alongside the explicit gap.
func TestRevisionDiffReturnsKnownSegmentsAcrossExternalGap(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{
		"first.txt": "before\n", "second.txt": "alpha beta\n",
	})
	workspace := direct.get(workspacecore.ID(workspaceID))
	if _, err := workspace.Refresh(filepath.Join(root, "first.txt"), workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "first.txt"), []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Refresh(filepath.Join(root, "first.txt"), workspacecore.ProviderLayer{}); err != nil {
		t.Fatal(err)
	}
	session, cleanup := connectOfficialClient(t, mcpapi.ProfileEdit, direct)
	defer cleanup()
	inspected := callModern(t, session, "workspace_inspect", map[string]any{"workspace_id": workspaceID})
	if inspected["data"].(map[string]any)["revision"] != "wsrev_2" {
		t.Fatalf("external change was not observed: %#v", inspected)
	}
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "post-gap-edit")
	diffed := callModern(t, session, "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "current",
	})
	if diffed["outcome"] != "partial" || diffed["code"] != "diff_evidence_incomplete" {
		t.Fatalf("gap diff outcome = %#v", diffed)
	}
	data := diffed["data"].(map[string]any)
	if len(data["gaps"].([]any)) != 1 || len(data["known_segments"].([]any)) != 1 || len(data["diffs"].([]any)) != 1 {
		t.Fatalf("known diff segments or explicit gaps missing: %#v", diffed)
	}
}

// A committed plan receipt contributes every committed diff to the revision
// history, not only the first path.
func TestRevisionDiffIncludesEveryCommittedPlanDiff(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"sample.py": "value = 1\n"})
	applied := applyLiteralProbeEdit(t, direct, workspaceID, "1", "2", "plan-receipt-source")
	if applied["outcome"] != "ok" && applied["outcome"] != "provisional" {
		t.Fatalf("seed edit failed: %#v", applied)
	}

	direct.receipts.mu.Lock()
	for key := range direct.receipts.replays {
		if strings.HasPrefix(key, workspaceID+"\x00edit_apply\x00") {
			delete(direct.receipts.replays, key)
		}
	}
	direct.receipts.replays[workspaceID+"\x00change_plan\x00python-plan-apply"] = &directReplay{
		complete: true,
		result: map[string]any{"data": map[string]any{
			"canonical_changed": true,
			"from_revision":     "wsrev_1",
			"revision":          "wsrev_2",
			"plan": workspacecore.PlanRecord{Preparation: &workspacecore.PlanPreparation{
				CommittedDiffs: []workspacecore.ExactDiff{
					{Path: "sample.py", BeforeSHA256: "before-sample", AfterSHA256: "after-sample"},
					{Path: "other.py", BeforeSHA256: "before-other", AfterSHA256: "after-other"},
				},
			}},
		}},
	}
	direct.receipts.mu.Unlock()

	result := direct.call(context.Background(), "revision_diff", map[string]any{
		"workspace_id": workspaceID, "from_revision": "wsrev_1", "to_revision_or_current": "wsrev_2",
	})
	if result["outcome"] != "ok" {
		t.Fatalf("revision diff outcome = %#v", result)
	}
	diffs := result["data"].(map[string]any)["diffs"].([]any)
	if len(diffs) != 2 {
		t.Fatalf("revision diff returned %d plan diffs: %#v", len(diffs), result)
	}
}

// An inspect receipt that repeats a committed plan's diffs does not double
// the revision history.
func TestRecordedRevisionDiffsDeduplicateRepeatedPlanReceipts(t *testing.T) {
	direct := newDirectWorkspaces(t.TempDir())
	diff := workspacecore.ExactDiff{
		Path: "report.go", BeforeSHA256: "before", AfterSHA256: "after",
		Patch: "@@ -1 +1 @@\n-before\n+after\n",
	}
	plan := workspacecore.PlanRecord{Preparation: &workspacecore.PlanPreparation{
		CanonicalFromRevision: "wsrev_2", CanonicalRevision: "wsrev_3",
		CanonicalChanged: true, CommittedDiffs: []workspacecore.ExactDiff{diff},
	}}
	result := func() map[string]any {
		return map[string]any{"data": map[string]any{
			"canonical_changed": true, "from_revision": "wsrev_2", "revision": "wsrev_3", "plan": plan,
		}}
	}
	prefix := "workspace\x00change_plan\x00"
	direct.receipts.replays[prefix+"apply"] = &directReplay{complete: true, result: result()}
	direct.receipts.replays[prefix+"inspect"] = &directReplay{complete: true, result: result()}

	recorded := direct.receipts.RecordedRevisionDiffs("workspace", 2, 3)
	if len(recorded) != 1 {
		t.Fatalf("duplicate committed plan receipts produced %d revision diffs: %#v", len(recorded), recorded)
	}
}
