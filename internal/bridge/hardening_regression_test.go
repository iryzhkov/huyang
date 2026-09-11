package bridge

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestModernVerificationEnvelopeRecommendsRunnableFullFallback(t *testing.T) {
	graph := workspacecore.ImpactGraph{Revision: "prep_123", RecommendFull: true}
	result := workspacecore.VerificationResult{
		Revision: "prep_123",
		Stages: []workspacecore.VerificationStage{
			{Stage: "check"},
			{Stage: "tests", TestScope: "affected", TestVerdict: "affected_tests_unavailable"},
		},
		Impact:   &graph,
		Targeted: &workspacecore.TargetedTestResult{Status: "unavailable", Graph: graph},
	}
	envelope := modernVerificationEnvelope("req_verify", nil, "partial", "", "verified", "revision_miss", result)
	next := envelope["next"].([]any)
	if len(next) != 1 {
		t.Fatalf("next = %#v, want one full-verification fallback", next)
	}
	action := next[0].(map[string]any)
	if action["tool"] != "verify_run" || action["revision_or_transaction"] != "prep_123" || action["test_scope"] != "full" || action["use_new_idempotency_key"] != true {
		t.Fatalf("fallback action = %#v", action)
	}
	if got := action["stages"].([]string); !reflect.DeepEqual(got, []string{"check", "tests"}) {
		t.Fatalf("fallback stages = %#v", got)
	}
}

func TestCompactRevisionDiffOmitsEndpointBodies(t *testing.T) {
	diff := workspacecore.ExactDiff{
		Path: "large.rb", BeforeSHA256: "before", AfterSHA256: "after",
		Before: []byte(strings.Repeat("a", 16*1024)),
		After:  []byte(strings.Repeat("b", 16*1024)),
		Patch:  "@@ -1 +1 @@\n-old\n+new\n",
	}
	encoded, err := json.Marshal(compactRevisionDiff(diff))
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

func TestVerificationProviderWaitsStayBounded(t *testing.T) {
	if verificationProviderAttachWaitMS > 2000 || verificationDiagnosticSettleWait > 2*time.Second {
		t.Fatalf("verification provider waits are not routine-call bounded: attach=%dms settle=%s", verificationProviderAttachWaitMS, verificationDiagnosticSettleWait)
	}
}

func TestReconcileParserSupportDistinguishesInstalledFromVerification(t *testing.T) {
	ruby := map[string]any{"filetype": "ruby", "treesitter_parser": true}
	reconcileParserSupport(ruby)
	if ruby["treesitter_parser_installed"] != true || ruby["verification_parser"] != false || ruby["treesitter_parser"] != false {
		t.Fatalf("ruby parser support = %#v", ruby)
	}

	jsonEntry := map[string]any{"filetype": "json", "treesitter_parser": true}
	reconcileParserSupport(jsonEntry)
	if jsonEntry["treesitter_parser_installed"] != true || jsonEntry["verification_parser"] != true || jsonEntry["treesitter_parser"] != true {
		t.Fatalf("json parser support = %#v", jsonEntry)
	}
}
