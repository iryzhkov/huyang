package workspace

// What a change means, rather than which lines moved.
//
// A diff answers "what bytes differ". The question before applying a change is
// a different one: which declarations came and went, what the code outside
// this repository can no longer rely on, what it now imports, what the
// language server made of it, whether the tests ran, and which of the plan's
// own assertions held. One summary answers all of that from the exact bytes on
// both sides, so two callers asking about the same change get the same answer.
//
// The index is the part that must not shrink. Every affected file stays in it
// even when nothing could be read from that file, because a file missing from
// a summary reads as a file with nothing to say. A dimension nobody could
// cover is a gap with a name, and "no semantic change" is a claim this summary
// makes only when the coverage behind it is complete.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// semanticDetailLimit bounds each list of details in one summary. The index
// of affected files is never bounded: it is what makes the rest readable.
const semanticDetailLimit = 50

// SemanticFileSummary is one affected file's place in the index.
type SemanticFileSummary struct {
	Path string `json:"path"`
	// Change is added, removed or modified.
	Change string `json:"change"`
	// Covered says an adapter read this file's declarations on both sides.
	Covered bool   `json:"covered"`
	Reason  string `json:"reason,omitempty"`
	// Declarations is how many the adapter saw after the change.
	Declarations int `json:"declarations"`
}

