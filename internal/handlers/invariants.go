package handlers

// Evaluating what a plan declared its result must satisfy.
//
// Each kind is answered by the strongest evidence available for it and by
// nothing weaker. The diagnostic assertion reads the prepared delta, so it
// inherits that comparison's honesty about an incomplete baseline. The test
// assertion reads the verification the sandbox pipeline already ran. The
// symbol assertions read the staged bytes, and the reference assertion asks
// the language server that read them.
//
// Every one of them can answer "unknown", and unknown is not a pass: a
// required invariant nobody could evaluate keeps the plan out of READY
// exactly as a violated one does.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/iryzhkov/huyang/internal/providerpool"
	workspacecore "github.com/iryzhkov/huyang/internal/workspace"
)

// planInvariantEvaluator answers a plan's invariants against the sandbox its
// prepare has just staged.
type planInvariantEvaluator struct {
	handlers  *Handlers
	requestID string
	workspace *workspacecore.Workspace
	stager    workspacecore.PlanStager
}

func (h *Handlers) invariantEvaluator(requestID string, workspace *workspacecore.Workspace, stager workspacecore.PlanStager) workspacecore.InvariantEvaluator {
	return planInvariantEvaluator{handlers: h, requestID: requestID, workspace: workspace, stager: stager}
}

// preparedViewer is the part of a stager that knows where its staged bytes
// live. A stager without one - a plain buffer-backed stager in a test - can
// still answer the invariants that read verification evidence.
type preparedViewer interface {
	Prepared() (providerpool.PreparedView, bool)
}

func (e planInvariantEvaluator) EvaluateInvariants(ctx context.Context, plan workspacecore.PlanRecord, preparation workspacecore.PlanPreparation) []workspacecore.PlanInvariant {
	// The plan record does not carry this preparation yet - it is being
	// decided right now - and the attribution reads it for the files a tool
	// wrote, so it is attached to the copy this evaluation works from.
	plan.Preparation = &preparation
	var view providerpool.PreparedView
	staged := false
	if viewer, ok := e.stager.(preparedViewer); ok {
		view, staged = viewer.Prepared()
	}
	answers := make([]workspacecore.PlanInvariant, 0, len(plan.Invariants))
	for _, invariant := range plan.Invariants {
		answers = append(answers, e.evaluate(ctx, plan, preparation, view, staged, invariant))
	}
	return answers
}

func (e planInvariantEvaluator) evaluate(ctx context.Context, plan workspacecore.PlanRecord, preparation workspacecore.PlanPreparation, view providerpool.PreparedView, staged bool, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	if invariant.Kind == workspacecore.InvariantTestsPass {
		return testsPassInvariant(invariant, preparation)
	}
	if !staged {
		return invariantUnknown(invariant, "the prepared revision is not readable, so this could not be evaluated")
	}
	switch invariant.Kind {
	case workspacecore.InvariantNoNewDiagnostics:
		return e.noNewDiagnostics(ctx, plan, view, invariant)
	case workspacecore.InvariantNoReferences:
		return e.noReferences(ctx, view, invariant)
	case workspacecore.InvariantSymbolExists, workspacecore.InvariantSymbolAbsent:
		return stagedSymbolInvariant(view, invariant)
	case workspacecore.InvariantAPICompatible:
		return e.apiCompatible(plan, view, invariant)
	case workspacecore.InvariantPathUnreachable:
		return e.pathUnreachable(ctx, view, invariant)
	}
	return invariantUnknown(invariant, fmt.Sprintf("%s is not evaluated by this service", invariant.Kind))
}

// testsPassInvariant reads the test stage of the verification the prepare
// already ran. A stage that did not run is unknown: a test suite nobody
// executed proves nothing about the code.
func testsPassInvariant(invariant workspacecore.PlanInvariant, preparation workspacecore.PlanPreparation) workspacecore.PlanInvariant {
	for _, stage := range preparation.Verification {
		if stage.Stage != "tests" {
			continue
		}
		answer := invariant
		answer.EvidenceIDs = append([]string(nil), stage.EvidenceIDs...)
		answer.Coverage = stage.Coverage
		switch stage.Status {
		case workspacecore.VerificationPassed:
			answer.Status = workspacecore.InvariantProven
			answer.Detail = fmt.Sprintf("the tests stage passed over %s", testScopeOf(stage))
		case workspacecore.VerificationFailed:
			answer.Status = workspacecore.InvariantViolated
			answer.Detail = fmt.Sprintf("the tests stage failed (exit %d)", stage.Exit)
		default:
			answer.Status = workspacecore.InvariantUnknown
			answer.Detail = fmt.Sprintf("the tests stage was %s", stage.Status)
		}
		return answer
	}
	return invariantUnknown(invariant, "this preparation ran no tests stage")
}

