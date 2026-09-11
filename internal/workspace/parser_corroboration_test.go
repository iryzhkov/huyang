package workspace

import (
	"strings"
	"testing"
)

func TestCorroborateParserWithTrustedProjectCheck(t *testing.T) {
	result := VerificationResult{Stages: []VerificationStage{
		{Stage: "parser", Status: VerificationSkipped, Exit: -1, Coverage: Coverage{FilesConsidered: 2, Skipped: []string{"parser_unavailable:.py"}}},
		{Stage: "check", Status: VerificationPassed, Implementation: []string{"python", "-m", "compileall", "."}},
	}}
	corroborateParserWithProjectCheck(&result)
	parser := result.Stages[0]
	// The parser stage did not run a parser, so it stays unavailable; the
	// corroboration lives in coverage and confidence only.
	if parser.Status != VerificationSkipped || parser.Exit != -1 || parser.Coverage.Complete || parser.Coverage.FilesRead != 0 {
		t.Fatalf("corroboration relabelled the parser stage as if it ran: %#v", parser)
	}
	if parser.Confidence != ConfidenceCorroborated || parser.Coverage.Semantic != "configured_project_check" ||
		len(parser.Coverage.Skipped) != 1 || parser.Coverage.Skipped[0] != "parser_unavailable_corroborated_by_project_check:1" {
		t.Fatalf("corroboration not expressed in coverage: %#v", parser)
	}
}

func TestCanonicalStagesOrdersRequestsAndRejectsUnknownNames(t *testing.T) {
	ordered, err := CanonicalStages([]string{"tests", "parser", "format_gate", "tests", "check", "diagnostics"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"format_gate", "parser", "diagnostics", "check", "tests"}
	if strings.Join(ordered, ",") != strings.Join(want, ",") {
		t.Fatalf("ordered = %v, want %v", ordered, want)
	}
	if _, err := CanonicalStages([]string{"parser", "lint"}); err == nil {
		t.Fatal("unknown stage accepted")
	}
}

func TestFormattingClaimsAreExplicit(t *testing.T) {
	gate := CommandPolicy{Command: []string{"gofmt", "-l", "."}}
	var policy PipelinePolicy
	policy.Format.Gate = gate
	passed := formattingClaims(VerificationResult{Stages: []VerificationStage{
		{Stage: "format", Mode: "transform", Status: VerificationPassed, Writes: []string{"a.go"}},
		{Stage: "format_gate", Mode: "check", Status: VerificationPassed},
	}}, policy, []string{"format_gate"})
	if !passed.RepositoryFormatted || !passed.FormatGatePassed || passed.Provisional || passed.NotConfigured || passed.EditorFormatted {
		t.Fatalf("passed claims = %+v", passed)
	}
	timedOut := formattingClaims(VerificationResult{Stages: []VerificationStage{
		{Stage: "format_gate", Mode: "check", Status: VerificationTimedOut},
	}}, policy, []string{"format_gate"})
	if timedOut.FormatGatePassed || !timedOut.Provisional || len(timedOut.Reasons) == 0 || timedOut.Reasons[0] != "format_gate_timed_out" {
		t.Fatalf("timed-out claims = %+v", timedOut)
	}
	notRequested := formattingClaims(VerificationResult{}, policy, []string{"parser"})
	if !notRequested.Provisional || notRequested.Reasons[0] != "format_gate_not_requested" {
		t.Fatalf("not-requested claims = %+v", notRequested)
	}
	unconfigured := formattingClaims(VerificationResult{Stages: []VerificationStage{
		{Stage: "format_gate", Mode: "check", Status: VerificationSkipped, Coverage: Coverage{Skipped: []string{"not_configured"}}},
	}}, PipelinePolicy{}, []string{"format_gate"})
	if !unconfigured.NotConfigured || unconfigured.Provisional || unconfigured.FormatGatePassed || len(unconfigured.Reasons) != 1 {
		t.Fatalf("unconfigured claims = %+v", unconfigured)
	}
	failed := formattingClaims(VerificationResult{Stages: []VerificationStage{
		{Stage: "format_gate", Mode: "check", Status: VerificationFailed},
	}}, policy, []string{"format_gate"})
	if failed.FormatGatePassed || failed.Provisional || failed.NotConfigured || failed.Reasons[0] != "format_gate_failed" {
		t.Fatalf("failed claims = %+v", failed)
	}
}

func TestUnrelatedProjectCheckDoesNotCorroborateParser(t *testing.T) {
	result := VerificationResult{Stages: []VerificationStage{
		{Stage: "parser", Status: VerificationSkipped, Exit: -1, Coverage: Coverage{FilesConsidered: 1, Skipped: []string{"parser_unavailable:.py"}}},
		{Stage: "check", Status: VerificationPassed, Implementation: []string{"go", "version"}},
	}}
	corroborateParserWithProjectCheck(&result)
	if result.Stages[0].Status != VerificationSkipped {
		t.Fatalf("unrelated check invented parser coverage: %#v", result.Stages[0])
	}
}

func TestToolNameSubstringDoesNotCorroborateParser(t *testing.T) {
	if projectCheckCoversParserExtension(".go", []string{"cargo", "check"}) {
		t.Fatal("cargo must not be mistaken for the Go tool")
	}
}

func TestParserWithoutProjectCheckRemainsUnavailable(t *testing.T) {
	result := VerificationResult{Stages: []VerificationStage{
		{Stage: "parser", Status: VerificationSkipped, Exit: -1, Coverage: Coverage{FilesConsidered: 1, Skipped: []string{"parser_unavailable:.rb"}}},
	}}
	corroborateParserWithProjectCheck(&result)
	if result.Stages[0].Status != VerificationSkipped {
		t.Fatalf("parser invented coverage without trusted check: %#v", result.Stages[0])
	}
}