// SymbolChange is one declaration that came, went, moved or changed shape.
type SymbolChange struct {
	Path string `json:"path"`
	Name string `json:"name"`
	// Kind is added, removed, renamed, moved, signature_changed or
	// members_changed.
	Kind string `json:"kind"`
	// To names where a renamed or moved declaration went.
	To string `json:"to,omitempty"`
	// Shape is the declaration's kind and signature. Two declarations with
	// the same shape on either side of a change are what a rename or a move
	// looks like from the outside.
	Shape  string `json:"shape,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// EdgeChange is one dependency the change adds or removes. Only what a file
// says about itself is read here: an import line is a claim the file makes,
// and resolving it to another file needs the analysis snapshot.
type EdgeChange struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Change is added or removed.
	Change string `json:"change"`
}

// SemanticTestSummary is what the tests say about this change.
type SemanticTestSummary struct {
	Status   string   `json:"status"`
	Scope    string   `json:"scope,omitempty"`
	Verdict  string   `json:"verdict,omitempty"`
	Selected []string `json:"selected,omitempty"`
	Executed []string `json:"executed,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// SemanticChangeSummary is the whole answer.
type SemanticChangeSummary struct {
	FromRevision string `json:"from_revision"`
	ToRevision   string `json:"to_revision"`
	// Files is the complete index of what the change touches.
	Files           []SemanticFileSummary `json:"files"`
	Symbols         []SymbolChange        `json:"symbols,omitempty"`
	API             []APIChange           `json:"api,omitempty"`
	Edges           []EdgeChange          `json:"edges,omitempty"`
	Diagnostics     *DiagnosticDelta      `json:"diagnostics,omitempty"`
	Tests           SemanticTestSummary   `json:"tests"`
	Invariants      []PlanInvariant       `json:"invariants,omitempty"`
	Gaps            []string              `json:"gaps,omitempty"`
	Recommendations []string              `json:"recommendations,omitempty"`
	// Complete says every dimension this summary reports on was covered. It is
	// what separates "nothing changed" from "nothing could be seen to change".
	Complete    bool     `json:"complete"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	Summary     string   `json:"summary"`
}

// SemanticFileInput is one file's exact bytes on both sides of the change.
type SemanticFileInput struct {
	Path                      string
	Before, After             []byte
	BeforeExists, AfterExists bool
}

// SemanticSummaryInput is everything one summary is built from. What is
// missing is as meaningful as what is present: a nil Diagnostics and an empty
// Verification are gaps, and they are reported as such.
type SemanticSummaryInput struct {
	FromRevision string
	ToRevision   string
	Files        []SemanticFileInput
	Diagnostics  *DiagnosticDelta
	Verification []VerificationStage
	Invariants   []PlanInvariant
	EvidenceIDs  []string
	// Gaps the caller already knows about: no sandbox to diagnose, no
	// language server, a revision range with no content behind it.
	Gaps []string
}

// BuildSemanticChangeSummary is the one place a change is turned into meaning,
// so `change_plan`, `revision_diff` and a prepared result cannot disagree.
func BuildSemanticChangeSummary(input SemanticSummaryInput) SemanticChangeSummary {
	summary := SemanticChangeSummary{
		FromRevision: input.FromRevision, ToRevision: input.ToRevision,
		Files:       make([]SemanticFileSummary, 0, len(input.Files)),
		Diagnostics: input.Diagnostics, Invariants: input.Invariants,
		Gaps: append([]string(nil), input.Gaps...), EvidenceIDs: append([]string(nil), input.EvidenceIDs...),
	}
	var removed, added []SymbolChange
	for _, file := range sortedFiles(input.Files) {
		entry, changes, edges := summariseFile(file)
		summary.Files = append(summary.Files, entry)
		if !entry.Covered {
			summary.Gaps = append(summary.Gaps, entry.Path+": "+entry.Reason)
		}
		if gap := DeclarationCoverageGap(file.Path); gap != "" && entry.Covered {
			summary.Gaps = append(summary.Gaps, gap)
		}
		for _, change := range changes {
			switch change.Kind {
			case "removed":
				removed = append(removed, change)
			case "added":
				added = append(added, change)
			default:
				summary.Symbols = append(summary.Symbols, change)
			}
		}
		summary.Edges = append(summary.Edges, edges...)
		summary.API = append(summary.API, publicChanges(file)...)
	}
	summary.Symbols = append(summary.Symbols, pairRenames(removed, added)...)
	sort.Slice(summary.Symbols, func(i, j int) bool {
		if summary.Symbols[i].Path != summary.Symbols[j].Path {
			return summary.Symbols[i].Path < summary.Symbols[j].Path
		}
		return summary.Symbols[i].Name < summary.Symbols[j].Name
	})
	summary.Tests = summariseTests(input.Verification)
	summary.Gaps = append(summary.Gaps, missingDimensions(input)...)
	summary.Gaps = boundedList(uniqueStrings(summary.Gaps))
	summary.Complete = len(summary.Gaps) == 0
	summary.Recommendations = boundedList(recommendations(summary, input))
	summary.Symbols = boundedSymbols(summary.Symbols)
	summary.Edges = boundedEdges(summary.Edges)
	summary.Summary = describeSummary(summary)
	return summary
}

func sortedFiles(files []SemanticFileInput) []SemanticFileInput {
	sorted := append([]SemanticFileInput(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	return sorted
}

// summariseFile places one file in the index and reads its declarations and
// its imports on both sides.
func summariseFile(file SemanticFileInput) (SemanticFileSummary, []SymbolChange, []EdgeChange) {
	entry := SemanticFileSummary{Path: file.Path, Change: fileChangeKind(file)}
	before := ReadDeclarationSurface(file.Path, file.Before, file.BeforeExists)
	after := ReadDeclarationSurface(file.Path, file.After, file.AfterExists)
	entry.Declarations = len(after.Symbols)
	entry.Covered = before.Covered && after.Covered
	if !entry.Covered {
		entry.Reason = firstNonEmpty(before.Reason, after.Reason, "this file could not be read")
		return entry, nil, importChanges(file)
	}
	var changes []SymbolChange
	for _, change := range CompareAPISurfaces(before, after) {
		symbol := before.Symbols[change.Name]
		if change.Kind == "added" {
			symbol = after.Symbols[change.Name]
		}
		changes = append(changes, SymbolChange{
			Path: file.Path, Name: change.Name, Kind: change.Kind,
			Shape: shapeOf(symbol), Detail: change.Detail,
		})
	}
	return entry, changes, importChanges(file)
}

func fileChangeKind(file SemanticFileInput) string {
	switch {
	case !file.BeforeExists && file.AfterExists:
		return "added"
	case file.BeforeExists && !file.AfterExists:
		return "removed"
	}
	return "modified"
}

// publicChanges are the exported-surface differences, which are the ones that
// reach code this workspace does not contain.
func publicChanges(file SemanticFileInput) []APIChange {
	before := ReadAPISurface(file.Path, file.Before, file.BeforeExists)
	after := ReadAPISurface(file.Path, file.After, file.AfterExists)
	if !before.Covered || !after.Covered {
		return nil
	}
	return CompareAPISurfaces(before, after)
}

// importChanges reads what each side of the file says it depends on.
func importChanges(file SemanticFileInput) []EdgeChange {
	language := impactLanguage(file.Path)
	if language == "" {
		return nil
	}
	before := map[string]bool{}
	for _, module := range impactImports(language, string(file.Before)) {
		before[module] = true
	}
	var changes []EdgeChange
	for _, module := range impactImports(language, string(file.After)) {
		if before[module] {
			delete(before, module)
			continue
		}
		changes = append(changes, EdgeChange{Path: file.Path, Kind: "imports", Target: module, Change: "added"})
	}
	for module := range before {
		changes = append(changes, EdgeChange{Path: file.Path, Kind: "imports", Target: module, Change: "removed"})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Target < changes[j].Target })
	return changes
}

// pairRenames matches what disappeared against what appeared. Two declarations
// with the same shape are the same declaration under a new name or in a new
// file, and saying so is more useful than reporting a removal and an addition
// that a reader has to pair up by eye. A shape that appears more than once is
// left unpaired: a guess between two candidates is not evidence.
func pairRenames(removed, added []SymbolChange) []SymbolChange {
	sort.Slice(removed, func(i, j int) bool { return removed[i].Name < removed[j].Name })
	sort.Slice(added, func(i, j int) bool { return added[i].Name < added[j].Name })
	var paired []SymbolChange
	used := map[int]bool{}
	for _, gone := range removed {
		match, matches := -1, 0
		for index, arrival := range added {
			if used[index] || gone.Shape == "" || arrival.Shape != gone.Shape {
				continue
			}
			match, matches = index, matches+1
		}
		if matches != 1 {
			paired = append(paired, gone)
			continue
		}
		arrival := added[match]
		used[match] = true
		kind, to := "renamed", arrival.Name
		if arrival.Path != gone.Path {
			kind, to = "moved", arrival.Path+"#"+arrival.Name
			if arrival.Name == gone.Name {
				to = arrival.Path
			}
		}
		paired = append(paired, SymbolChange{
			Path: gone.Path, Name: gone.Name, Kind: kind, To: to, Shape: gone.Shape,
			Detail: fmt.Sprintf("%s became %s with the same shape", gone.Name, arrival.Name),
		})
	}
	for index, arrival := range added {
		if !used[index] {
			paired = append(paired, arrival)
		}
	}
	return paired
}

// shapeOf is what a declaration looks like without its name: its kind, its
// signature and its members. A declaration with no signature at all - a
// constant whose type is inferred, say - has no shape, and nothing is paired
// on nothing.
func shapeOf(symbol APISymbol) string {
	if symbol.Kind == "" || (symbol.Signature == "" && len(symbol.Members) == 0) {
		return ""
	}
	return symbol.Kind + " " + symbol.Signature + " {" + strings.Join(symbol.Members, "; ") + "}"
}

func summariseTests(stages []VerificationStage) SemanticTestSummary {
	for _, stage := range stages {
		if stage.Stage != "tests" {
			continue
		}
		summary := SemanticTestSummary{
			Status: string(stage.Status), Scope: stage.TestScope, Verdict: stage.TestVerdict,
			Executed: append([]string(nil), stage.ExecutedTests...),
		}
		for _, selected := range stage.SelectedTests {
			summary.Selected = append(summary.Selected, selected.Name)
		}
		if stage.Status != VerificationPassed && stage.Status != VerificationFailed {
			summary.Detail = "the tests stage did not run: " + strings.Join(stage.Coverage.Skipped, ", ")
		}
		return summary
	}
	return SemanticTestSummary{Status: "not_run", Detail: "this change was not accompanied by a test run"}
}

// missingDimensions names what this summary could not see. Each one is a
// sentence about evidence, not about the code.
func missingDimensions(input SemanticSummaryInput) []string {
	var gaps []string
	if input.Diagnostics == nil {
		gaps = append(gaps, "diagnostics: no language server answered about this change")
	} else if !input.Diagnostics.BaselineComplete {
		gaps = append(gaps, "diagnostics: the baseline was incomplete, so nothing can be called new")
	}
	if summary := summariseTests(input.Verification); summary.Status != string(VerificationPassed) && summary.Status != string(VerificationFailed) {
		gaps = append(gaps, "tests: "+firstNonEmpty(summary.Detail, "no test stage ran"))
	}
	for _, file := range input.Files {
		if schemaPath(file.Path) {
			gaps = append(gaps, file.Path+": no schema adapter reads this format, so its compatibility is unknown")
		}
	}
	// Call and implementation edges come from the analysis snapshot, which
	// describes the canonical revision; what a file imports is read from the
	// file itself and is all this summary claims.
	gaps = append(gaps, "edges: only the imports each file declares are read; call and implementation edges need the analysis snapshot")
	gaps = append(gaps, "execution: new error paths and side effects need the static execution graph, which this service does not build")
	return gaps
}

func schemaPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".yaml", ".yml", ".toml", ".proto", ".graphql", ".sql":
		return true
	}
	return false
}

// recommendations are what a reader should do about what this says. They are
// derived, never invented: each one points at something in the summary.
func recommendations(summary SemanticChangeSummary, input SemanticSummaryInput) []string {
	var advice []string
	for _, change := range summary.API {
		if change.Breaking() {
			advice = append(advice, "callers outside this repository depend on "+change.Name+": "+change.Detail)
		}
	}
	if sourceChanged(input.Files) && !testChanged(input.Files) {
		advice = append(advice, "no test file changed beside the source: the new behaviour is not covered by this change")
	}
	for _, invariant := range summary.Invariants {
		if invariant.Status != InvariantProven {
			advice = append(advice, "invariant "+invariant.describe())
		}
	}
	if summary.Tests.Status == "not_run" || summary.Tests.Status == string(VerificationSkipped) {
		advice = append(advice, "run verify_run with test_scope affected: nothing here says the behaviour still works")
	}
	return advice
}

func sourceChanged(files []SemanticFileInput) bool {
	for _, file := range files {
		if !isTestPath(file.Path) && apiAdapterFor(file.Path) != "" {
			return true
		}
	}
	return false
}

func testChanged(files []SemanticFileInput) bool {
	for _, file := range files {
		if isTestPath(file.Path) {
			return true
		}
	}
	return false
}

// describeSummary is the one line a reader sees first. It never says nothing
// changed unless the coverage behind that claim is complete.
func describeSummary(summary SemanticChangeSummary) string {
	if len(summary.Symbols) == 0 && len(summary.API) == 0 && len(summary.Edges) == 0 {
		if summary.Complete {
			return fmt.Sprintf("No semantic change across %d file(s)", len(summary.Files))
		}
		return fmt.Sprintf("No semantic change was seen across %d file(s), and the coverage behind that is incomplete", len(summary.Files))
	}
	breaking := 0
	for _, change := range summary.API {
		if change.Breaking() {
			breaking++
		}
	}
	return fmt.Sprintf("%d file(s), %d symbol change(s), %d breaking API change(s), %d edge change(s)",
		len(summary.Files), len(summary.Symbols), breaking, len(summary.Edges))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	return unique
}

func boundedList(values []string) []string {
	if len(values) <= semanticDetailLimit {
		return values
	}
	bounded := append([]string(nil), values[:semanticDetailLimit]...)
	return append(bounded, fmt.Sprintf("and %d more", len(values)-semanticDetailLimit))
}

func boundedSymbols(changes []SymbolChange) []SymbolChange {
	if len(changes) <= semanticDetailLimit {
		return changes
	}
	return changes[:semanticDetailLimit]
}

func boundedEdges(changes []EdgeChange) []EdgeChange {
	if len(changes) <= semanticDetailLimit {
		return changes
	}
	return changes[:semanticDetailLimit]
}
