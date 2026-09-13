package workspace

// What a proposal did to the diagnostics, and who in it did that.
//
// A prepared revision has its own diagnostic report. On its own that report
// answers the wrong question: it says what is wrong with the staged code, not
// what this change made wrong. A file with nine pre-existing warnings and one
// new error reads as ten problems, and the one that matters is buried.
//
// So the prepared report is compared with the base one, and each finding is
// placed: new, resolved, unchanged, or beyond comparison because the baseline
// itself was incomplete. A baseline nobody could compute is said out loud
// rather than treated as "nothing was wrong before", which would turn every
// pre-existing problem into something this change caused.
//
// Then each new finding is attributed, in the same evidence ranking the
// culprit attribution already uses: exact when it lands in bytes exactly one
// operation wrote, strong when it is in a file exactly one operation targeted,
// likely when the analysis snapshot connects it to one, ambiguous when several
// are plausible, unattributed when nothing supports a claim. A change a tool
// made - a formatter, a code action, a generator - is attributed to the tool
// and never assigned to the nearest user operation.

import "sort"

// DiagnosticDeltaKind places one finding between two reports.
type DiagnosticDeltaKind string

const (
	DeltaNew                DiagnosticDeltaKind = "new"
	DeltaResolved           DiagnosticDeltaKind = "resolved"
	DeltaUnchanged          DiagnosticDeltaKind = "unchanged"
	DeltaBaselineIncomplete DiagnosticDeltaKind = "baseline_incomplete"
)

// DeltaFinding is one finding with where it stands and who caused it.
type DeltaFinding struct {
	Kind        DiagnosticDeltaKind `json:"kind"`
	Path        string              `json:"path"`
	Line        int                 `json:"line,omitempty"`
	Severity    int                 `json:"severity,omitempty"`
	Code        string              `json:"code,omitempty"`
	Message     string              `json:"message"`
	Attribution OperationCulprit    `json:"attribution,omitempty"`
}

// OperationCulprit is who inside the plan is responsible, and on what
// evidence.
type OperationCulprit struct {
	Rank       string   `json:"rank"`
	Operations []string `json:"operations,omitempty"`
	Tool       string   `json:"tool,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
}

// DiagnosticDelta is the whole comparison.
type DiagnosticDelta struct {
	BaseRevision     string         `json:"base_revision"`
	PreparedRevision string         `json:"prepared_revision"`
	BaselineComplete bool           `json:"baseline_complete"`
	New              []DeltaFinding `json:"new,omitempty"`
	Resolved         []DeltaFinding `json:"resolved,omitempty"`
	UnchangedCount   int            `json:"unchanged_count"`
}

// ComparableFinding is one finding as the delta needs it, from either report.
type ComparableFinding struct {
	Path     string
	Line     int
	Severity int
	Code     string
	Message  string
}

// identity is what makes two findings the same finding across a change. The
// line is deliberately absent: an edit above a warning moves it without
// changing it, and a delta that called that "one resolved, one new" would
// report every edit as breaking something.
func (f ComparableFinding) identity() string {
	return f.Path + "\x00" + f.Code + "\x00" + f.Message
}

// AttributionInput is everything the ranking may draw on.
type AttributionInput struct {
	Hunks     []PlanHunk
	ToolPaths map[string]string
	Targets   map[string][]string
	Reachable map[string][]string
}

// CompareDiagnostics places every finding and attributes the new ones.
func CompareDiagnostics(baseRevision, preparedRevision string, base, prepared []ComparableFinding, baselineComplete bool, input AttributionInput) DiagnosticDelta {
	delta := DiagnosticDelta{
		BaseRevision: baseRevision, PreparedRevision: preparedRevision, BaselineComplete: baselineComplete,
	}
	baseline := map[string]int{}
	for _, finding := range base {
		baseline[finding.identity()]++
	}
	for _, finding := range prepared {
		if baseline[finding.identity()] > 0 {
			baseline[finding.identity()]--
			delta.UnchangedCount++
			continue
		}
		entry := DeltaFinding{
			Kind: DeltaNew, Path: finding.Path, Line: finding.Line, Severity: finding.Severity,
			Code: finding.Code, Message: finding.Message,
		}
		if !baselineComplete {
			// Nobody could say what was wrong before, so nobody can say this
			// is new. Saying so is the honest answer; guessing is not.
			entry.Kind = DeltaBaselineIncomplete
		}
		entry.Attribution = AttributeFinding(finding, input)
		delta.New = append(delta.New, entry)
	}
	for _, finding := range base {
		if baseline[finding.identity()] > 0 {
			baseline[finding.identity()]--
			delta.Resolved = append(delta.Resolved, DeltaFinding{
				Kind: DeltaResolved, Path: finding.Path, Line: finding.Line, Severity: finding.Severity,
				Code: finding.Code, Message: finding.Message,
			})
		}
	}
	return delta
}

// AttributeFinding ranks who caused one finding.
func AttributeFinding(finding ComparableFinding, input AttributionInput) OperationCulprit {
	if tool := input.ToolPaths[finding.Path]; tool != "" {
		// A formatter or a generator wrote this file. Handing its
		// consequences to whichever operation happens to be nearest is how a
		// tool's mistakes end up blamed on a person.
		return OperationCulprit{Rank: "exact", Tool: tool, Reasons: []string{"tool_delta_owns_file"}}
	}
	var exact []string
	for _, hunk := range input.Hunks {
		if hunk.Superseded || hunk.Path != finding.Path || finding.Line == 0 {
			continue
		}
		if finding.Line >= hunk.StartLine && finding.Line <= hunk.EndLine {
			exact = append(exact, hunk.OpID)
		}
	}
	if culprit, ok := decide(exact, "exact", "postimage_hunk"); ok {
		return culprit
	}
	if culprit, ok := decide(input.Targets[finding.Path], "strong", "declared_target"); ok {
		return culprit
	}
	if culprit, ok := decide(input.Reachable[finding.Path], "likely", "impact_path"); ok {
		return culprit
	}
	return OperationCulprit{Rank: "unattributed", Reasons: []string{"no_supporting_evidence"}}
}

// decide turns a set of candidates at one evidence level into a verdict: one
// candidate is that rank, several are ambiguous, none defers to the next
// level down.
func decide(candidates []string, rank, reason string) (OperationCulprit, bool) {
	unique := uniqueSorted(candidates)
	switch len(unique) {
	case 0:
		return OperationCulprit{}, false
	case 1:
		return OperationCulprit{Rank: rank, Operations: unique, Reasons: []string{reason}}, true
	default:
		return OperationCulprit{Rank: "ambiguous", Operations: unique, Reasons: []string{reason, "several_candidates"}}, true
	}
}

// ReachableFrom is the bounded impact path an attribution may use: the files
// that refer to the files a plan touched, from the snapshot.
func ReachableFrom(snapshot AnalysisSnapshot, targets map[string][]string) map[string][]string {
	nodes := map[string]string{}
	for _, node := range snapshot.Nodes {
		nodes[node.ID] = node.Path
	}
	reachable := map[string][]string{}
	for _, fact := range snapshot.Facts {
		to, toKnown := nodes[fact.To]
		from, fromKnown := nodes[fact.From]
		if !toKnown || !fromKnown {
			continue
		}
		// The operation changed `to`; `from` refers to it, so a new finding
		// in `from` may be this change arriving downstream.
		for _, opID := range targets[to] {
			reachable[from] = append(reachable[from], opID)
		}
	}
	for path := range reachable {
		reachable[path] = uniqueSorted(reachable[path])
		sort.Strings(reachable[path])
	}
	return reachable
}