func testScopeOf(stage workspacecore.VerificationStage) string {
	if stage.TestScope == "" {
		return "the configured tests"
	}
	return stage.TestScope + " tests"
}

// noNewDiagnostics is proven only by a complete comparison. An incomplete
// baseline, a file the server could not answer about, and more staged files
// than one call diagnoses all mean the same thing: this was not established.
func (e planInvariantEvaluator) noNewDiagnostics(ctx context.Context, plan workspacecore.PlanRecord, view providerpool.PreparedView, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	files := e.invariantFiles(view, invariant)
	if len(files) == 0 {
		return invariantUnknown(invariant, "this preparation staged no file this invariant is about")
	}
	if len(files) > preparedDiagnosticBudget {
		return invariantUnknown(invariant, fmt.Sprintf(
			"the plan stages %d files and one call diagnoses %d, so no complete comparison was made",
			len(files), preparedDiagnosticBudget))
	}
	var findings []workspacecore.ComparableFinding
	for _, path := range files {
		absolute, err := preparedPath(view, path)
		if err != nil {
			return invariantUnknown(invariant, err.Error())
		}
		value, failure := e.handlers.callPrepared(ctx, e.requestID+"_"+path, e.workspace, view, "diagnostics", map[string]any{
			"root": view.Tree, "file": absolute,
		})
		if failure != nil {
			return invariantUnknown(invariant, fmt.Sprintf("no diagnostics could be read for %s", path))
		}
		findings = append(findings, comparableFindings(path, value)...)
	}
	delta := e.handlers.preparedDelta(ctx, e.requestID, e.workspace, plan, view, findings, files)
	if delta == nil {
		return invariantUnknown(invariant, "the prepared findings could not be compared with the canonical ones")
	}
	if !delta.BaselineComplete {
		return invariantUnknown(invariant,
			"the workspace held no current evidence about these files, so nothing in the report can be called new")
	}
	threshold := severityThreshold(invariant.Scope.Severity)
	var introduced []string
	for _, finding := range delta.New {
		if finding.Severity == 0 || finding.Severity > threshold {
			continue
		}
		introduced = append(introduced, describeNewFinding(finding))
	}
	answer := invariant
	answer.Coverage = workspacecore.Coverage{Complete: true, FilesRead: len(files), Semantic: "embedded_nvim"}
	if len(introduced) > 0 {
		answer.Status = workspacecore.InvariantViolated
		answer.Detail = fmt.Sprintf("%d new finding(s): %s", len(introduced), strings.Join(boundedDetails(introduced), "; "))
		return answer
	}
	answer.Status = workspacecore.InvariantProven
	answer.Detail = fmt.Sprintf("no new finding in %d staged file(s); %d resolved", len(files), len(delta.Resolved))
	return answer
}

// describeNewFinding names one finding and who in the plan caused it, because
// a violated invariant is only useful if it says what to go and look at.
func describeNewFinding(finding workspacecore.DeltaFinding) string {
	description := fmt.Sprintf("%s:%d %s", finding.Path, finding.Line, finding.Message)
	switch {
	case finding.Attribution.Tool != "":
		return description + " (" + finding.Attribution.Tool + ")"
	case len(finding.Attribution.Operations) > 0:
		return description + " (" + strings.Join(finding.Attribution.Operations, ", ") + ")"
	}
	return description
}

