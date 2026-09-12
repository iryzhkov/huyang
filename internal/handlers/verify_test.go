package handlers

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iryzhkov/huyang/internal/mcpapi"
	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

func TestFinalizedEnvelopeBoundsNextAndFillsRequiredKeys(t *testing.T) {
	envelope := mcpapi.FinalizeEnvelope("verify_run", map[string]any{
		"request_id": "req_x", "outcome": "ok", "summary": "s",
		"next": []any{map[string]any{"a": 1}, map[string]any{"b": 2}, map[string]any{"c": 3}},
	})
	if next := envelope["next"].([]any); len(next) != mcpapi.MaxNextEntries || next[0].(map[string]any)["a"] != 1 {
		t.Fatalf("next = %#v", next)
	}
	assertModernOutputValid(t, envelope)

	verification := workspacecore.VerificationResult{Revision: "wsrev_1", Stages: []workspacecore.VerificationStage{
		{Stage: "parser", EvidenceIDs: []string{"ev_1"}}, {Stage: "check", EvidenceIDs: []string{"ev_2"}}, {Stage: "tests", EvidenceIDs: []string{"ev_3"}},
	}}
	envelope = mcpapi.FinalizeEnvelope("verify_run", modernVerificationEnvelope("req_v", nil, "ok", "", "done", "revision_miss", verification))
	if ids := envelope["evidence"].(map[string]any)["ids"].([]string); len(ids) != 3 {
		t.Fatalf("evidence ids = %#v", ids)
	}
	if next := envelope["next"].([]any); len(next) != 1 || next[0].(map[string]any)["evidence_count"] != 3 {
		t.Fatalf("verification next = %#v", next)
	}
	assertModernOutputValid(t, envelope)
}

