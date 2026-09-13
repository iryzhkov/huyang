package workspace

import (
	"strings"
	"testing"
)

func summaryOf(files ...SemanticFileInput) SemanticChangeSummary {
	return BuildSemanticChangeSummary(SemanticSummaryInput{
		FromRevision: "wsrev_1", ToRevision: "prep_1", Files: files,
	})
}

func goFile(path, before, after string) SemanticFileInput {
	return SemanticFileInput{
		Path: path, Before: []byte(before), After: []byte(after),
		BeforeExists: before != "", AfterExists: after != "",
	}
}

func symbolChange(summary SemanticChangeSummary, name string) SymbolChange {
	for _, change := range summary.Symbols {
		if change.Name == name {
			return change
		}
	}
	return SymbolChange{}
}

func mentions(values []string, needle string) bool {
	for _, value := range values {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

// A rename is one change, not a removal and an addition a reader has to pair
// up by eye.
func TestARenamedDeclarationIsReportedAsARename(t *testing.T) {
	summary := summaryOf(goFile("report.go",
		"package p\n\nfunc Report() int { return 1 }\n",
		"package p\n\nfunc Summary() int { return 1 }\n"))
	change := symbolChange(summary, "Report")
	if change.Kind != "renamed" || change.To != "Summary" {
		t.Fatalf("symbols = %#v", summary.Symbols)
	}
	// It is still a break for anybody outside this repository.
	if !mentions(summary.Recommendations, "Report") {
		t.Fatalf("recommendations = %#v", summary.Recommendations)
	}
}

// The same declaration in another file is a move, and the summary says where
// it went.
func TestAMovedDeclarationNamesItsNewFile(t *testing.T) {
	summary := summaryOf(
		goFile("ledger.go", "package p\n\nfunc Total() int { return 7 }\n", "package p\n"),
		goFile("totals.go", "", "package p\n\nfunc Total() int { return 7 }\n"),
	)
	change := symbolChange(summary, "Total")
	if change.Kind != "moved" || change.To != "totals.go" {
		t.Fatalf("symbols = %#v", summary.Symbols)
	}
}

// Two declarations with the same shape are two candidates, and a guess between
// them is not evidence.
func TestAnAmbiguousShapeIsNotPairedIntoARename(t *testing.T) {
	summary := summaryOf(goFile("ledger.go",
		"package p\n\nfunc Total() int { return 7 }\n",
		"package p\n\nfunc First() int { return 7 }\n\nfunc Second() int { return 8 }\n"))
	if change := symbolChange(summary, "Total"); change.Kind != "removed" {
		t.Fatalf("an ambiguous pairing was guessed: %#v", summary.Symbols)
	}
}

// A file nobody could read still appears in the index: a file missing from a
// summary reads as a file with nothing to say.
func TestEveryAffectedFileStaysInTheIndex(t *testing.T) {
	summary := summaryOf(
		goFile("ledger.go", "package p\n", "package p\n\nfunc Total() int { return 7 }\n"),
		SemanticFileInput{Path: "deploy.yaml", Before: []byte("a: 1\n"), After: []byte("a: 2\n"), BeforeExists: true, AfterExists: true},
	)
	if len(summary.Files) != 2 {
		t.Fatalf("files = %#v", summary.Files)
	}
	var config SemanticFileSummary
	for _, file := range summary.Files {
		if file.Path == "deploy.yaml" {
			config = file
		}
	}
	if config.Covered || config.Reason == "" {
		t.Fatalf("the unreadable file claims coverage: %#v", config)
	}
	if !mentions(summary.Gaps, "no schema adapter") {
		t.Fatalf("a changed schema file was not reported as a risk: %#v", summary.Gaps)
	}
}

// "Nothing changed" is a claim about coverage as much as about code.
func TestNoSemanticChangeIsOnlyClaimedWithCompleteCoverage(t *testing.T) {
	summary := summaryOf(goFile("ledger.go",
		"package p\n\nfunc Total() int { return 7 }\n",
		"package p\n\nfunc Total() int {\n\treturn 7\n}\n"))
	if summary.Complete {
		t.Fatalf("a summary with no diagnostics and no tests called itself complete: %#v", summary.Gaps)
	}
	if !strings.Contains(summary.Summary, "coverage behind that is incomplete") {
		t.Fatalf("summary = %q", summary.Summary)
	}
}

// What a file says it depends on is read from the file itself.
func TestAddedAndRemovedImportsAreReportedAsEdges(t *testing.T) {
	summary := summaryOf(goFile("report.go",
		"package p\n\nimport \"strings\"\n\nfunc Report() string { return strings.TrimSpace(\"\") }\n",
		"package p\n\nimport \"fmt\"\n\nfunc Report() string { return fmt.Sprint(1) }\n"))
	var added, removed bool
	for _, edge := range summary.Edges {
		if edge.Target == "fmt" && edge.Change == "added" {
			added = true
		}
		if edge.Target == "strings" && edge.Change == "removed" {
			removed = true
		}
	}
	if !added || !removed {
		t.Fatalf("edges = %#v", summary.Edges)
	}
}

// Changing behaviour without touching a test is a gap worth saying out loud.
func TestBehaviourWithoutATestIsRecommendedAgainst(t *testing.T) {
	summary := summaryOf(goFile("ledger.go",
		"package p\n\nfunc Total() int { return 7 }\n",
		"package p\n\nfunc Total() int { return 8 }\n\nfunc Extra() int { return 1 }\n"))
	if !mentions(summary.Recommendations, "no test file changed") {
		t.Fatalf("recommendations = %#v", summary.Recommendations)
	}
}

// A test that changes with the source answers that recommendation.
func TestAChangedTestAnswersTheCoverageRecommendation(t *testing.T) {
	summary := summaryOf(
		goFile("ledger.go", "package p\n\nfunc Total() int { return 7 }\n", "package p\n\nfunc Total() int { return 8 }\n"),
		goFile("ledger_test.go", "package p\n", "package p\n\nimport \"testing\"\n\nfunc TestTotal(t *testing.T) {}\n"),
	)
	if mentions(summary.Recommendations, "no test file changed") {
		t.Fatalf("recommendations = %#v", summary.Recommendations)
	}
}

// A signature change is reported as one, and named as a break.
func TestASignatureChangeIsBothASymbolChangeAndABreak(t *testing.T) {
	summary := summaryOf(goFile("ledger.go",
		"package p\n\nfunc Total() int { return 7 }\n",
		"package p\n\nfunc Total() (int, error) { return 7, nil }\n"))
	if change := symbolChange(summary, "Total"); change.Kind != "signature_changed" {
		t.Fatalf("symbols = %#v", summary.Symbols)
	}
	breaking := false
	for _, change := range summary.API {
		if change.Name == "Total" && change.Breaking() {
			breaking = true
		}
	}
	if !breaking {
		t.Fatalf("api = %#v", summary.API)
	}
}