// noReferences asks the language server that read the staged bytes whether
// anything outside the declaration still refers to it.
func (e planInvariantEvaluator) noReferences(ctx context.Context, view providerpool.PreparedView, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	symbol := invariant.Scope.Symbol
	path := workspacePath(e.workspace, symbol.Path)
	outside, reason := e.stagedReferences(ctx, view, symbol)
	if reason != "" {
		return invariantUnknown(invariant, reason)
	}
	answer := invariant
	answer.Coverage = workspacecore.Coverage{Complete: true, Semantic: "embedded_nvim"}
	if len(outside) > 0 {
		answer.Status = workspacecore.InvariantViolated
		answer.Detail = fmt.Sprintf("%d reference(s) remain: %s", len(outside), strings.Join(boundedDetails(outside), "; "))
		return answer
	}
	answer.Status = workspacecore.InvariantProven
	answer.Detail = fmt.Sprintf("nothing outside %s refers to %s", path, symbol.NamePath)
	return answer
}

// pathUnreachable uses call evidence, never a non-call reference. Complete
// absence is available only inside the closed Go adapter's declared grammar.
func (e planInvariantEvaluator) pathUnreachable(ctx context.Context, view providerpool.PreparedView, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(workspacecore.MaxExecutionAnalysisMillis)*time.Millisecond)
	defer cancel()
	q, err := e.handlers.captureExecution(ctx, e.workspace, map[string]any{"use_provider": false}, &view)
	if err != nil {
		return invariantUnknown(invariant, "prepared execution evidence could not be acquired")
	}
	target, err := executionEndpoint(*q.snapshot.Execution, map[string]any{"path": invariant.Scope.Symbol.Path, "name_path": invariant.Scope.Symbol.NamePath})
	if err != nil {
		return invariantUnknown(invariant, "the target is not uniquely represented in the prepared execution graph")
	}
	for _, edge := range q.snapshot.Execution.Edges {
		if edge.To == target.ID && edge.From != target.ID && (edge.Kind == "calls" || edge.Kind == "dynamic_dispatch") {
			invariant.Status = workspacecore.InvariantViolated
			invariant.Detail = "A static call candidate still reaches " + target.Name
			invariant.Coverage = workspacecore.Coverage{Complete: false, Semantic: "go_parser_calls", Skipped: []string{"dynamic coverage remains incomplete"}}
			invariant.EvidenceIDs = []string{q.snapshot.ID, edge.ID}
			return invariant
		}
	}
	if !workspacecore.ClosedGoMainTarget(q.sources, target) {
		return invariantUnknown(invariant, "absence requires an unexported non-entry target in the closed Go main-package model")
	}
	q.closedProof(ctx, target, target)
	graph := q.snapshot.Execution
	if q.closedDirectory == nil || !graph.Coverage.Complete || graph.Coverage.Capped || len(graph.Coverage.Gaps) > 0 || len(graph.Coverage.Limits) > 0 {
		return invariantUnknown(invariant, "no call remains, but the relevant graph is not complete and uncapped")
	}
	mainFound, targetFound := false, false
	for _, node := range graph.Nodes {
		mainFound = mainFound || node.Name == "main"
		targetFound = targetFound || node.ID == target.ID
	}
	if !mainFound || !targetFound {
		return invariantUnknown(invariant, "closed proof lacks a main entry or exact target")
	}
	for _, edge := range graph.Edges {
		if edge.To == target.ID && edge.From != target.ID {
			return invariantUnknown(invariant, "complete graph retains an incoming call")
		}
	}
	if err = q.validate(ctx, e.handlers); err != nil {
		return invariantUnknown(invariant, "prepared source changed during proof")
	}
	invariant.Status = workspacecore.InvariantProven
	invariant.Detail = "No other function in this closed Go main package can invoke " + target.Name + "; the target is not externally exported or a runtime entry"
	invariant.Coverage = workspacecore.Coverage{Complete: true, Semantic: "closed_go_calls"}
	invariant.EvidenceIDs = []string{q.snapshot.ID}
	return invariant
}

