package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// revision_diff refreshes the canonical documents itself, so an external
// write made since the last call is observed without a prior
// workspace_inspect, and the step it caused is answered with an inferred
// entry for that path instead of an unexplained gap.
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
	if result["outcome"] != "ok" {
		t.Fatalf("revision diff did not account for the observed external write: %#v", result)
	}
	data := result["data"].(map[string]any)
	diffs := mcpapi.AnySlice(data["diffs"])
	if len(diffs) != 1 || len(mcpapi.AnySlice(data["inferred_segments"])) != 1 {
		t.Fatalf("external change was not reported as one inferred step: %#v", result)
	}
	entry := diffs[0].(map[string]any)
	if entry["path"] != "main.go" || entry["inferred"] != true || entry["change"] != "modified" ||
		entry["after_sha256"] != contentSHA256("package changed\n") {
		t.Fatalf("inferred entry = %#v", entry)
	}
	if warnings := result["warnings"].([]string); len(warnings) == 0 || !strings.Contains(warnings[0], "inferred") {
		t.Fatalf("inferred answer carries no warning: %#v", result)
	}
}

func contentSHA256(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Either end may be current, a reversed range is swapped, and wsrev_0 is
// read as wsrev_1, each with a warning; a token that is no revision at all is
// refused with the current revision and a call that works.
func TestRevisionDiffNormalisesRangeEndpoints(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{"note.txt": "alpha beta\n"})
	applyLiteralProbeEdit(t, direct, workspaceID, "beta", "gamma", "range-edit")
	call := func(from, to string) map[string]any {
		return direct.call(context.Background(), "revision_diff", map[string]any{
			"workspace_id": workspaceID, "from_revision": from, "to_revision_or_current": to,
		})
	}
	for _, request := range []struct{ from, to, warning string }{
		{"current", "wsrev_1", "swapped"},
		{"wsrev_0", "current", "wsrev_1"},
		{"current", "current", ""},
	} {
		result := call(request.from, request.to)
		if result["outcome"] != "ok" {
			t.Fatalf("revision_diff %s..%s = %#v", request.from, request.to, result)
		}
		warnings := strings.Join(result["warnings"].([]string), " ")
		if request.warning != "" && !strings.Contains(warnings, request.warning) {
			t.Fatalf("revision_diff %s..%s warnings = %q", request.from, request.to, warnings)
		}
	}
	swapped := call("current", "wsrev_1")["data"].(map[string]any)
	if swapped["from_revision"] != "wsrev_1" || len(mcpapi.AnySlice(swapped["diffs"])) != 1 {
		t.Fatalf("swapped range = %#v", swapped)
	}
	refused := call("HEAD", "current")
	if refused["code"] != "invalid_revision_range" || refused["data"].(map[string]any)["current_revision"] != "wsrev_2" ||
		len(mcpapi.AnySlice(refused["next"])) == 0 {
		t.Fatalf("invalid range refusal = %#v", refused)
	}
}

// A step nobody observed, here one from before a service restart, stays an
// explicit gap: the reply names the uncovered range and points at git.
func TestRevisionDiffNamesGapItCannotAccountFor(t *testing.T) {
	root, stateDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	direct := newDirectWorkspaces(stateDir)
	opened := direct.call(context.Background(), "workspace_open", map[string]any{"kind": "project", "root": root})
	workspaceID := string(opened["workspace"].(workspacecore.Identity).ID)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspected := direct.call(context.Background(), "workspace_inspect", map[string]any{"workspace_id": workspaceID, "view": "status"})
	if revision := inspected["data"].(map[string]any)["revision"]; revision != "wsrev_2" {
		t.Fatalf("external write was not observed: %#v", inspected)
	}
	direct.closeProviders()

	restarted := newDirectWorkspaces(stateDir)
	defer restarted.closeProviders()
	result := restarted.call(context.Background(), "revision_diff", map[string]any{
		"root": root, "from_revision": "wsrev_1", "to_revision_or_current": "wsrev_2",
	})
	if result["outcome"] != "partial" || result["code"] != "diff_evidence_incomplete" {
		t.Fatalf("unobserved step = %#v", result)
	}
	if summary, _ := result["summary"].(string); !strings.Contains(summary, "wsrev_1..wsrev_2") {
		t.Fatalf("summary does not name the uncovered range: %q", summary)
	}
	next := mcpapi.AnySlice(result["next"])
	if len(next) == 0 || next[0].(map[string]any)["action"] != "inspect_uncovered_range_with_git" {
		t.Fatalf("partial diff does not point at git: %#v", next)
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

// An edit that named the repository with root is as visible to revision_diff
// as one that named the workspace: the receipt is filed under the workspace
// the root resolved to, not under the empty ID the arguments carried before
// it was resolved.
func TestRevisionDiffSeesEditsMadeThroughRoot(t *testing.T) {
	direct, workspaceID, root := openProbeProject(t, map[string]string{"note.txt": "alpha beta\n"})
	defer direct.closeProviders()
	workspace := direct.get(workspacecore.ID(workspaceID))
	from := fmt.Sprintf("wsrev_%d", workspace.Identity().StateSeq)
	applied := direct.call(context.Background(), "edit_apply", map[string]any{
		"root": root, "operation": map[string]any{"kind": "replace_literal", "old": "beta", "new": "gamma"},
	})
	if applied["outcome"] != "ok" && applied["outcome"] != "provisional" {
		t.Fatalf("edit through root failed: %#v", applied)
	}
	diffed := direct.call(context.Background(), "revision_diff", map[string]any{
		"root": root, "from_revision": from, "to_revision_or_current": "current",
	})
	if diffed["outcome"] != "ok" {
		t.Fatalf("revision diff did not cover the edit made through root: %#v", diffed)
	}
}

// A range that starts before an observed external write returns the native
// edit segment it knows and an inferred entry for the write.
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
	if diffed["outcome"] != "ok" {
		t.Fatalf("gap diff outcome = %#v", diffed)
	}
	data := diffed["data"].(map[string]any)
	diffs := data["diffs"].([]any)
	if len(data["inferred_segments"].([]any)) != 1 || len(diffs) != 2 {
		t.Fatalf("known diff segment or inferred entry missing: %#v", diffed)
	}
	if first := diffs[0].(map[string]any); first["path"] != "first.txt" || first["inferred"] != true {
		t.Fatalf("external write is not the first, inferred entry: %#v", diffs)
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