func TestModernVerificationEnvelopeUsesEmptyEvidenceArray(t *testing.T) {
	envelope := modernVerificationEnvelope(
		"req_verify",
		nil,
		"ok",
		"",
		"verified",
		"revision_miss",
		workspacecore.VerificationResult{Revision: "wsrev_9"},
	)
	evidence := envelope["evidence"].(map[string]any)
	ids, ok := evidence["ids"].([]string)
	if !ok || ids == nil || len(ids) != 0 {
		t.Fatalf("evidence ids = %#v, want non-nil empty []string", evidence["ids"])
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"ids":[]`)) || bytes.Contains(encoded, []byte(`"ids":null`)) {
		t.Fatalf("encoded envelope has invalid empty evidence ids: %s", encoded)
	}
}

func TestModernVerificationEnvelopeBoundsRepeatedDetails(t *testing.T) {
	const itemCount = mcpapi.VerificationListLimit + 25
	paths := make([]string, 0, itemCount)
	selected := make([]workspacecore.SelectedTest, 0, itemCount)
	nodes := make([]workspacecore.ImpactNode, 0, itemCount)
	edges := make([]workspacecore.ImpactEdge, 0, itemCount)
	risks := make([]workspacecore.ImpactRisk, 0, itemCount)
	deltas := make([]workspacecore.ToolDelta, 0, itemCount)
	for index := 0; index < itemCount; index++ {
		path := "src/" + strings.Repeat("x", index+1) + ".py"
		paths = append(paths, path)
		selected = append(selected, workspacecore.SelectedTest{
			Name:     path,
			Reasons:  []string{"changed dependency"},
			Variants: []string{"python"},
		})
		nodes = append(nodes, workspacecore.ImpactNode{Path: path, Language: "python"})
		edges = append(edges, workspacecore.ImpactEdge{From: path, To: "tests/test_webhooks.py", Kind: "imports"})
		risks = append(risks, workspacecore.ImpactRisk{
			Kind:   "dynamic_import",
			Path:   path,
			Detail: "RAW_RISK_DETAIL_" + strings.Repeat("r", 256),
		})
		deltas = append(deltas, workspacecore.ToolDelta{
			Path:           path,
			Before:         []byte("RAW_BEFORE_" + strings.Repeat("b", 4096)),
			After:          []byte("RAW_AFTER_" + strings.Repeat("a", 4096)),
			BeforeExists:   true,
			AfterExists:    true,
			Classification: "modified",
		})
	}
	graph := workspacecore.ImpactGraph{
		Revision:      "wsrev_9",
		Changed:       paths,
		Nodes:         nodes,
		Edges:         edges,
		Affected:      paths,
		Untested:      paths,
		Risks:         risks,
		Adapters:      []string{"python"},
		Included:      []string{"python"},
		RecommendFull: true,
	}
	result := workspacecore.VerificationResult{
		Revision: "wsrev_9",
		Stages: []workspacecore.VerificationStage{{
			Stage:           "tests",
			Mode:            "check",
			Scope:           paths,
			StartedRevision: "wsrev_9",
			Exit:            0,
			Writes:          paths,
			Output:          "RAW_OUTPUT_BODY" + strings.Repeat("o", mcpapi.VerificationOutputLimit) + "\nok example.com/ledger\t0.01s\n",
			Status:          workspacecore.VerificationPassed,
			EvidenceIDs:     []string{"ev_tests", "ev_impact"},
			TestScope:       "affected",
			TestVerdict:     "passed",
			SelectedTests:   selected,
			ExecutedTests:   paths,
		}},
		ToolDelta: deltas,
		Impact:    &graph,
		Targeted: &workspacecore.TargetedTestResult{
			Status:   "passed",
			Selected: selected,
			Executed: paths,
			Graph:    graph,
		},
		FullTestGate: "required",
	}

	envelope := modernVerificationEnvelope("req_verify", nil, "ok", "", "verified", "revision_miss", result)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 18<<10 {
		t.Fatalf("compacted verify envelope = %d bytes, want <= %d", len(encoded), 18<<10)
	}
	for _, omitted := range []string{"RAW_OUTPUT_BODY", "RAW_RISK_DETAIL_", "RAW_BEFORE_", "RAW_AFTER_"} {
		if bytes.Contains(encoded, []byte(omitted)) {
			t.Fatalf("compacted verify envelope retained omitted payload %q", omitted)
		}
	}

	data := envelope["data"].(map[string]any)
	verification := data["verification"].(map[string]any)
	if verification["revision"] != "wsrev_9" || verification["stage_count"] != 1 || verification["details_truncated"] != true {
		t.Fatalf("verification summary = %#v", verification)
	}
	impact := verification["impact"].(map[string]any)
	if impact["node_count"] != itemCount || impact["risk_count"] != itemCount || impact["details_truncated"] != true {
		t.Fatalf("impact summary = %#v", impact)
	}
	if _, ok := impact["nodes"]; ok {
		t.Fatal("compacted impact unexpectedly contains nodes")
	}
	targeted := verification["targeted_tests"].(map[string]any)
	if _, ok := targeted["graph"]; ok {
		t.Fatal("compacted targeted tests unexpectedly contain duplicate graph")
	}
	if verification["tool_delta_count"] != itemCount || len(verification["tool_delta"].([]any)) != mcpapi.VerificationListLimit {
		t.Fatalf("tool delta summary = %#v", verification["tool_delta"])
	}
	stage := verification["stages"].([]any)[0].(map[string]any)
	if stage["status"] != workspacecore.VerificationPassed || stage["output_truncated"] != true {
		t.Fatalf("stage summary = %#v", stage)
	}
	// The passing stage keeps the closing line of its output as evidence,
	// bounded, and nothing else.
	tail, _ := stage["output_tail"].(string)
	if !strings.HasSuffix(tail, "ok example.com/ledger\t0.01s") || len(tail) > mcpapi.VerificationTailBytes {
		t.Fatalf("stage output tail = %q (%d bytes)", tail, len(tail))
	}
	evidence := envelope["evidence"].(map[string]any)
	next := envelope["next"].([]any)
	if !slices.Equal(evidence["ids"].([]string), []string{"ev_impact", "ev_tests"}) || len(next) != 1 ||
		next[0].(map[string]any)["evidence_id"] != "ev_impact" || next[0].(map[string]any)["evidence_count"] != 2 {
		t.Fatalf("evidence follow-up = %#v, next = %#v", evidence, envelope["next"])
	}
}

// The verification cache key includes the trust policy, so trusting a root
// after a cached run does not replay the untrusted result.
func TestVerificationCacheKeyChangesWhenTrustChanges(t *testing.T) {
	root := t.TempDir()
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configDir := filepath.Join(configRoot, "huyang")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[trust]\nroots = []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	untrusted := verificationPolicyFingerprint(root)
	trustedConfig := []byte("[trust]\nroots = [\"" + root + "\"]\n")
	if err := os.WriteFile(configPath, trustedConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	trusted := verificationPolicyFingerprint(root)
	if untrusted == trusted {
		t.Fatalf("verification cache fingerprint ignored trust change: %s", trusted)
	}
}

// When the impact graph recommends a full run and affected tests were
// unavailable, the verification envelope offers a runnable full
// verification of the same stages.
func TestVerificationEnvelopeOffersRunnableFullFallback(t *testing.T) {
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

// A verification in which every stage was skipped because the root is not
// trusted says so in the summary and names the user config file, instead of
// claiming completion; a mixed result stays partial with a count.
func TestVerificationOutcomeNamesWhyNothingRan(t *testing.T) {
	skipped := workspacecore.VerificationStage{Stage: "check", Status: workspacecore.VerificationSkipped, Coverage: workspacecore.Coverage{Skipped: []string{"workspace_not_trusted"}}}
	outcome, summary := verificationOutcome(workspacecore.VerificationResult{Stages: []workspacecore.VerificationStage{skipped, skipped}})
	if outcome != "partial" || !strings.Contains(summary, "not trusted") || !strings.Contains(summary, workspacecore.UserConfigPath()) {
		t.Fatalf("untrusted outcome=%q summary=%q", outcome, summary)
	}
	ran := workspacecore.VerificationStage{Stage: "tests", Status: workspacecore.VerificationPassed}
	outcome, summary = verificationOutcome(workspacecore.VerificationResult{Stages: []workspacecore.VerificationStage{ran, skipped}})
	if outcome != "partial" || !strings.Contains(summary, "1 stage(s) unavailable") {
		t.Fatalf("mixed outcome=%q summary=%q", outcome, summary)
	}
	if outcome, summary = verificationOutcome(workspacecore.VerificationResult{Stages: []workspacecore.VerificationStage{ran}}); outcome != "ok" || strings.Contains(summary, "unavailable") {
		t.Fatalf("clean outcome=%q summary=%q", outcome, summary)
	}
}

// Provider attach and diagnostic settle waits during verification stay
// within a routine tool call.
func TestVerificationProviderWaitsStayBounded(t *testing.T) {
	if providerpool.VerificationAttachWaitMS > 2000 || verificationDiagnosticSettleWait > 2*time.Second {
		t.Fatalf("verification provider waits are not routine-call bounded: attach=%dms settle=%s", providerpool.VerificationAttachWaitMS, verificationDiagnosticSettleWait)
	}
}