// stagedReferences are the places outside the declaration that refer to it in
// the staged bytes. A non-empty reason means the question was not answered: a
// server that reports more references than it lists has not answered it
// either, and a count that cannot be trusted is worse than no count.
func (e planInvariantEvaluator) stagedReferences(ctx context.Context, view providerpool.PreparedView, symbol *workspacecore.PlanSymbolLocator) ([]string, string) {
	path := workspacePath(e.workspace, symbol.Path)
	arguments, err := preparedProviderTarget(view, e.workspace, map[string]any{
		"symbol_locator": map[string]any{"path": path, "name_path": symbol.NamePath},
	})
	if err != nil {
		return nil, err.Error()
	}
	value, failure := e.handlers.callPrepared(ctx, e.requestID+"_references", e.workspace, view, "references", arguments)
	if failure != nil {
		return nil, "no language server answered for the prepared revision"
	}
	payload, _ := value.(map[string]any)
	locations := referenceLocations(payload)
	if total := argInt(payload, "count", len(locations)); total > len(locations) {
		return nil, fmt.Sprintf("the server reported %d references but listed %d", total, len(locations))
	}
	var outside []string
	for _, location := range locations {
		file, ok := canonicalFacing(view, location.file)
		if !ok {
			file = workspacePath(e.workspace, location.file)
		}
		if file == path {
			continue
		}
		outside = append(outside, fmt.Sprintf("%s:%d", file, location.line))
	}
	return outside, ""
}

// apiCompatibleFileLimit bounds how many files one compatibility answer reads
// on each side of the change.
const apiCompatibleFileLimit = 20

// apiCompatible reads each affected file's exported surface as the workspace
// holds it and as the plan would leave it, and reports what the change breaks.
//
// A file no adapter reads is a gap, not a pass: the answer is unknown unless
// something breaking was already found, because one proven break is not made
// less true by a file nobody could read.
func (e planInvariantEvaluator) apiCompatible(plan workspacecore.PlanRecord, view providerpool.PreparedView, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	files := invariant.Scope.Paths
	if len(files) == 0 && plan.Preparation != nil {
		// Every file the plan affects, which unlike the staged set includes
		// the ones it deletes - and deleting a file removes everything it
		// exported.
		files = plan.Preparation.AffectedFiles
	}
	if len(files) == 0 {
		return invariantUnknown(invariant, "this preparation affects no file this invariant is about")
	}
	if len(files) > apiCompatibleFileLimit {
		return invariantUnknown(invariant, fmt.Sprintf(
			"the plan affects %d files and one answer reads %d", len(files), apiCompatibleFileLimit))
	}
	var breaking, gaps []string
	for _, named := range files {
		path := workspacePath(e.workspace, named)
		before := e.canonicalSurface(path)
		after, err := preparedSurface(view, path)
		if err != nil {
			return invariantUnknown(invariant, err.Error())
		}
		if !before.Covered || !after.Covered {
			gaps = append(gaps, path+": "+firstReason(before.Reason, after.Reason))
			continue
		}
		for _, change := range workspacecore.CompareAPISurfaces(before, after) {
			if change.Breaking() {
				breaking = append(breaking, change.Detail)
			}
		}
	}
	answer := invariant
	answer.Coverage = workspacecore.Coverage{
		Complete: len(gaps) == 0, FilesRead: len(files) - len(gaps), Semantic: "api_surface", Skipped: gaps,
	}
	switch {
	case len(breaking) > 0:
		answer.Status = workspacecore.InvariantViolated
		answer.Detail = fmt.Sprintf("%d incompatible change(s): %s",
			len(breaking), strings.Join(boundedDetails(breaking), "; "))
	case len(gaps) > 0:
		answer.Status = workspacecore.InvariantUnknown
		answer.Detail = fmt.Sprintf("%d file(s) no adapter could read: %s",
			len(gaps), strings.Join(boundedDetails(gaps), "; "))
	default:
		answer.Status = workspacecore.InvariantProven
		answer.Detail = fmt.Sprintf("nothing exported by %d affected file(s) was removed or changed", len(files))
	}
	return answer
}

// canonicalSurface is what the workspace promises today. A path it cannot read
// is a file the plan creates, which promised nothing before.
func (e planInvariantEvaluator) canonicalSurface(path string) workspacecore.APISurface {
	read, err := e.workspace.Read(path)
	if err != nil {
		return workspacecore.ReadAPISurface(path, nil, false)
	}
	return workspacecore.ReadAPISurface(path, read.Content, true)
}

// preparedSurface is what the plan would promise. A path that is not in the
// prepared tree is a file the plan deletes.
func preparedSurface(view providerpool.PreparedView, path string) (workspacecore.APISurface, error) {
	absolute, err := preparedPath(view, path)
	if err != nil {
		return workspacecore.APISurface{}, err
	}
	content, readErr := os.ReadFile(absolute)
	if readErr != nil {
		return workspacecore.ReadAPISurface(path, nil, false), nil
	}
	return workspacecore.ReadAPISurface(path, content, true), nil
}

