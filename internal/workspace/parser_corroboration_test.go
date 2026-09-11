package workspace

import "testing"

func TestCorroborateParserWithTrustedProjectCheck(t *testing.T) {
	result := VerificationResult{Stages: []VerificationStage{
		{Stage: "parser", Status: VerificationSkipped, Exit: -1, Coverage: Coverage{FilesConsidered: 2, Skipped: []string{"parser_unavailable:.py"}}},
		{Stage: "check", Status: VerificationPassed, Implementation: []string{"python", "-m", "compileall", "."}},
	}}
	corroborateParserWithProjectCheck(&result)
	parser := result.Stages[0]
	if parser.Status != VerificationPassed || !parser.Coverage.Complete || parser.Coverage.Semantic != "configured_project_check" || parser.Coverage.FilesRead != 2 {
		t.Fatalf("corroborated parser = %#v", parser)
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
