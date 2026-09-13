package mcpapi

import (
	"encoding/json"
	"strings"
	"testing"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// The text rendering summarises an orientation without dropping the other
// result fields.
func TestCompactTextDataSummarisesOrientationWithoutDroppingResults(t *testing.T) {
	orientation := workspacecore.Orientation{Entries: []workspacecore.Entry{{Path: "a.go"}, {Path: "b.go"}}}
	data := CompactTextData(map[string]any{"overview": orientation, "content": "kept"}).(map[string]any)
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

// The text envelope compacts a typed plan record so a large operation body
// never reaches the model.
func TestCompactTextEnvelopeBoundsTypedPlanPayload(t *testing.T) {
	large := strings.Repeat("large-marker-", 10000)
	envelope := map[string]any{
		"api_version": "huyang.workspace/v1alpha1",
		"request_id":  "req_test",
		"outcome":     "ok",
		"summary":     "Plan created",
		"data": map[string]any{
			"plan": workspacecore.PlanRecord{
				PlanID: "plan_test",
				Operations: []workspacecore.PlanOperation{{
					OpID:    "large-create",
					Kind:    workspacecore.OperationCreateFile,
					Path:    "large.txt",
					Content: large,
				}},
			},
		},
		"evidence": map[string]any{"ids": []string{}, "truncated": false},
		"warnings": []string{},
		"next":     []any{},
	}
	encoded, err := json.Marshal(CompactTextEnvelope(envelope))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 16<<10 || strings.Contains(string(encoded), large[:1024]) {
		t.Fatalf("compact text envelope retained large plan bytes: %d", len(encoded))
	}
}

// The default revision diff carries hashes and the patch, never the full
// before and after bodies.
func TestCompactRevisionDiffOmitsEndpointBodies(t *testing.T) {
	diff := workspacecore.ExactDiff{
		Path: "large.rb", BeforeSHA256: "before", AfterSHA256: "after",
		Before: []byte(strings.Repeat("a", 16*1024)),
		After:  []byte(strings.Repeat("b", 16*1024)),
		Patch:  "@@ -1 +1 @@\n-old\n+new\n",
	}
	encoded, err := json.Marshal(CompactRevisionDiff(diff))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"before":`) || strings.Contains(string(encoded), `"after":`) {
		t.Fatalf("endpoint bodies leaked into default revision diff: %s", encoded)
	}
	if len(encoded) > 1024 {
		t.Fatalf("compact revision diff unexpectedly large: %d bytes", len(encoded))
	}
	for _, want := range []string{"large.rb", "before_sha256", "after_sha256", "@@ -1 +1 @@"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("compact revision diff %s omitted %q", encoded, want)
		}
	}
}

// A committed plan's diffs travel as sizes and hashes. They used to travel
// as three fields set to null, which reads as "this commit changed nothing"
// rather than "the bodies are not in this reply".
func TestCompactedCommittedDiffsCarrySizesNotNulls(t *testing.T) {
	plan := workspacecore.PlanRecord{
		PlanID: "plan_test", State: workspacecore.PlanCommitted,
		Preparation: &workspacecore.PlanPreparation{
			PreparedRevision: "prep_test",
			CommittedDiffs: []workspacecore.ExactDiff{{
				Path: "ledger.go", BeforeSHA256: "before", AfterSHA256: "after",
				Before: []byte(strings.Repeat("a", 4096)), After: []byte(strings.Repeat("b", 4097)),
				Patch: "@@ bytes 10:20 @@\n-\"old\"\n+\"new\"\n",
			}},
		},
	}
	encoded, err := json.Marshal(CompactTextData(map[string]any{"plan": plan}))
	if err != nil {
		t.Fatal(err)
	}
	rendered := string(encoded)
	if strings.Contains(rendered, `"before":`) || strings.Contains(rendered, `"patch":`) {
		t.Fatalf("a committed diff carried its bodies: %s", rendered)
	}
	for _, want := range []string{`"before_bytes":4096`, `"after_bytes":4097`, `"patch_bytes":`, `"before_sha256":"before"`} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("a committed diff omitted %s: %s", want, rendered)
		}
	}
}

// A finding that was announced as new and has since gone stale is counted
// under stale_count and left out of new; the report's stale count is
// surfaced as is.
func TestCompactDiagnosticReportSeparatesStaleFromNew(t *testing.T) {
	report := workspacecore.DiagnosticReport{
		Confidence: workspacecore.ConfidenceAuthoritative,
		New: []workspacecore.DiagnosticItem{
			{ID: "diag_current", Status: workspacecore.DiagnosticStatusCurrent, Finding: workspacecore.DiagnosticFinding{Message: "current"}},
			{ID: "diag_stale", Status: workspacecore.DiagnosticStatusStale, Finding: workspacecore.DiagnosticFinding{Message: "stale"}},
		},
		StaleCount: 3, Cursor: "diagcur_7",
	}
	compact := CompactDiagnosticReport(report, "ok", false)["diagnostics"].(map[string]any)
	items := compact["new"].([]map[string]any)
	if compact["stale_count"] != 3 || compact["new_count"] != 1 || len(items) != 1 || items[0]["id"] != "diag_current" {
		t.Fatalf("compact report = %#v", compact)
	}
}