func firstReason(reasons ...string) string {
	for _, reason := range reasons {
		if strings.TrimSpace(reason) != "" {
			return reason
		}
	}
	return "no reason given"
}

// stagedSymbolInvariant reads the staged file itself, and answers only where
// a parser read it: a name found by scanning text is a guess, and a guess is
// not a proof of anything, in either direction.
func stagedSymbolInvariant(view providerpool.PreparedView, invariant workspacecore.PlanInvariant) workspacecore.PlanInvariant {
	symbol := invariant.Scope.Symbol
	absolute, err := preparedPath(view, symbol.Path)
	if err != nil {
		return invariantUnknown(invariant, err.Error())
	}
	leaf := symbol.NamePath
	if index := strings.LastIndexAny(leaf, "/"); index >= 0 {
		leaf = leaf[index+1:]
	}
	// A file that is not there settles both assertions without a parser:
	// everything it declared is gone.
	present, parsed := false, true
	if _, err := os.Stat(absolute); err == nil {
		present, parsed = stagedDeclaration(absolute, leaf)
	} else if !os.IsNotExist(err) {
		return invariantUnknown(invariant, fmt.Sprintf("%s could not be read in the prepared revision", symbol.Path))
	}
	if !parsed {
		return invariantUnknown(invariant, fmt.Sprintf(
			"no native parser reads %s, so whether %s is declared there cannot be settled: a text scan finds the name in a call, an import or a string as readily as in a declaration",
			symbol.Path, leaf))
	}
	answer := invariant
	answer.Coverage = workspacecore.Coverage{Complete: true, FilesRead: 1, Semantic: "parser_sections"}
	wanted := invariant.Kind == workspacecore.InvariantSymbolExists
	answer.Status = workspacecore.InvariantViolated
	if present == wanted {
		answer.Status = workspacecore.InvariantProven
	}
	state := "is not declared in"
	if present {
		state = "is declared in"
	}
	answer.Detail = fmt.Sprintf("%s %s %s at %s", symbol.NamePath, state, symbol.Path, view.PreparedRevision)
	return answer
}

// invariantFiles are the staged paths one invariant is about: the ones it
// named, or every file the plan stages.
func (e planInvariantEvaluator) invariantFiles(view providerpool.PreparedView, invariant workspacecore.PlanInvariant) []string {
	if len(invariant.Scope.Paths) == 0 {
		return view.Files
	}
	wanted := map[string]bool{}
	for _, path := range invariant.Scope.Paths {
		wanted[workspacePath(e.workspace, path)] = true
	}
	var files []string
	for _, path := range view.Files {
		if wanted[path] {
			files = append(files, path)
		}
	}
	return files
}

// severityThreshold is the weakest finding a diagnostic invariant counts.
// Severity 1 is an error, and an empty setting counts everything.
func severityThreshold(name string) int {
	if rank := severityRank(name); rank > 0 {
		return rank
	}
	return 4
}

// boundedDetails keeps a refusal readable: the first few items, then how many
// more there are.
func boundedDetails(items []string) []string {
	if len(items) <= maxReportedReferences {
		return items
	}
	bounded := append([]string(nil), items[:maxReportedReferences]...)
	return append(bounded, fmt.Sprintf("and %d more", len(items)-maxReportedReferences))
}

func invariantUnknown(invariant workspacecore.PlanInvariant, detail string) workspacecore.PlanInvariant {
	invariant.Status = workspacecore.InvariantUnknown
	invariant.Detail = detail
	invariant.Coverage = workspacecore.Coverage{Complete: false, Skipped: []string{detail}}
	return invariant
}

// acceptableGaps are the dimensions of a prepared plan that one accept_provisional
// covers. A required invariant is deliberately not among them.
func acceptableGaps(plan workspacecore.PlanRecord) []string {
	gaps := workspacecore.PreparationGaps(plan)
	dimensions := make([]string, 0, len(gaps)+1)
	for _, gap := range gaps {
		dimensions = append(dimensions, gap.Dimension)
	}
	if len(dimensions) == 0 {
		// A plan that is PROVISIONAL without a named gap is provisional about
		// its diagnostics, which is what the committer synthesises too.
		dimensions = append(dimensions, "diagnostics")
	}
	return dimensions
}
