package bridge

import (
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestRecordedRevisionDiffsDeduplicatesLaterPlanInspectionReceipt(t *testing.T) {
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
	direct.replays[prefix+"apply"] = &directReplay{complete: true, result: result()}
	direct.replays[prefix+"inspect"] = &directReplay{complete: true, result: result()}

	recorded := direct.recordedRevisionDiffs("workspace", 2, 3)
	if len(recorded) != 1 {
		t.Fatalf("duplicate committed plan receipts produced %d revision diffs: %#v", len(recorded), recorded)
	}
}

func TestModernProviderTargetPositionsSymbolHandleOnIdentifier(t *testing.T) {
	direct, workspaceID, _ := openProbeProject(t, map[string]string{
		"report.go": "package report\n\nfunc OperationalSummary() string { return \"ok\" }\n",
	})
	workspace := direct.get(workspacecore.ID(workspaceID))
	symbol, err := workspace.RegisterSymbolHandle(
		"report.go", "OperationalSummary", "function", len("package report\n\n"), len("package report\n\nfunc OperationalSummary() string { return \"ok\" }"),
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := modernProviderTarget(workspace, map[string]any{"handle": string(symbol.Handle)})
	if err != nil {
		t.Fatal(err)
	}
	if target["line"] != 3 || target["col"] != 6 || target["symbol"] != "OperationalSummary" {
		t.Fatalf("semantic target does not point at identifier: %#v", target)
	}
}
