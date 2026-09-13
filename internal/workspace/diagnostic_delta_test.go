package workspace

import "testing"

func deltaFinding(path string, line int, message string) ComparableFinding {
	return ComparableFinding{Path: path, Line: line, Severity: 1, Code: "E1", Message: message}
}

// The comparison answers "what did this change do", not "what is wrong with
// this code": a pre-existing problem is unchanged however far the edit moved
// it, a fixed one is resolved, and only what appeared is new.
func TestDiagnosticDeltaSeparatesWhatTheChangeDid(t *testing.T) {
	base := []ComparableFinding{
		deltaFinding("ledger.go", 4, "undefined: old"),
		deltaFinding("ledger.go", 40, "declared and not used: total"),
	}
	prepared := []ComparableFinding{
		// The same warning, seven lines further down because the edit above
		// it grew. It is not news.
		deltaFinding("ledger.go", 47, "declared and not used: total"),
		deltaFinding("ledger.go", 12, "cannot use \"seven\" as int"),
	}
	delta := CompareDiagnostics("wsrev_3", "prep_a", base, prepared, true, AttributionInput{
		Hunks: []PlanHunk{{OpID: "retype", Path: "ledger.go", StartLine: 10, EndLine: 14}},
	})
	if delta.UnchangedCount != 1 {
		t.Fatalf("a finding that only moved was not recognised: %#v", delta)
	}
	if len(delta.New) != 1 || delta.New[0].Message != "cannot use \"seven\" as int" {
		t.Fatalf("new = %#v", delta.New)
	}
	if len(delta.Resolved) != 1 || delta.Resolved[0].Message != "undefined: old" {
		t.Fatalf("resolved = %#v", delta.Resolved)
	}
	// It landed in the bytes one operation wrote.
	attribution := delta.New[0].Attribution
	if attribution.Rank != "exact" || len(attribution.Operations) != 1 || attribution.Operations[0] != "retype" {
		t.Fatalf("attribution = %#v", attribution)
	}
}

// A baseline nobody could compute is said out loud. Treating it as "nothing
// was wrong before" would report every pre-existing problem as caused by this
// change.
func TestAnIncompleteBaselineIsNotTreatedAsSilence(t *testing.T) {
	delta := CompareDiagnostics("wsrev_3", "prep_a", nil,
		[]ComparableFinding{deltaFinding("ledger.go", 12, "undefined: x")}, false, AttributionInput{})
	if delta.BaselineComplete {
		t.Fatal("an incomplete baseline was reported as complete")
	}
	if len(delta.New) != 1 || delta.New[0].Kind != DeltaBaselineIncomplete {
		t.Fatalf("a finding with no baseline behind it was called new: %#v", delta.New)
	}
}

// The ranking walks down the evidence: the bytes an operation wrote, then the
// file it targeted, then what the snapshot connects to it, and it says
// ambiguous rather than choosing between equals.
func TestAttributionRanksByTheEvidenceItHas(t *testing.T) {
	input := AttributionInput{
		Hunks: []PlanHunk{
			{OpID: "first", Path: "ledger.go", StartLine: 10, EndLine: 14},
			{OpID: "second", Path: "ledger.go", StartLine: 12, EndLine: 20},
			{OpID: "stale", Path: "ledger.go", StartLine: 1, EndLine: 40, Superseded: true},
		},
		Targets:   map[string][]string{"report.go": {"third"}},
		Reachable: map[string][]string{"store.go": {"third"}, "other.go": {"third", "fourth"}},
		ToolPaths: map[string]string{"zz_generated.go": "gofmt"},
	}
	cases := map[string]struct {
		finding ComparableFinding
		rank    string
		first   string
	}{
		"exact":        {deltaFinding("ledger.go", 11, "a"), "exact", "first"},
		"ambiguous":    {deltaFinding("ledger.go", 13, "b"), "ambiguous", "first"},
		"strong":       {deltaFinding("report.go", 3, "c"), "strong", "third"},
		"likely":       {deltaFinding("store.go", 3, "d"), "likely", "third"},
		"unattributed": {deltaFinding("elsewhere.go", 3, "e"), "unattributed", ""},
	}
	for name, probe := range cases {
		culprit := AttributeFinding(probe.finding, input)
		if culprit.Rank != probe.rank {
			t.Fatalf("%s: rank = %q, want %q (%#v)", name, culprit.Rank, probe.rank, culprit)
		}
		if probe.first != "" && (len(culprit.Operations) == 0 || culprit.Operations[0] != probe.first) {
			t.Fatalf("%s: operations = %v", name, culprit.Operations)
		}
	}
	// A superseded hunk does not claim a finding: those bytes belong to
	// whoever rewrote them.
	if culprit := AttributeFinding(deltaFinding("ledger.go", 35, "f"), input); culprit.Rank != "unattributed" {
		t.Fatalf("a superseded hunk claimed a finding: %#v", culprit)
	}
	// What a tool wrote is the tool's, whatever operations are nearby.
	culprit := AttributeFinding(deltaFinding("zz_generated.go", 2, "g"), input)
	if culprit.Tool != "gofmt" || len(culprit.Operations) != 0 {
		t.Fatalf("a tool's change was assigned to an operation: %#v", culprit)
	}
}

// The impact path is read from the snapshot: a file that refers to what an
// operation changed is where that change arrives downstream.
func TestReachableFromReadsTheSnapshot(t *testing.T) {
	builder := newAnalysisBuilder(AnalysisCaps{}.withDefaults())
	builder.Node(AnalysisNode{Kind: NodeFile, Path: "ledger.go"})
	builder.Node(AnalysisNode{Kind: NodeFile, Path: "report.go"})
	builder.Fact(NodeID(NodeFile, "report.go", ""), NodeID(NodeFile, "ledger.go", ""), EdgeReferences,
		FactProducer{Name: "embedded_nvim", Confidence: string(ConfidenceAuthoritative)})
	snapshot := builder.finish(AnalysisKey{Revision: "wsrev_3"})

	reachable := ReachableFrom(snapshot, map[string][]string{"ledger.go": {"retype"}})
	if len(reachable["report.go"]) != 1 || reachable["report.go"][0] != "retype" {
		t.Fatalf("the caller of a changed file is not reachable from it: %#v", reachable)
	}
	if len(reachable["ledger.go"]) != 0 {
		t.Fatalf("the changed file was reported as downstream of itself: %#v", reachable)
	}
}
